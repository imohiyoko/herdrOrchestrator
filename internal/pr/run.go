package pr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/capv"
	"github.com/imohiyoko/herdrorchestrator/internal/notify"
)

// ghList は gh pr list の結果: 終了コードと、配列として読めたときの件数(読めなければ -1)。
func ghList(gh string, args ...string) (rc, n int) {
	b, err := exec.Command(gh, args...).Output()
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			return e.ExitCode(), -1
		}
		return 127, -1
	}
	d := json.NewDecoder(bytes.NewReader(b))
	var last any
	seen := false
	for {
		var v any
		if err := d.Decode(&v); err != nil {
			if err != io.EOF {
				return 0, -1
			}
			break
		}
		last, seen = v, true
	}
	a, ok := last.([]any)
	if !seen || !ok {
		return 0, -1
	}
	return 0, len(a)
}

func (q *Q) emit(ref, text string) {
	notify.New(q.DB, q.Sess).Emit(notify.Event{Type: "warning", Ref: ref, A: text})
}

// authLocked は pq_auth_locked: approved の行すべてに印を付け、印がまだ無かったときだけ 1 回通知する。attempts は使わない。
func (q *Q) authLocked(dry bool) error {
	fmt.Fprintln(q.Err, "pr-queue: 認証で止まっています(auth-locked)。残りの行は処理せず、次の回に自動で再開します")
	if dry {
		return nil
	}
	was := q.count(`SELECT COUNT(*) FROM pr_queue WHERE status='approved' AND last_error='auth-locked'`)
	if err := q.exec(`UPDATE main.pr_queue SET last_error='auth-locked' WHERE herdr_session=? AND status='approved'`, q.Sess); err != nil {
		return err
	}
	if was == 0 {
		q.emit("pr-queue-auth-locked", "pr-queue: 公開待ちがあるが、ghtkn が token を返せず止まっています。人の対話端末で ghtkn agent unlock --enable-refresh(ロック中)か ghtkn auth <app>(期限切れ)。再開は次の run が自動で拾います")
	}
	return nil
}

func (q *Q) capAttempts(id int64, dry bool) error {
	if dry {
		return nil
	}
	return q.exec(`UPDATE main.pr_queue SET status='failed', executed_at=? WHERE herdr_session=? AND id=? AND status='approved' AND attempts>=?`, now(), q.Sess, id, MaxAttempts)
}

func (q *Q) reject(id int64, why string) error {
	return q.exec(`UPDATE main.pr_queue SET status='rejected', last_error=?, executed_at=? WHERE herdr_session=? AND id=? AND status='approved'`, why, now(), q.Sess, id)
}

var reURL = func() *regexp.Regexp {
	r := regexp.MustCompile(`https://[^[:space:]]+/pull/[0-9]+`)
	r.Longest()
	return r
}()

// ErrStop は bash の return 1(理由は stderr に出し済み)。
var ErrStop = fmt.Errorf("pr-queue: 止めました")

// Run は pq_run: approved だけを処理する。dry は gh pr create も DB の更新もしない。
func (q *Q) Run(dry bool) error {
	gh, ok := GH(q.Err)
	if !ok {
		return ErrStop
	}
	if why, ok := repoDirOK(); !ok {
		fmt.Fprintf(q.Err, "pr-queue: %s。止めます(fail-closed。承認済みの行はそのまま残します)\n", why)
		return ErrStop
	}
	deny, ok := LoadDeny(q.Err)
	if !ok {
		return ErrStop
	}
	maxc := MaxCreate
	if v := Conf("max_create_per_run", strconv.Itoa(MaxCreate)); reNum.MatchString(v) {
		maxc, _ = strconv.Atoi(v)
	} else if capv.IsUnlimited(v) { // 上限なし
		maxc = capv.NoLimit
	}
	var ids []int64
	rows, err := q.DB.Query(`SELECT id FROM pr_queue WHERE status='approved' ORDER BY id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	made, locked := 0, false
	for _, id := range ids {
		if made >= maxc {
			q.say("pr-queue: 1 回の上限(%d 件)に達した。残りは次の回", maxc)
			break
		}
		r, err := q.row(strconv.FormatInt(id, 10))
		if err != nil {
			return err
		}
		if r == nil {
			continue
		}
		base, why, rc := q.baseCheck(r, false)
		if rc == 2 {
			q.say("PQ-%d skipped: %s(作らない。次の回に回す)", id, why)
			continue
		}
		if rc != 0 {
			q.say("PQ-%d rejected: %s", id, why)
			if !dry {
				if err := q.reject(id, "base: "+why); err != nil {
					return err
				}
			}
			continue
		}
		checked, why, rc := syncOrigin(r.Branch, base, dry)
		if rc == 2 {
			q.say("PQ-%d skipped: %s(検査できないので作らない。次の回に回す)", id, why)
			continue
		}
		if rc == 0 {
			why = guard(r, base)
		}
		if rc == 1 || why != "" {
			q.say("PQ-%d rejected: %s", id, why)
			if !dry {
				if err := q.reject(id, "ガード: "+why); err != nil {
					return err
				}
			}
			continue
		}
		why, rc = deny.check(r, base)
		if rc == 2 {
			q.say("PQ-%d skipped: %s(公開前検査ができないので作らない。次の回に回す)", id, why)
			continue
		}
		if rc != 0 {
			q.say("PQ-%d rejected: %s", id, why)
			if !dry {
				if err := q.reject(id, "公開前検査: "+why); err != nil {
					return err
				}
				q.emit("pq-deny-"+strconv.FormatInt(id, 10), fmt.Sprintf("PQ-%d は公開前検査で止めました(PR は作っていません): %s", id, why))
			}
			continue
		}
		lrc, n := ghList(gh, "pr", "list", "--repo", r.Repo, "--head", r.Branch, "--state", "open", "--json", "number")
		if lrc == AuthRC {
			if err := q.authLocked(dry); err != nil {
				return err
			}
			locked = true
			break
		}
		if lrc != 0 || n < 0 {
			q.say("PQ-%d skipped: 重複確認の gh pr list に失敗(作らない)", id)
			if !dry {
				if err := q.exec(`UPDATE main.pr_queue SET attempts=attempts+1, last_error='重複確認に失敗' WHERE herdr_session=? AND id=?`, q.Sess, id); err != nil {
					return err
				}
			}
			if err := q.capAttempts(id, dry); err != nil {
				return err
			}
			continue
		}
		if n > 0 {
			q.say("PQ-%d rejected: 同じ head の open PR が既にある", id)
			if !dry {
				if err := q.reject(id, "同じ head の open PR が既にある"); err != nil {
					return err
				}
			}
			continue
		}
		if dry {
			q.say("PQ-%d would-create: %s %s → %s (draft)", id, r.Repo, r.Branch, base)
			made++
			continue
		}
		if cur, ok := originSHA(r.Branch); !ok || cur != checked {
			q.say("PQ-%d skipped: 検査したあとに origin のブランチが変わった(か、確認できない)。作らない(次の回に回す)", id)
			continue
		}
		o, err := exec.Command(gh, "pr", "create", "--repo", r.Repo, "--base", base, "--head", r.Branch, "--title", r.Title, "--body-file", r.BodyFile, "--draft").Output()
		crc := 0
		if err != nil {
			crc = 127
			if e, ok := err.(*exec.ExitError); ok {
				crc = e.ExitCode()
			}
		}
		switch {
		case crc == 0:
			url := reURL.FindString(string(o))
			var u any
			if url != "" {
				u = url
			}
			if err := q.exec(`UPDATE main.pr_queue SET status='created', pr_url=?, head_sha=?, last_error=NULL, executed_at=? WHERE herdr_session=? AND id=? AND status='approved'`, u, checked, now(), q.Sess, id); err != nil {
				return err
			}
			if url != "" {
				q.say("PQ-%d created: %s", id, url)
			} else {
				q.say("PQ-%d created", id)
			}
			made++
		case crc == AuthRC:
			if err := q.authLocked(false); err != nil {
				return err
			}
			locked = true
		default:
			if err := q.exec(`UPDATE main.pr_queue SET attempts=attempts+1, last_error=? WHERE herdr_session=? AND id=?`, fmt.Sprintf("gh pr create が失敗(exit %d)", crc), q.Sess, id); err != nil {
				return err
			}
			q.say("PQ-%d error: gh pr create が失敗(exit %d)", id, crc)
			if err := q.capAttempts(id, false); err != nil {
				return err
			}
		}
		if locked {
			break
		}
	}
	if !locked && !dry {
		return q.exec(`UPDATE main.pr_queue SET last_error=NULL WHERE herdr_session=? AND last_error='auth-locked'`, q.Sess)
	}
	return nil
}

var reBraces = regexp.MustCompile(`\{\{[^}\n]*\}\}`) // grep は行ごと

// Scan は pq_scan: origin の先頭の接頭辞のブランチを走査し、本文があるものを積む。読み取りだけ(git は fetch と ls-remote、gh は pr list)。
func (q *Q) Scan(dry bool, repo string) error {
	repos := Repos()
	if len(repos) == 0 {
		fmt.Fprintln(q.Err, "pr-queue scan: 許可リスト(config/repos.md)が空")
		return nil
	}
	if repo == "" {
		if len(repos) != 1 {
			fmt.Fprintln(q.Err, "pr-queue scan: 許可リストが複数。--repo を付ける")
			return nil
		}
		repo = repos[0]
	}
	if !has(repos, repo) {
		fmt.Fprintln(q.Err, "pr-queue scan: リポジトリが許可リストに無い")
		return nil
	}
	if why, ok := repoDirOK(); !ok {
		fmt.Fprintf(q.Err, "pr-queue scan: %s。止めます(fail-closed)\n", why)
		return ErrStop
	}
	gh, ok := GH(q.Err)
	if !ok {
		return ErrStop
	}
	root, ok := BodyRoot()
	if !ok {
		fmt.Fprintln(q.Err, "pr-queue scan: 本文のルートが無い")
		return nil
	}
	st := "pending"
	if Conf("auto_approve", "0") == "1" {
		st = "approved"
	}
	pfx := Prefixes()[0]
	o, err := fakeGit("ls-remote", "--heads", "origin", "refs/heads/"+pfx+"*")
	if err != nil {
		fmt.Fprintln(q.Err, "pr-queue scan: origin の一覧を取れない")
		return nil
	}
	var heads []string
	for _, l := range lines(o) {
		b := ""
		if f := awkFields(l); len(f) >= 2 {
			b = f[1]
		}
		heads = append(heads, strings.TrimPrefix(b, "refs/heads/"))
	}
	if strings.Trim(strings.Join(heads, "\n"), "\n") == "" {
		q.say("pr-queue scan: 対象ブランチなし")
		return nil
	}
	if !dry {
		_, _ = fakeGit("fetch", "-q", "origin", "+refs/heads/"+pfx+"*:refs/remotes/origin/"+pfx+"*")
		if baseMode() == "default_branch" {
			if def, _, ok := defaultBase(); ok {
				_, _ = fakeGit("fetch", "-q", "origin", "+refs/heads/"+def+":refs/remotes/origin/"+def)
			}
		}
	}
	for _, b := range heads {
		if b == "" {
			continue
		}
		if !ValidBranch(b) {
			q.say("skip %s: ブランチ名が安全な形ではない", b)
			continue
		}
		cnt := q.count(`SELECT COUNT(*) FROM pr_queue WHERE repo=? AND branch=?`, repo, b)
		if q.count(`SELECT COUNT(*) FROM pr_queue WHERE repo=? AND branch=? AND status<>'rejected'`, repo, b) > 0 {
			q.say("skip %s: 既にキューにある", b)
			continue
		}
		if cnt >= MaxAttempts {
			q.say("skip %s: 同じブランチの試行が上限(%d)", b, MaxAttempts)
			continue
		}
		slug := strings.ReplaceAll(b, "/", "-")
		file := root + "/pr-body-" + slug + ".md"
		if lst, err := os.Lstat(file); err != nil || lst.Mode()&os.ModeSymlink != 0 || !lst.Mode().IsRegular() {
			q.say("skip %s: 本文(pr-body-%s.md)が無い", b, slug)
			continue
		}
		body, _ := os.ReadFile(file)
		if reBraces.Match(body) {
			q.say("skip %s: 本文にプレースホルダ {{…}} が残っている", b)
			continue
		}
		t, err := fakeGit("log", "-1", "--format=%s", "refs/remotes/origin/"+b)
		title, _, _ := strings.Cut(t, "\n")
		if err != nil {
			title = ""
		}
		title = strings.ReplaceAll(title, "\r", "")
		if strings.TrimFunc(title, func(r rune) bool { return strings.ContainsRune(" \t\n\v\f\r", r) }) == "" {
			q.say("skip %s: 最新コミットの題が空(か取得できない)", b)
			continue
		}
		if runeLen(title) > 200 {
			title = string([]rune(title)[:200])
		}
		sha := fileSHA(file)
		nb, src, why, ok := resolveBase("", b)
		if !ok {
			q.say("skip %s: %s", b, why)
			continue
		}
		r := &Row{Repo: repo, Branch: b, Title: title, BodyFile: file, BodySHA: sha, Base: nb, BaseSource: src}
		base, why, rc := q.baseCheck(r, false)
		if rc != 0 {
			q.say("skip %s: %s", b, why)
			continue
		}
		if why := guard(r, base); why != "" {
			q.say("skip %s: %s", b, why)
			continue
		}
		lrc, n := ghList(gh, "pr", "list", "--repo", repo, "--head", b, "--state", "all", "--json", "number")
		if lrc != 0 || n < 0 {
			q.say("skip %s: 重複確認の gh pr list に失敗", b)
			continue
		}
		if n > 0 {
			q.say("skip %s: 同じ head の PR が既にある", b)
			continue
		}
		if dry {
			q.say("would-queue %s (%s): %s", b, st, title)
			continue
		}
		ts := now()
		var dec any
		if st == "approved" {
			dec = ts
		}
		res, err := q.DB.Exec(`INSERT INTO main.pr_queue (herdr_session, repo,branch,base,base_source,title,body_file,body_sha256,requested_by,status,created_at,decided_at)
      VALUES (?, ?,?,?,?,?,?,?,'scan',?,?,?)`, q.Sess, repo, b, nb, src, title, file, sha, st, ts, dec)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		q.say("PQ-%d %s: %s", id, st, b)
	}
	return nil
}
