// Package review は review サービス(taskboard/lib/review.sh の review-pack。review-submit は gate と一緒に internal/gate と tbcli)。
package review

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var reSince = regexp.MustCompile(`^[0-9T:.Z-]{10,30}$`)

// ErrSince は --since の形の誤り。
var ErrSince = fmt.Errorf("review-pack: --since は ISO 8601")

// ErrJq は scheduler のログに jq が読めない行があったこと。bash は pipefail と set -e でその場で終わる(終了コード 5、何も言わない)。
var ErrJq = fmt.Errorf("review-pack: scheduler のログを読めません")

func q(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// cliValue は sqlite3 の CLI(-nullvalue -)と同じ出し方。
func cliValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "-"
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		s := strconv.FormatFloat(x, 'g', 15, 64) // %!.15g
		if strings.ContainsAny(s, "IN") {        // Inf / NaN
			return s
		}
		mant, exp, hasExp := strings.Cut(s, "e")
		if !strings.Contains(mant, ".") {
			mant += ".0"
		}
		if hasExp {
			return mant + "e" + exp
		}
		return mant
	case []byte:
		return string(x)
	case string:
		return x
	default:
		return fmt.Sprint(x)
	}
}

func tsv(db *sql.DB, w io.Writer, query string) error {
	rows, err := db.Query(query)
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
		out := make([]string, len(vals))
		for i, v := range vals {
			out[i] = cliValue(v)
		}
		fmt.Fprintln(w, strings.Join(out, " | "))
	}
	return rows.Err()
}

// Pack は cmd_review-pack: 会計役に渡すレビュー材料(集計と上位だけ)。since が空なら前回 --mark 以降(初回は 24 時間)。
func Pack(db *sql.DB, session, since string, mark bool, heldSQL string, w io.Writer) error {
	lim := os.Getenv("TB_REVIEW_TOP")
	if lim == "" {
		lim = "8"
	}
	logs := os.Getenv("TB_SCHED_LOGS")
	if logs == "" {
		logs = os.Getenv("TB_ROOT") + "/../scheduler/logs"
	}
	if since == "" {
		var s sql.NullString
		if err := db.QueryRow(`SELECT COALESCE(MAX(until_at), strftime('%Y-%m-%dT%H:%M:%SZ','now','-1 day')) FROM reviews`).Scan(&s); err != nil {
			return err
		}
		since = s.String
	}
	if !reSince.MatchString(since) {
		return ErrSince
	}
	until := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	wh := "created_at > " + q(since)
	sec := func(t string) { fmt.Fprintf(w, "\n## %s\n", t) }
	var qerr error
	t := func(query string) {
		if qerr == nil {
			qerr = tsv(db, w, query)
		}
	}
	fmt.Fprintf(w, "# レビュー材料(%s 〜 %s)\n", since, until)
	fmt.Fprintln(w, "生ログではなく集計です。詳しく見たいものだけ tb のコマンドで引いてください(tb messages は無いので sqlite3 で messages テーブル)。")
	sec("送信(種類 | 結果 | 件数 | 平均文字数)")
	t("SELECT kind, result, COUNT(*), CAST(AVG(length(body)) AS INTEGER) FROM messages WHERE " + wh + " GROUP BY kind, result ORDER BY COUNT(*) DESC;")
	sec("送れなかった宛先(宛先 | busy | failed)")
	t("SELECT target, SUM(result='busy'), SUM(result='failed') FROM messages WHERE " + wh + " AND result!='sent' GROUP BY target ORDER BY COUNT(*) DESC LIMIT " + lim + ";")
	sec("長い送信(トークンを食うもの: 種類 | 宛先 | 文字数 | 先頭)")
	t("SELECT kind, target, length(body), substr(replace(body, char(10), ' '),1,80) FROM messages WHERE " + wh + " ORDER BY length(body) DESC LIMIT 5;")
	sec("tb send(状態 | 件数)と拒否・承認待ちの例")
	t("SELECT status, COUNT(*) FROM outbox WHERE " + wh + " GROUP BY status;")
	t("SELECT 'O-'||id, status, from_name||' -> '||target, kind, COALESCE(reason,'') FROM outbox WHERE " + wh + " AND status IN ('denied','pending') ORDER BY id DESC LIMIT " + lim + ";")
	sec("人の承認(承認された送信の操作 | 回数)— 何度も承認されているものは allow にする候補")
	t("SELECT 'send:'||kind||':'||target, COUNT(*) FROM outbox WHERE " + wh + " AND reason LIKE '%人が承認%' GROUP BY kind, target ORDER BY COUNT(*) DESC LIMIT " + lim + ";")
	t("SELECT 'approvals 変更', pattern, value, created_at FROM approval_changes WHERE " + wh + " ORDER BY id;")
	sec("人への依頼の繰り返し(依頼元 | 種類 | 対象・内容 | 回数)— 同じ依頼を何度も人に出していないか(2 回以上だけ)")
	t(`SELECT who, what, substr(obj,1,40), COUNT(*) AS n FROM (
         SELECT from_name AS who, 'send:'||kind AS what, target AS obj FROM outbox
           WHERE ` + wh + ` AND (status='pending' OR reason LIKE '%人が承認%' OR status='rejected')
         UNION ALL
         SELECT requested_by, 'agent:'||op, target FROM agent_ops
           WHERE ` + wh + ` AND requested_by!='human' AND (decided_at IS NULL OR decided_at!=created_at)
         UNION ALL
         SELECT from_name, kind, replace(text, char(10), ' ') FROM comments
           WHERE ` + wh + ` AND kind IN ('question','blocked'))
       GROUP BY who, what, obj HAVING n >= 2 ORDER BY n DESC LIMIT ` + lim + `;`)
	sec("仕事の混在(agent | ロール | project 数 | ロール変更 | project)— 1 体が複数種類の仕事をしていないか(該当だけ)")
	t(`SELECT a.name, COALESCE(a.role,'-'), p.np, COALESCE(rc.n,0), p.ps
       FROM agents a
       LEFT JOIN (SELECT owner_agent, COUNT(DISTINCT project) AS np, group_concat(DISTINCT project) AS ps FROM tasks
                  WHERE owner_agent IS NOT NULL AND (status IN (` + heldSQL + `) OR updated_at > ` + q(since) + `)
                  GROUP BY owner_agent) p ON p.owner_agent=a.name
       LEFT JOIN (SELECT agent, COUNT(*) AS n FROM role_changes WHERE ` + wh + ` GROUP BY agent) rc ON rc.agent=a.name
       WHERE (COALESCE(p.np,0) > 1 OR COALESCE(rc.n,0) > 0) ORDER BY COALESCE(p.np,0) DESC, a.name LIMIT ` + lim + `;`)
	sec("報告(種類 | 件数)と、質問・ブロックの例")
	t("SELECT kind, COUNT(*) FROM comments WHERE " + wh + " GROUP BY kind;")
	t("SELECT 'C-'||id, kind, from_name, substr(replace(text, char(10), ' '),1,100) FROM comments WHERE " + wh + " AND kind IN ('question','blocked') ORDER BY id DESC LIMIT " + lim + ";")
	sec("まとめ送信(回数 | 平均件数 | 平均文字数)")
	t("SELECT COUNT(*), ROUND(AVG(item_count),1), CAST(AVG(length(body)) AS INTEGER) FROM digests WHERE " + wh + ";")
	sec("アラート(状態 | 緊急度 | 件数 | まとめた重複の合計 | 平均解析分)")
	t(`SELECT status, COALESCE(severity,'未解析'), COUNT(*), SUM(count)-COUNT(*),
         CAST(ROUND(AVG(CASE WHEN analyzed_at IS NOT NULL THEN (julianday(analyzed_at)-julianday(analysis_started_at))*1440 END)) AS INTEGER)
       FROM alerts WHERE last_seen > ` + q(since) + ` GROUP BY status, severity;`)
	if qerr == nil {
		fmt.Fprintln(w, "重複の多いアラート(見張り役の見回りが同じものを拾い続けていないか):")
	}
	t("SELECT 'A-'||id, count, COALESCE(severity,'-'), substr(title,1,60) FROM alerts WHERE last_seen > " + q(since) + " AND count > 1 ORDER BY count DESC LIMIT " + lim + ";")
	if qerr == nil {
		fmt.Fprintln(w, "low と判定された解析(誤報・過剰な見回りの候補):")
	}
	t("SELECT 'A-'||id, substr(title,1,60), substr(replace(COALESCE(result_summary,''), char(10), ' '),1,80) FROM alerts WHERE analyzed_at > " + q(since) + " AND severity='low' ORDER BY id DESC LIMIT " + lim + ";")
	if qerr != nil {
		return qerr
	}
	sec("観測(tb が見つけた滞留など: 種類 | 件数)")
	t("SELECT kind, COUNT(*) FROM observations WHERE " + wh + " GROUP BY kind;")
	sec("役割の配布(agent | 回数)— compact が多い agent は文脈が大きすぎる候補")
	t("SELECT agent, COUNT(*), SUM(source='compact') FROM role_briefings WHERE " + wh + " GROUP BY agent ORDER BY COUNT(*) DESC LIMIT " + lim + ";")
	if qerr != nil {
		return qerr
	}
	sec("定期実行(job | action | 件数)")
	if st, err := os.Stat(logs); err == nil && st.IsDir() {
		fs, _ := filepath.Glob(filepath.Join(logs, "*.log"))
		sort.Strings(fs)
		for _, f := range fs {
			if st, err := os.Stat(f); err != nil || !st.Mode().IsRegular() {
				continue
			}
			if !schedCounts(f, since, w) {
				return ErrJq
			}
		}
	} else {
		fmt.Fprintln(w, "(scheduler のログなし)")
	}
	sec("提案の状況(状態 | 件数)")
	t("SELECT status, COUNT(*) FROM proposals WHERE " + wh + " GROUP BY status;")
	if qerr != nil {
		return qerr
	}
	if mark {
		if _, err := db.Exec(`INSERT INTO main.reviews (herdr_session, since_at, until_at, created_at) VALUES (?, ?, ?, ?)`, session, since, until, until); err != nil {
			return err
		}
	}
	return nil
}

// schedCounts は tail -n 2000 | jq -r 'select((.ts // "") >= ($s[0:10])) | "\(.job) | \(.action)"' | sort | uniq -c | awk ...。
// jq は読めない行で止まる(それまでの行は数えて出し、false を返す)。
func schedCounts(f, since string, w io.Writer) bool {
	b, err := os.ReadFile(f)
	if err != nil {
		return false
	}
	ok := true
	ls := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(ls) > 2000 {
		ls = ls[len(ls)-2000:]
	}
	day := since
	if utf8.RuneCountInString(day) > 10 {
		day = string([]rune(day)[:10])
	}
	d := json.NewDecoder(bytes.NewReader([]byte(strings.Join(ls, "\n"))))
	d.UseNumber()
	var out []string
	for {
		var v any
		if err := d.Decode(&v); err != nil {
			ok = err == io.EOF
			break
		}
		m, isObj := v.(map[string]any)
		if !isObj && v != nil {
			ok = false
			break // .ts を引けない(jq はここでエラーで止まる)
		}
		ts, _ := m["ts"].(string)
		if raw, has := m["ts"]; has && raw != nil && raw != false {
			if _, isStr := raw.(string); !isStr {
				continue // 文字列でない ts は文字列より前に並ぶ(>= にならない)
			}
		}
		if ts < day {
			continue
		}
		out = append(out, interp(m["job"])+" | "+interp(m["action"]))
	}
	sort.Strings(out)
	for i := 0; i < len(out); {
		j := i
		for j < len(out) && out[j] == out[i] {
			j++
		}
		fmt.Fprintf(w, "%s | %d\n", strings.Join(strings.Fields(out[i]), " "), j-i)
		i = j
	}
	return ok
}

// interp は jq の文字列の中の \(x)。
func interp(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return "null"
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}
