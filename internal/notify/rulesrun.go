package notify

import (
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// LoadRules は nrule_load: 効いている規則(誤りのある行は飛ばす)。
func (q *Q) LoadRules() []Rule {
	body, _, _ := config.Lookup(q.DB, q.Sess, "notify-rules")
	rules, _ := ParseRules(body)
	return rules
}

const rank = "CASE %s WHEN 'highest' THEN 1 WHEN 'high' THEN 2 WHEN 'medium' THEN 3 WHEN 'low' THEN 4 ELSE 5 END"

// where は nrule_where: 規則 1 行の条件 → SQL(e = task_events、t = tasks)。
func where(kind string, conds []string) string {
	w := "1=1"
	if kind != "*" {
		w += " AND e.kind=" + store.Quote(kind)
	}
	for _, tok := range conds {
		m := reCond.FindStringSubmatch(tok)
		if m == nil {
			continue
		}
		k, op, v := m[1], m[2], store.Quote(m[3])
		switch k {
		case "type":
			w += " AND t.type=" + v
		case "priority":
			if op == ">=" {
				w += " AND " + fmt.Sprintf(rank, "t.priority") + " <= " + fmt.Sprintf(rank, v)
			} else {
				w += " AND t.priority=" + v
			}
		case "label":
			switch kind { // ラベルの付け外しのイベントでは、付け外したラベルそのもの。ほかは今そのラベルを持っているか
			case "label_added":
				w += " AND e.new=" + v
			case "label_removed":
				w += " AND e.old=" + v
			default:
				w += " AND EXISTS (SELECT 1 FROM json_each(t.labels) WHERE value=" + v + ")"
			}
		case "project":
			w += " AND t.project=" + v
		case "owner":
			w += " AND t.owner_agent=" + v
		case "new":
			w += " AND e.new=" + v
		case "old":
			w += " AND e.old=" + v
		case "actor":
			if m[3] == "agent" {
				w += " AND e.actor NOT IN ('human','system')"
			} else {
				w += " AND e.actor=" + v
			}
		}
	}
	return w
}

// Describe は nrule_describe: イベントの説明(本文に載せる 1 句)。
func Describe(kind, o, n string) string {
	switch kind {
	case "created":
		return "作成(" + n + ")"
	case "status":
		return "状態 " + o + " → " + n
	case "label_added":
		return "ラベル +" + n
	case "label_removed":
		return "ラベル -" + o
	case "priority":
		return "優先度 " + o + " → " + n
	case "type":
		return "種別 " + o + " → " + n
	case "owner":
		return "担当 " + or(o, "-") + " → " + or(n, "-")
	case "due":
		return "期日 " + or(o, "-") + " → " + or(n, "-")
	case "comment":
		return "報告(" + n + ")"
	case "link":
		return "リンク " + n
	case "unlink":
		return "リンク解除 " + o
	case "ref_added":
		return "外部リンク追加"
	case "ref_removed":
		return "外部リンク削除"
	case "title":
		return "題の変更"
	case "description":
		return "説明の変更"
	case "overdue":
		return "期限切れ(期日 " + n + ")"
	case "due_soon":
		return "期限間近(期日 " + n + ")"
	case "pr_opened":
		return "PR " + n + " を出した"
	case "pr_approved":
		return "PR " + n + " が approve された"
	case "pr_changes":
		return "PR " + n + " に変更依頼"
	case "pr_merged":
		return "PR " + n + " がマージされた"
	}
	return kind
}

// Hit は 1 件のイベントに当たった規則(届け先を合わせたもの)。
type Hit struct {
	Event int64
	Sinks string // 空白区切り
	Lines string // 当たった行番号(カンマ区切り)
}

// Apply は nrule_apply: 範囲 (from, to] のイベントに規則を当てる。dry なら積まずに当たりだけを返す。
func (q *Q) Apply(rules []Rule, from, to int64, dry bool) ([]Hit, error) {
	var qs []string
	for _, r := range rules {
		qs = append(qs, fmt.Sprintf(`SELECT e.id AS eid, %d AS ln, %s AS sinks FROM main.task_events e JOIN tasks t ON t.id=e.task_id
           WHERE e.herdr_session=%s AND e.id > %d AND e.id <= %d AND %s`, r.Line, store.Quote(strings.Join(r.Sinks, " ")), store.Quote(q.Sess), from, to, where(r.Kind, r.Conds)))
	}
	if len(qs) == 0 {
		return nil, nil
	}
	rows, err := q.DB.Query("SELECT eid, ln, sinks FROM (" + strings.Join(qs, " UNION ALL ") + ") ORDER BY eid, ln")
	if err != nil {
		return nil, err
	}
	var hits []Hit
	var seen []string
	for rows.Next() {
		var id, ln int64
		var sinks string
		if err := rows.Scan(&id, &ln, &sinks); err != nil {
			rows.Close()
			return nil, err
		}
		if len(hits) == 0 || hits[len(hits)-1].Event != id {
			hits = append(hits, Hit{Event: id})
			seen = nil
		}
		h := &hits[len(hits)-1]
		for _, s := range strings.Fields(sinks) {
			if !slices.Contains(seen, s) {
				seen = append(seen, s)
				h.Sinks = strings.TrimPrefix(h.Sinks+" "+s, " ")
			}
		}
		h.Lines = strings.TrimPrefix(h.Lines+","+strconv.FormatInt(ln, 10), ",")
	}
	rows.Close()
	if dry {
		return hits, nil
	}
	for _, h := range hits {
		var tid, title, ty, pr, ow, kd string
		var o, n sql.NullString
		if q.DB.QueryRow(`SELECT e.task_id, t.title, t.type, t.priority, COALESCE(t.owner_agent,'-') AS owner, e.kind, e.old, e.new
                   FROM main.task_events e JOIN tasks t ON t.id=e.task_id WHERE e.herdr_session=? AND e.id=?`, q.Sess, h.Event).Scan(&tid, &title, &ty, &pr, &ow, &kd, &o, &n) != nil {
			continue
		}
		q.Emit(Event{Type: "ticket", Ref: fmt.Sprintf("E-%d", h.Event), Sinks: h.Sinks,
			A: tid + " " + title, B: Describe(kd, o.String, n.String) + " / " + pr + " / " + ty + " / 担当 " + ow})
	}
	return hits, nil
}

var reNum = regexp.MustCompile(`^[0-9]+$`)

// windowMin は TB_NOTIFY_RULES_WINDOW_MIN(既定 60)。
func windowMin() int {
	if v := os.Getenv("TB_NOTIFY_RULES_WINDOW_MIN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 60
}

// RunRules は notify_rules_run: 新しいイベントに規則を当てる(読み位置は notify_seen の rules:last)。
func (q *Q) RunRules() error {
	if !q.C.On() {
		return nil
	}
	rules := q.LoadRules()
	var max int64
	if err := q.DB.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM main.task_events WHERE herdr_session=?`, q.Sess).Scan(&max); err != nil {
		return err
	}
	last := q.SeenGet("rules:last")
	if !reNum.MatchString(last) {
		last = ""
	}
	l, _ := strconv.ParseInt(last, 10, 64)
	if max <= l {
		return nil
	}
	var r sql.Result
	var err error
	ms := strconv.FormatInt(max, 10)
	if last == "" {
		r, err = q.DB.Exec(`INSERT OR IGNORE INTO main.notify_seen (herdr_session, key, value, updated_at) VALUES (?, 'rules:last', ?, ?)`, q.Sess, ms, now())
	} else {
		r, err = q.DB.Exec(`UPDATE main.notify_seen SET value=?, updated_at=? WHERE herdr_session=? AND key='rules:last' AND value=?`, ms, now(), q.Sess, last)
	}
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n != 1 { // 別の tb が先に取った
		return nil
	}
	if last == "" || len(rules) == 0 { // 初めて(基準線を引いただけ)か、規則なし(読み位置だけ進める)
		return nil
	}
	var from int64
	if err := q.DB.QueryRow(fmt.Sprintf(`SELECT COALESCE(MIN(id) - 1, %d) FROM main.task_events WHERE herdr_session=? AND id > %d AND id <= %d
              AND julianday(created_at) >= julianday('now') - %d / 1440.0`, max, l, max, windowMin()), q.Sess).Scan(&from); err != nil {
		return err
	}
	_, err = q.Apply(rules, from, max, false)
	return err
}
