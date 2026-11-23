package pr

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/identity"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/perms"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// Err は tb の終了: Code 2 は使い方(usage)、1 はそれ以外(die)。
type Err struct {
	Code int
	Msg  string
}

func (e *Err) Error() string { return e.Msg }

func usage(f string, a ...any) error { return &Err{2, fmt.Sprintf(f, a...)} }
func die(f string, a ...any) error   { return &Err{1, fmt.Sprintf(f, a...)} }

func ref(s string) (string, error) {
	r, ok := Ref(s)
	if !ok {
		return "", usage("pr-queue: ID は PQ-<数字>: %s", s)
	}
	return r, nil
}

// requireHuman はコマンドの中の人だけの確かめ(bash の require_human)。段階は権限段階の表(ADR 0006)と設定 policy の行で決まる。
func requireHuman(what string) error {
	var d *perms.Denied
	if err := perms.RequireHuman(what, nil); errors.As(err, &d) {
		return &Err{d.Code, d.Msg}
	} else if err != nil {
		return err
	}
	return nil
}

var reTaskID = regexp.MustCompile(`^T-[A-Za-z0-9_]+-[0-9]{3,}$`)

func blank(s string) bool {
	return strings.TrimFunc(s, func(r rune) bool { return strings.ContainsRune(" \t\n\v\f\r", r) }) == ""
}

// Add は pq_add: agent が依頼を置く(pending)。
func (q *Q) Add(args []string) error {
	var branch, file, title, repo, task, base, dep string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--branch", "--body-file", "--title", "--repo", "--task", "--base", "--depends-on":
			if i+1 >= len(args) {
				return usage("%s に値が必要です", a)
			}
			i++
			v := args[i]
			switch a {
			case "--branch":
				branch = v
			case "--body-file":
				file = v
			case "--title":
				title = v
			case "--repo":
				repo = v
			case "--task":
				task = v
			case "--base":
				base = v
			case "--depends-on":
				r, err := ref(v)
				if err != nil {
					return err
				}
				dep = r
			}
		default:
			return usage("pr-queue add: 不明な引数 %s", a)
		}
	}
	if branch == "" || file == "" || title == "" {
		return usage("pr-queue add --branch <接頭辞/…> --body-file <body_root の中のファイル> --title <題> [--repo <org/repo>] [--task T-…] [--base <ブランチ> | --depends-on PQ-<n>]")
	}
	if base != "" && dep != "" {
		return usage("pr-queue add: --base と --depends-on は一緒に使えません")
	}
	if !ValidBranch(branch) {
		return die("pr-queue: ブランチは %s のどれかで始め、英数字と . _ - / だけにします: %s", prefixList(), branch)
	}
	if strings.ContainsAny(title, "\n\r") || blank(title) || runeLen(title) > 200 {
		return die("pr-queue: 題は 1 行・200 字まで")
	}
	if task != "" && !reTaskID.MatchString(task) {
		return usage("タスク ID は T-<project>-NNN 形式: %s", task)
	}
	repos := Repos()
	if len(repos) == 0 {
		return die("pr-queue: 許可リスト(config/repos.md)が空です。人に聞いてください")
	}
	if repo == "" {
		if len(repos) != 1 {
			return die("pr-queue: 許可リストが複数あります。--repo を付けてください")
		}
		repo = repos[0]
	}
	if !has(repos, repo) {
		return die("pr-queue: リポジトリが許可リストにありません: %s", repo)
	}
	if why, ok := repoDirOK(); !ok {
		return die("pr-queue: %s", why)
	}
	if originState(branch) != 0 {
		return die("pr-queue: ブランチが origin に見つからない(か、確認できない)。先に push してください: %s", branch)
	}
	var nb, src, why string
	var ok bool
	if dep != "" {
		nb, src, why, ok = q.resolveStack(dep, branch, repo)
	} else {
		nb, src, why, ok = resolveBase(base, branch)
	}
	if !ok {
		return die("pr-queue: %s", why)
	}
	root, ok := BodyRoot()
	if !ok {
		return die("pr-queue: 本文のルート(body_root)が見つかりません")
	}
	abs, why := bodyAbs(file)
	if why != "" {
		if why == "本文ファイルの場所が不正" {
			return die("%s", why) // bash では cd が失敗して止まる
		}
		return die("pr-queue: 本文ファイルが無い(かシンボリックリンク): %s", file)
	}
	if !strings.HasPrefix(abs, root+"/") {
		return die("pr-queue: 本文ファイルは body_root の中だけ: %s", file)
	}
	if st, err := os.Stat(abs); err != nil || st.Size() > MaxBody {
		return die("pr-queue: 本文が大きすぎます(100KB まで)")
	}
	by := "human"
	if p := os.Getenv("HERDR_PANE_ID"); p != "" {
		n, _ := identity.PaneOwnerName(p)
		if by = n; by == "" {
			by = "unknown"
		}
	}
	var d, tk any
	if dep != "" {
		d, _ = strconv.ParseInt(dep, 10, 64)
	}
	if task != "" {
		tk = task
	}
	res, err := q.DB.Exec(`INSERT INTO main.pr_queue (herdr_session, repo,branch,base,base_source,depends_on,title,body_file,body_sha256,task_id,requested_by,status,created_at)
    VALUES (?, ?,?,?,?,?,?,?,?,?,?,'pending',?)`, q.Sess, repo, branch, nb, src, d, title, abs, fileSHA(abs), tk, by, now())
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	q.say("PQ-%d pending", id)
	return nil
}

var listCols = []string{"id", "repo", "branch", "base", "base_source", "depends_on", "title", "task_id", "requested_by", "status", "last_error", "attempts", "pr_url", "created_at"}

// List は pq_list。row は 1 行を jq -c の形にする関数(呼ぶ側の JSON の書き方)。
func (q *Q) List(args []string, rowJSON func([]string, []any) (string, error)) error {
	status, js := "", false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--status":
			if i+1 >= len(args) {
				return usage("--status に値が必要です")
			}
			i++
			status = args[i]
		case "--json":
			js = true
		default:
			return usage("pr-queue list [--status S] [--json]")
		}
	}
	switch status {
	case "", "pending", "approved", "rejected", "created", "failed":
	default:
		return usage("pr-queue: status は pending|approved|rejected|created|failed")
	}
	where, qa := "", []any{}
	if status != "" {
		where, qa = "WHERE status=?", []any{status}
	}
	if js {
		rows, err := q.DB.Query("SELECT "+strings.Join(listCols, ",")+" FROM pr_queue "+where+" ORDER BY id", qa...)
		if err != nil {
			return err
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				return err
			}
			s, err := rowJSON(cols, vals)
			if err != nil {
				return err
			}
			q.say("%s", s)
		}
		return rows.Err()
	}
	rows, err := q.DB.Query("SELECT 'PQ-'||id, status, branch, title, COALESCE(base,'main') FROM pr_queue "+where+" ORDER BY id", qa...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		v := make([]any, 5)
		if err := rows.Scan(&v[0], &v[1], &v[2], &v[3], &v[4]); err != nil {
			return err
		}
		s := make([]string, 5)
		for i := range v {
			s[i] = tickets.Text(v[i])
		}
		q.say("%s", strings.Join(s, "\t"))
	}
	return rows.Err()
}

func or(s, d string, null bool) string {
	if null {
		return d
	}
	return s
}

// Show は pq_show。
func (q *Q) Show(args []string) error {
	if len(args) != 1 {
		return usage("pr-queue show <PQ-n>")
	}
	id, err := ref(args[0])
	if err != nil {
		return err
	}
	r, err := q.row(id)
	if err != nil {
		return err
	}
	if r == nil {
		return die("見つかりません: PQ-%s", id)
	}
	dep := or(r.DependsOn, "null", r.IsNull("depends_on"))
	src := r.BaseSource
	if r.IsNull("base_source") {
		src = "fixed"
	}
	label, known := map[string]string{"fixed": "固定", "default": "origin の既定ブランチ", "explicit": "指定", "stack": "PQ-" + dep + " の上に積む"}[src]
	if !known {
		label = r.BaseSource
		if r.IsNull("base_source") {
			label = "null"
		}
	}
	q.say("PQ-%d %s", r.ID, r.Status)
	q.say("repo: %s", r.Repo)
	q.say("branch: %s  → base: %s(%s。draft)", r.Branch, or(r.Base, "main", r.IsNull("base")), label)
	q.say("title: %s", r.Title)
	q.say("body: %s", r.BodyFile)
	q.say("task: %s  依頼: %s", or(r.TaskID, "-", r.IsNull("task_id")), r.RequestedBy)
	q.say("試行: %s  PR: %s", r.Attempts, or(r.PRURL, "-", r.IsNull("pr_url")))
	q.say("理由/エラー: %s", or(r.LastError, "-", r.IsNull("last_error")))
	if !r.IsNull("depends_on") {
		var st string
		_ = q.DB.QueryRow(`SELECT status FROM pr_queue WHERE id=?`, r.DependsOn).Scan(&st)
		q.say("積み重ね: 下は PQ-%s(%s)。マージは下から。base が既定ブランチでない PR では CI が走らないことがある", r.DependsOn, st)
	}
	var ups any
	_ = q.DB.QueryRow(`SELECT group_concat('PQ-'||id||'('||status||')', ' ') FROM pr_queue WHERE depends_on=?`, r.ID).Scan(&ups)
	if u := tickets.Text(ups); u != "" {
		q.say("上に積まれている: %s(この PR を先にマージする)", u)
	}
	return nil
}

// Approve は pq_approve(人だけ)。本文・題・差分のガードも通ったときだけ approved にする。
func (q *Q) Approve(args []string) error {
	if len(args) != 1 {
		return usage("pr-queue approve <PQ-n>")
	}
	if err := requireHuman("tb pr-queue approve"); err != nil {
		return err
	}
	id, err := ref(args[0])
	if err != nil {
		return err
	}
	r, err := q.row(id)
	if err != nil {
		return err
	}
	if r == nil {
		return die("見つかりません: PQ-%s", id)
	}
	if r.Status != "pending" {
		return die("PQ-%s は %s です(承認できるのは pending だけ)", id, r.Status)
	}
	if why, ok := repoDirOK(); !ok {
		return die("PQ-%s は承認しません: %s", id, why)
	}
	base, why, rc := q.baseCheck(r, true)
	if rc != 0 {
		return die("PQ-%s は承認しません: %s", id, why)
	}
	if _, why, rc := syncOrigin(r.Branch, base, false); rc != 0 {
		return die("PQ-%s は承認しません: %s", id, why)
	}
	if why := guard(r, base); why != "" {
		return die("PQ-%s はガードに落ちるため承認しません: %s", id, why)
	}
	if err := q.exec(`UPDATE main.pr_queue SET status='approved', decided_at=?, last_error=NULL WHERE herdr_session=? AND id=? AND status='pending'`, now(), q.Sess, id); err != nil {
		return err
	}
	q.say("PQ-%s approved(base: %s。次の tb pr-queue run で draft PR を作ります)", id, base)
	return nil
}

// Reject は pq_reject(人だけ)。
func (q *Q) Reject(args []string) error {
	if len(args) < 1 {
		return usage("pr-queue reject <PQ-n> [-m 理由]")
	}
	id, err := ref(args[0])
	if err != nil {
		return err
	}
	note := ""
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "-m", "--message":
			if i+1 >= len(args) {
				return usage("%s に値が必要です", args[i])
			}
			i++
			note = args[i]
		default:
			return usage("pr-queue reject: 不明な引数 %s", args[i])
		}
	}
	if runeLen(note) > 300 {
		return usage("pr-queue reject: 理由は 300 字まで")
	}
	if err := requireHuman("tb pr-queue reject"); err != nil {
		return err
	}
	r, err := q.row(id)
	if err != nil {
		return err
	}
	if r == nil {
		return die("見つかりません: PQ-%s", id)
	}
	if r.Status != "pending" && r.Status != "approved" {
		return die("PQ-%s は %s です(却下できるのは pending / approved だけ)", id, r.Status)
	}
	msg := "人が却下"
	if note != "" {
		msg += ": " + note
	}
	if err := q.exec(`UPDATE main.pr_queue SET status='rejected', decided_at=?, last_error=? WHERE herdr_session=? AND id=? AND status IN ('pending','approved')`, now(), msg, q.Sess, id); err != nil {
		return err
	}
	q.say("PQ-%s rejected", id)
	return nil
}

// Retry は pq_retry(人だけ): failed を approved に戻す(attempts を 0 に)。ガードは run のたびにかけ直す。
func (q *Q) Retry(args []string) error {
	if len(args) != 1 {
		return usage("pr-queue retry <PQ-n>")
	}
	if err := requireHuman("tb pr-queue retry"); err != nil {
		return err
	}
	id, err := ref(args[0])
	if err != nil {
		return err
	}
	r, err := q.row(id)
	if err != nil {
		return err
	}
	if r == nil {
		return die("見つかりません: PQ-%s", id)
	}
	if r.Status != "failed" {
		return die("PQ-%s は %s です(戻せるのは failed だけ)", id, r.Status)
	}
	if err := q.exec(`UPDATE main.pr_queue SET status='approved', attempts=0, last_error=NULL, executed_at=NULL WHERE herdr_session=? AND id=? AND status='failed'`, q.Sess, id); err != nil {
		return err
	}
	q.say("PQ-%s approved(failed から戻しました。次の tb pr-queue run が、ガードを通して draft PR を作ります)", id)
	return nil
}

// Cmd は cmd_pr-queue: サブコマンドに振り分ける。
func (q *Q) Cmd(args []string, rowJSON func([]string, []any) (string, error)) error {
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "add":
		return q.Add(args)
	case "list":
		return q.List(args, rowJSON)
	case "show":
		return q.Show(args)
	case "approve":
		return q.Approve(args)
	case "reject":
		return q.Reject(args)
	case "run":
		dry := false
		if len(args) > 0 && args[0] != "" { // case ${1:-} in "") ;; --dry-run) ...(2 つ目からは見ない)
			if args[0] != "--dry-run" {
				return usage("pr-queue run [--dry-run]")
			}
			dry = true
		}
		return q.Run(dry)
	case "scan":
		dry, repo := false, ""
		for i := 0; i < len(args); i++ {
			switch args[i] {
			case "--dry-run":
				dry = true
			case "--repo":
				if i+1 >= len(args) {
					return usage("--repo に値が必要です")
				}
				i++
				repo = args[i]
			default:
				return usage("pr-queue scan [--dry-run] [--repo <org/repo>]")
			}
		}
		return q.Scan(dry, repo)
	case "retry":
		return q.Retry(args)
	}
	return usage("pr-queue add|list|show|approve|reject|retry|run|scan")
}
