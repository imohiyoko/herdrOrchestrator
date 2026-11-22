package pr

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/notify"
)

// jqNum は jq の数の出し方(整数ならそのまま、それ以外は 17 桁)。
func jqNum(n json.Number) string {
	if i, err := strconv.ParseInt(string(n), 10, 64); err == nil {
		return strconv.FormatInt(i, 10)
	}
	f, err := n.Float64()
	if err != nil {
		return string(n)
	}
	if f == float64(int64(f)) && f < 1e17 && f > -1e17 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', 17, 64)
}

// tostring は jq の tostring(配列・object は JSON。キーの並びは Go の並び。目安)。
func tostring(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return jqNum(x)
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(x)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

func truthy(v any) bool { return v != nil && v != false }

func alt(v, d any) any {
	if truthy(v) {
		return v
	}
	return d
}

// login は (.x.login? // "")。
func login(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	return alt(m["login"], "")
}

var (
	reCntrl  = regexp.MustCompile(`\p{Cc}`) // jq(Oniguruma)の [[:cntrl:]]
	reLogin  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	reTaskIn = func() *regexp.Regexp { r := regexp.MustCompile(`T-[A-Za-z0-9_]+-[0-9]{3,}`); r.Longest(); return r }()
)

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

type stmt struct {
	q    string
	args []any
}

// ticketEvent は pr_ticket_event_sql: PR の出来事をチケットの履歴に残す(状態は変えない)。
func ticketEvent(sess, kind, repo, num, tid, ts string) stmt {
	url := "https://github.com/" + repo + "/pull/" + num
	u := asciiLower(url)
	return stmt{`INSERT INTO main.task_events (herdr_session, task_id, kind, old, new, actor, ref, created_at)
    SELECT ?, t.id, ?, NULL, ?, 'system', ?, ? FROM tasks t
    WHERE t.id=? OR EXISTS (SELECT 1 FROM json_each(t.refs) r WHERE lower(json_extract(r.value, '$.url'))=?
      OR substr(lower(json_extract(r.value, '$.url')), 1, length(?)+1) IN (?||'/', ?||'?', ?||'#'))`,
		[]any{sess, kind, repo + "#" + num, url, ts, tid, u, u, u, u, u}}
}

func seenStmt(sess, key, value, ts string) stmt {
	return stmt{`INSERT INTO main.notify_seen (herdr_session, key, value, updated_at) VALUES (?, ?, ?, ?)
    ON CONFLICT(herdr_session, key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`, []any{sess, key, value, ts}}
}

func decodeLast(b []byte) (any, bool) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var last any
	seen := false
	for {
		var v any
		if err := d.Decode(&v); err == io.EOF {
			break
		} else if err != nil {
			return nil, false
		}
		last, seen = v, true
	}
	return last, seen
}

// Watch は cmd_pr-watch: 許可リストのリポジトリの PR を gh で読み(読み取りだけ)、前回との差から作成・approve・変更依頼・マージを積む。
// 初めて見るリポジトリは基準線として記録するだけ。許可リストが無い・空・通知が off・gh が無いなら何もせず 0。
func (q *Q) Watch() error {
	repos := notify.Repos()
	if len(repos) == 0 {
		return nil
	}
	nq := notify.New(q.DB, q.Sess)
	if !nq.C.On() {
		return nil
	}
	gh := envOr("TB_GH_BIN", "gh")
	if _, err := exec.LookPath(gh); err != nil {
		return nil
	}
	for _, r := range repos {
		b, err := exec.Command(gh, "pr", "list", "--repo", r, "--state", "all", "--limit", "30", "--json", "number,title,state,isDraft,reviewDecision,author,mergedBy").Output()
		if err != nil {
			continue
		}
		v, ok := decodeLast(b)
		arr, isArr := v.([]any)
		if !ok || !isArr {
			continue
		}
		var seen string
		if err := q.DB.QueryRow(`SELECT value FROM notify_seen WHERE key=?`, "prbase:"+r).Scan(&seen); err != nil && !isNoRows(err) {
			continue
		}
		prev := map[string]string{}
		rows, err := q.DB.Query(`SELECT key, value FROM notify_seen WHERE substr(key, 1, ?)=?`, len([]rune(r))+4, "pr:"+r+"#")
		if err != nil {
			continue
		}
		for rows.Next() {
			var k, val string
			if rows.Scan(&k, &val) == nil {
				prev[k] = val
			}
		}
		rows.Close()
		var stmts []stmt
		ts := now()
		for _, x := range arr {
			m, ok := x.(map[string]any)
			if !ok {
				continue
			}
			k := "pr:" + r + "#" + tostring(m["number"])
			p, hasprev := prev[k]
			draft := ""
			if truthy(m["isDraft"]) {
				draft = "draft"
			}
			f := []any{alt(m["number"], ""), alt(m["title"], ""), alt(m["state"], ""), alt(m["reviewDecision"], ""), draft, login(m["author"]), login(m["mergedBy"])}
			s := make([]string, len(f))
			for i, y := range f {
				s[i] = reCntrl.ReplaceAllString(tostring(y), " ")
			}
			p = reCntrl.ReplaceAllString(p, " ")
			num, title, state, dec, author, merger := s[0], s[1], s[2], s[3], s[5], s[6]
			if !reNum.MatchString(num) {
				continue
			}
			if !reLogin.MatchString(author) {
				author = ""
			}
			if !reLogin.MatchString(merger) {
				merger = ""
			}
			tid := reTaskIn.FindString(title)
			ref := r + "#" + num
			ev := ""
			if seen != "" {
				if !hasprev {
					if state == "MERGED" {
						ev = "pr_merged"
						nq.Emit(notify.Event{Type: ev, Ref: ref, A: title, C: merger})
					} else if state == "OPEN" {
						ev = "pr_opened"
						d := ""
						if tid != "" {
							d = "タスク " + tid
						}
						nq.Emit(notify.Event{Type: ev, Ref: ref, A: title, B: draft, C: author, D: d})
					}
				} else {
					pstate, pdec, _ := strings.Cut(p, "|")
					if !strings.Contains(p, "|") {
						pdec = p
					}
					if state == "MERGED" && pstate != "MERGED" {
						ev = "pr_merged"
						nq.Emit(notify.Event{Type: ev, Ref: ref, A: title, C: merger})
					} else if state == "OPEN" && dec != pdec {
						switch dec {
						case "APPROVED":
							ev = "pr_approved"
							nq.Emit(notify.Event{Type: ev, Ref: ref, A: title})
						case "CHANGES_REQUESTED":
							ev = "pr_changes"
							nq.Emit(notify.Event{Type: ev, Ref: ref, A: title})
						}
					}
				}
			}
			if ev != "" {
				stmts = append(stmts, ticketEvent(q.Sess, ev, r, num, tid, ts))
			}
			if !(hasprev && p == state+"|"+dec) {
				stmts = append(stmts, seenStmt(q.Sess, k, state+"|"+dec, ts))
			}
		}
		if seen == "" {
			stmts = append(stmts, seenStmt(q.Sess, "prbase:"+r, ts, ts))
		}
		if len(stmts) > 0 {
			q.commit(stmts)
		}
	}
	return nil
}

// commit は BEGIN IMMEDIATE; ...; COMMIT(失敗は黙って捨てる。どれか 1 つでも失敗したら何も残さない)。
func (q *Q) commit(stmts []stmt) {
	conn, err := q.DB.Conn(ctx())
	if err != nil {
		return
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx(), "BEGIN IMMEDIATE"); err != nil {
		return
	}
	for _, s := range stmts {
		if _, err := conn.ExecContext(ctx(), s.q, s.args...); err != nil {
			_, _ = conn.ExecContext(ctx(), "ROLLBACK")
			return
		}
	}
	if _, err := conn.ExecContext(ctx(), "COMMIT"); err != nil {
		_, _ = conn.ExecContext(ctx(), "ROLLBACK")
	}
}

func ctx() context.Context { return context.Background() }

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }
