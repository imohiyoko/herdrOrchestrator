package pr

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
	"github.com/imohiyoko/herdrorchestrator/internal/notify"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }

// Row は pr_queue の 1 行(session_row pr_queue id N)。
type Row struct {
	ID                                        int64
	Repo, Branch, Title, BodyFile, BodySHA    string
	Status, Base, BaseSource, DependsOn, Head string
	TaskID, RequestedBy, LastError, PRURL     string
	Attempts                                  string
	null                                      map[string]bool
}

func (r *Row) IsNull(c string) bool { return r.null[c] }

// Q は PR キュー(1 回の tb の実行のあいだ)。
type Q struct {
	DB     *sql.DB
	Sess   string
	W, Err io.Writer
}

func (q *Q) row(id string) (*Row, error) {
	var r *Row
	err := store.Each(q.DB, `SELECT * FROM pr_queue WHERE id=?`, []any{id}, func(cols []string, vals []any) error {
		if r != nil {
			return nil
		}
		r = &Row{null: map[string]bool{}}
		for i, c := range cols {
			v := tickets.Text(vals[i])
			if vals[i] == nil {
				r.null[c] = true
			}
			switch c {
			case "id":
				r.ID, _ = strconv.ParseInt(v, 10, 64)
			case "repo":
				r.Repo = v
			case "branch":
				r.Branch = v
			case "title":
				r.Title = v
			case "body_file":
				r.BodyFile = v
			case "body_sha256":
				r.BodySHA = v
			case "status":
				r.Status = v
			case "base":
				r.Base = v
			case "base_source":
				r.BaseSource = v
			case "depends_on":
				r.DependsOn = v
			case "head_sha":
				r.Head = v
			case "task_id":
				r.TaskID = v
			case "requested_by":
				r.RequestedBy = v
			case "last_error":
				r.LastError = v
			case "pr_url":
				r.PRURL = v
			case "attempts":
				r.Attempts = v
			}
		}
		return nil
	})
	return r, err
}

func (q *Q) exec(stmt string, args ...any) error {
	_, err := q.DB.Exec(stmt, args...)
	return err
}

func (q *Q) count(query string, args ...any) int {
	var n int
	_ = q.DB.QueryRow(query, args...).Scan(&n)
	return n
}

func (q *Q) say(format string, a ...any) { fmt.Fprintf(q.W, format+"\n", a...) }

// ---------- base(PR の向き先) ----------

// defaultBase は pq_default_base: いまの設定での既定の base。取れなければ理由つきで false。
func defaultBase() (string, string, bool) {
	if baseMode() != "default_branch" {
		return BaseFixed, "", true
	}
	b, ok := defaultBranch()
	if !ok {
		return "", "origin の既定ブランチを確かめられない", false
	}
	return b, "", true
}

var reAllowPat = regexp.MustCompile(`^[A-Za-z0-9._/*-]{1,200}$`)

// baseAllowed は pq_base_allowed: 既定と同じか、base_allow(空白かカンマ区切りのグロブ)のどれかに合う。
func baseAllowed(b, def string) bool {
	if b == def {
		return true
	}
	for _, p := range strings.Fields(strings.ReplaceAll(Conf("base_allow", ""), ",", " ")) {
		if reAllowPat.MatchString(p) && util.Glob(p, b) {
			return true
		}
	}
	return false
}

// resolveBase は pq_resolve_base: 新しい行の base と決め方。
func resolveBase(want, head string) (base, src, reason string, ok bool) {
	def, why, ok := defaultBase()
	if !ok {
		return "", "", why, false
	}
	if want == "" {
		src = "fixed"
		if baseMode() == "default_branch" {
			src = "default"
		}
		if def == head {
			return "", "", "base(" + def + ")が head と同じ", false
		}
		return def, src, "", true
	}
	switch {
	case !validRef(want):
		return "", "", "base はブランチ名として安全な形(英数字と . _ - /)だけ: " + want, false
	case want == head:
		return "", "", "base が head と同じ: " + want, false
	case !baseAllowed(want, def):
		return "", "", "base(" + want + ")は許可されていない(既定の base の " + def + " か、config/pr-queue.conf の base_allow の形だけ)", false
	case originState(want) != 0:
		return "", "", "base(" + want + ")が origin に見つからない(か、確認できない)", false
	}
	return want, "explicit", "", true
}

// resolveStack は pq_resolve_stack: 積み重ねの base(下の行のブランチ)。
func (q *Q) resolveStack(low, head, repo string) (base, src, reason string, ok bool) {
	r, err := q.row(low)
	if err != nil || r == nil {
		return "", "", "下の行が見つからない: PQ-" + low, false
	}
	if r.Repo != repo {
		return "", "", "下の PQ-" + low + " は別のリポジトリ", false
	}
	if r.Status != "pending" && r.Status != "approved" && r.Status != "created" {
		return "", "", "下の PQ-" + low + " は " + r.Status + "(積めるのは pending / approved / created の上だけ)", false
	}
	if r.Status == "created" && r.Head == "" {
		return "", "", "下の PQ-" + low + " は作成時のコミットが記録されていない(この機能より前に作った PR)", false
	}
	if r.Branch == head {
		return "", "", "下の PQ-" + low + " と同じブランチ", false
	}
	if originState(r.Branch) != 0 {
		return "", "", "下のブランチ(" + r.Branch + ")が origin に見つからない(か、確認できない)", false
	}
	return r.Branch, "stack", "", true
}

var reNum = regexp.MustCompile(`^[0-9]+$`)

// baseCheck は pq_base_check: 行の base をかけ直す。approve なら、下の行を待っているだけのときは通す。
func (q *Q) baseCheck(r *Row, approve bool) (base, reason string, rc int) {
	if r.Base == "" && r.BaseSource == "" {
		return BaseFixed, "", 0 // base を持たない古い行は今までどおり main
	}
	b := r.Base
	if !validRef(b) {
		return BaseFixed, "base が安全な形ではない", 1
	}
	switch r.BaseSource {
	case "fixed":
		if b != BaseFixed {
			return BaseFixed, "base が固定の " + BaseFixed + " ではない(行が書き換えられている)", 1
		}
		return BaseFixed, "", 0
	case "default":
		def, ok := defaultBranch()
		if !ok {
			return BaseFixed, "origin の既定ブランチを確かめられない", 2
		}
		if def != b {
			return BaseFixed, "origin の既定ブランチが、登録したときの " + b + " から変わった(登録し直す)", 1
		}
	case "explicit":
		def, why, ok := defaultBase()
		if !ok {
			return BaseFixed, why, 2
		}
		if !baseAllowed(b, def) {
			return BaseFixed, "base(" + b + ")が許可されていない(既定の base か base_allow の形だけ)", 1
		}
	case "stack":
		if !reNum.MatchString(r.DependsOn) {
			return BaseFixed, "積み重ねの下の行が記録されていない", 1
		}
		low, err := q.row(r.DependsOn)
		if err != nil || low == nil {
			return BaseFixed, "下の行(PQ-" + r.DependsOn + ")が見つからない", 1
		}
		if low.Branch != b || low.Repo != r.Repo {
			return BaseFixed, "base が下の行のブランチと合わない(行が書き換えられている)", 1
		}
		lid := strconv.FormatInt(low.ID, 10)
		switch low.Status {
		case "rejected", "failed":
			return BaseFixed, "下の PQ-" + lid + " が " + low.Status + "(上の PR も作らない)", 1
		case "created":
			if low.Head == "" {
				return BaseFixed, "下の PQ-" + lid + " の作成時のコミットが記録されていない", 1
			}
		default:
			if !approve {
				return BaseFixed, "下の PQ-" + lid + " がまだ PR になっていない(" + low.Status + "。待つ)", 2
			}
		}
		if low.Head != "" {
			cur, ok := originSHA(b)
			rc := 0
			if !ok {
				if rc = originState(b); rc == 0 {
					rc = 2
				}
			}
			if rc == 2 {
				return BaseFixed, "下のブランチ(" + b + ")を origin で確かめられない", 2
			}
			if rc != 0 {
				return BaseFixed, "下のブランチ(" + b + ")が origin に無い(マージで消えた等。base を決め直して登録し直す)", 1
			}
			if cur != low.Head {
				return BaseFixed, "下のブランチ(" + b + ")が、下の PR を作ったときのコミットから変わった(積み直しは人がする)", 1
			}
		}
	default:
		return BaseFixed, "base の決め方が不明: " + r.BaseSource, 1
	}
	switch originState(b) {
	case 2:
		return BaseFixed, "base(" + b + ")が origin にあるか確かめられない", 2
	case 1:
		return BaseFixed, "base(" + b + ")が origin に無い", 1
	}
	return b, "", 0
}

// ---------- 本文・題・差分のガード ----------

// bodyAbs は本文ファイルの物理パス(シンボリックリンクでない普通のファイル)。
func bodyAbs(file string) (string, string) {
	if st, err := os.Lstat(file); err != nil || st.Mode()&os.ModeSymlink != 0 {
		return "", "本文ファイルが無い(か、シンボリックリンク)"
	}
	if st, err := os.Stat(file); err != nil || !st.Mode().IsRegular() {
		return "", "本文ファイルが無い(か、シンボリックリンク)"
	}
	dir, ok := physDir(filepath.Dir(file))
	if !ok {
		return "", "本文ファイルの場所が不正"
	}
	return filepath.Join(dir, filepath.Base(file)), ""
}

// guard は pq_guard: 通れば ""。落ちれば理由(値は含めない)。
func guard(r *Row, base string) string {
	if !ValidBranch(r.Branch) {
		return "ブランチ名が許可された接頭辞(" + prefixList() + ")で始まる安全な形ではない"
	}
	if !has(Repos(), r.Repo) {
		return "リポジトリが許可リストに無い"
	}
	root, ok := BodyRoot()
	if !ok {
		return "本文のルートが見つからない"
	}
	abs, why := bodyAbs(r.BodyFile)
	if why != "" {
		return why
	}
	if !strings.HasPrefix(abs, root+"/") {
		return "本文ファイルが許可された場所(body_root)の外"
	}
	if fileSHA(abs) != r.BodySHA {
		return "本文が、置かれたときから変わっている(承認のやり直しが要る)"
	}
	b, _ := os.ReadFile(abs)
	body := string(b)
	first, rest, _ := strings.Cut(body, "\n")
	first = strings.ReplaceAll(first, "\r", "")
	if re := Conf("body_first_line", ""); re != "" {
		if !ereMatch(re, first, false) {
			return "本文 1 行目が body_first_line の形ではない"
		}
		body = strings.TrimRight(rest, "\n") // tail -n +2 を $(...) で取ったもの
	} else {
		body = strings.TrimRight(body, "\n")
	}
	if kinds, _ := notify.Scan(r.Title + "\n" + body); kinds != "" {
		return "本文か題に秘密らしい値がある(種類: " + kinds + ")"
	}
	return diffGuard(r.Branch, base)
}

var (
	reCI    = regexp.MustCompile(`(?m)^\.github/(workflows|actions)/`)
	reAdded = regexp.MustCompile(`^\+[^+]`)
)

// diffGuard は pq_diff_guard: 差分を見る(必須。clone は読むだけ)。
func diffGuard(b, base string) string {
	if why, ok := repoDirOK(); !ok {
		return why
	}
	ref := "refs/remotes/origin/" + b
	if !hasCommit(ref) {
		return "差分を取得できない(ブランチの追跡参照が clone に無い)"
	}
	if !hasCommit("refs/remotes/origin/" + base) {
		return "差分を取得できない(origin/" + base + " が clone に無い)"
	}
	rng := "origin/" + base + "..." + ref
	files, err := realGit("-c", "core.quotePath=false", "diff", "-z", "--name-only", rng)
	if err != nil {
		return "差分を取得できない"
	}
	if reCI.MatchString(strings.ReplaceAll(files, "\x00", "\n")) {
		return "CI 設定(.github/workflows)を含む。人が作る"
	}
	nums, err := realGit("diff", "--numstat", rng)
	if err != nil {
		return "差分を取得できない"
	}
	for _, l := range lines(nums) {
		if f := strings.Split(l, "\t"); len(f) >= 2 && f[0] == "-" && f[1] == "-" {
			return "バイナリの変更を含む(内容を検査できない)。人が作る"
		}
	}
	d, err := realGit("-c", "core.quotePath=false", "diff", "-U0", rng)
	if err != nil {
		return "差分を取得できない"
	}
	var added []string
	for _, l := range lines(d) {
		if reAdded.MatchString(l) {
			added = append(added, l)
		}
	}
	a := strings.Join(added, "\n")
	if strings.Contains(a, "AdministratorAccess") {
		return "AdministratorAccess を足す差分を含む。人が作る"
	}
	if k := notify.PatternKinds(a); k != "" {
		return "差分に秘密らしい値がある(種類: " + k + ")"
	}
	return ""
}

// Ref は pq_ref: "PQ-12" か "12" → "12"。
func Ref(s string) (string, bool) {
	r := strings.TrimPrefix(s, "PQ-")
	return r, reNum.MatchString(r)
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }
