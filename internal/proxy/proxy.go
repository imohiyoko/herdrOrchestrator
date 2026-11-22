// Package proxy は代行依頼(tb proxy-*。ADR 0004 の独立した機能のモジュール)。判定と記録だけで、cmd は実行しない。
// 振る舞いは taskboard/lib/proxy.sh と同じにする。
package proxy

import (
	"bufio"
	"database/sql"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// Effects は申告できる副作用(書いた順が使い方の文面の順)。
var Effects = []string{"read-only", "write", "external"}

// Statuses は代行依頼の状態。
var Statuses = []string{"open", "done", "pending-human", "denied"}

// Trim は util.sh の trim と同じ(前後の空白を除く。空白は ASCII の空白だけとする)。
func Trim(s string) string { return strings.Trim(s, " \t\n\v\f\r") }

// Blank は core.sh の blank。
func Blank(s string) bool { return Trim(s) == "" }

// Request は依頼を記録し、id と状態を返す。同じ cmd が人に拒否済みなら、すぐに denied で記録し、その理由を返す。
func Request(db *sql.DB, session, taskID, cmd, why, effect, expect, now string) (id int64, status, result string, err error) {
	status = "open"
	var prior sql.NullInt64
	err = db.QueryRow("SELECT id FROM proxy_requests WHERE cmd=? AND status='denied' ORDER BY id LIMIT 1", cmd).Scan(&prior)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, "", "", err
	}
	if prior.Valid {
		status = "denied"
		result = "同一 cmd が人に拒否済み(依頼 " + strconv.FormatInt(prior.Int64, 10) + ")。同じ依頼は出し直さず、別の方法を提案すること"
	}
	r, err := db.Exec(`INSERT INTO main.proxy_requests (herdr_session, task_id, cmd, why, effect, expect, status, result, created_at, updated_at)
            VALUES (?, ?, ?, ?, ?, NULLIF(?,''), ?, NULLIF(?,''), ?, ?)`, session, taskID, cmd, why, effect, expect, status, result, now, now)
	if err != nil {
		return 0, "", "", err
	}
	id, err = r.LastInsertId()
	return id, status, result, err
}

// List は依頼を id の順に返す。status が空なら全部。full なら全列(--json)、でなければ表の列。
func List(db *sql.DB, status string, full bool, fn func(cols []string, vals []any) error) error {
	where, args := "1=1", []any{}
	if status != "" {
		where += " AND status=?"
		args = append(args, status)
	}
	q := "SELECT * FROM proxy_requests WHERE (" + where + ") ORDER BY id"
	if !full {
		q = "SELECT id, status, effect, task_id, replace(replace(cmd, char(10), ' '), char(9), ' ') FROM proxy_requests WHERE (" + where + ") ORDER BY id"
	}
	return store.Each(db, q, args, fn)
}

// ErrNotFound / ErrFinal は Resolve の失敗。
var (
	ErrNotFound = errors.New("not found")
	ErrFinal    = errors.New("final")
)

// Resolve は open か pending-human の依頼を status にする。id は数字の並び(bash と同じく SQL に埋める)。
// 解決済みなら今の状態と ErrFinal を返す。
func Resolve(db *sql.DB, session, id, status, result, now string) (cur string, err error) {
	err = db.QueryRow("SELECT status FROM proxy_requests WHERE id=" + id).Scan(&cur)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if cur != "open" && cur != "pending-human" {
		return cur, ErrFinal
	}
	_, err = db.Exec("UPDATE main.proxy_requests SET status=?, result=NULLIF(?,''), updated_at=? WHERE herdr_session=? AND id="+id,
		status, result, now, session)
	return cur, err
}

// Row は proxy-check に要る 1 件。
type Row struct{ Cmd, Effect, Status string }

// Get は id の依頼を返す。無ければ ErrNotFound。
func Get(db *sql.DB, id string) (Row, error) {
	var r Row
	err := db.QueryRow("SELECT cmd, effect, status FROM proxy_requests WHERE id=?", id).Scan(&r.Cmd, &r.Effect, &r.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

var (
	reHTTPWrite  = regexp.MustCompile(`(?i)(^|[[:space:]])(-X|--request|--method)[[:space:]=]*(POST|PUT|DELETE|PATCH)`)
	reWriteFlag  = regexp.MustCompile(`(^|[[:space:]])--(approve|merge|body|add)`)
	reOutputFlag = regexp.MustCompile(`(^|[[:space:]])--output`)
)

// anyLine は grep と同じく、行ごとに re を当てる。
func anyLine(re *regexp.Regexp, s string) bool {
	for _, l := range strings.Split(s, "\n") {
		if re.MatchString(l) {
			return true
		}
	}
	return false
}

// DetectMeta は detect_meta と同じ: cmd の副作用・連鎖・展開の兆候(クォートの中でも拾う)。
func DetectMeta(c string) []string {
	var m []string
	add := func(ok bool, name string) {
		if ok {
			m = append(m, name)
		}
	}
	add(strings.Contains(c, ">"), "リダイレクト(>)")
	add(strings.Contains(c, "<"), "リダイレクト(<)")
	add(strings.Contains(c, "|"), "パイプ/||")
	add(strings.Contains(c, ";"), "連結(;)")
	add(strings.Contains(c, "&"), "連結/バックグラウンド(&, &&)")
	add(strings.Contains(c, "$"), "展開($, $())")
	add(strings.Contains(c, "`"), "バッククォート")
	add(strings.ContainsAny(c, "\n\r"), "改行")
	add(anyLine(reHTTPWrite, c), "書き込み系の HTTP メソッド")
	add(anyLine(reWriteFlag, c), "書き込み系のフラグ(--approve/--merge/--body/--add)")
	add(anyLine(reOutputFlag, c), "ファイル出力(--output)")
	return m
}

// Check は proxy-check の出力の行を返す(1 行目が allow か human)。
func Check(r Row, allowlist string) []string {
	var reasons []string
	mismatch := ""
	if meta := DetectMeta(r.Cmd); len(meta) > 0 {
		metas := strings.Join(meta, "、")
		reasons = append(reasons, "危険な構文を含む: "+metas)
		if r.Effect == "read-only" {
			mismatch = "申告は read-only だが、コマンドに " + metas + " を含む"
		}
	}
	if r.Status == "denied" {
		reasons = append(reasons, "すでに denied")
	}
	if r.Effect != "read-only" {
		reasons = append(reasons, "申告 effect が "+r.Effect)
	}
	// 許可リスト(先頭一致。語の境界で比べる: "git log" は "git log -5" に一致、"git logx" には一致しない)
	matched := ""
	f, err := os.Open(allowlist)
	if err != nil {
		reasons = append(reasons, "許可リストを読めない("+allowlist+")。安全側で human")
	} else {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := Trim(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if r.Cmd == line || strings.HasPrefix(r.Cmd, line+" ") || strings.HasPrefix(r.Cmd, line+"\t") {
				matched = line
				break
			}
		}
		f.Close()
		if matched == "" {
			reasons = append(reasons, "許可リストにない")
		}
	}
	var out []string
	if len(reasons) == 0 {
		out = append(out, "allow", "match: "+matched)
	} else {
		out = append(out, "human")
		for _, s := range reasons {
			out = append(out, "reason: "+s)
		}
	}
	if mismatch != "" {
		out = append(out, "mismatch: "+mismatch)
	}
	return out
}
