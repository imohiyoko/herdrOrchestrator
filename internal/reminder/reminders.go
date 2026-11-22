// Package reminder は reminder サービス(taskboard/lib/reminders.sh の tb reminders と、send.sh の tb nudge)。
package reminder

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
	"github.com/imohiyoko/herdrorchestrator/internal/notify"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// NoChange は --check で新しく出すものが無いときの終了コード。
const NoChange = 3

// Env は環境変数で変えられる値(reminders.sh の REMINDERS_* と DIGEST_QUEUED_SEC)。
type Env struct {
	StaleHours, DueDays, QueuedSec string
	Deadlines                      string
}

// LoadEnv は既定つきで環境変数を読む。
func LoadEnv() Env {
	get := envOr // ${TB_...:-既定}: 空も既定
	return Env{
		StaleHours: get("TB_REMINDERS_STALE_HOURS", "24"),
		DueDays:    get("TB_REMINDERS_DUE_DAYS", "7"),
		QueuedSec:  get("TB_DIGEST_QUEUED_SEC", "300"),
		Deadlines:  get("TB_REMINDERS_DEADLINES", filepath.Join(os.Getenv("TB_ROOT"), "config", "deadlines.conf")),
	}
}

var reDigits = regexp.MustCompile(`^[0-9]+$`)

// Valid は 3 つの数が数字か。
func (e Env) Valid() bool {
	return reDigits.MatchString(e.StaleHours) && reDigits.MatchString(e.DueDays) && reDigits.MatchString(e.QueuedSec)
}

// Item は 1 項目。
type Item struct {
	P                                    int
	Key, Sig, What, Who, Due, Ref, Since string
	Age, Status, First                   string
	Day                                  int
}

// R は 1 回の実行。
type R struct {
	DB      *sql.DB
	Session string
	St      tickets.StatusSet
	Env     Env
	Today   string
}

func dueSQL(d, t string) string {
	return fmt.Sprintf(`CASE WHEN date(%s) < date(%s) THEN 'overdue' WHEN date(%s) = date(%s) THEN 'today'
    WHEN date(%s) = date(%s, '+1 day') THEN 'tomorrow' ELSE 'soon' END`, d, t, d, t, d, t)
}

func (r *R) collectDB() ([]Item, error) {
	t, sqs := store.Quote(r.Today), store.Quote(r.Session)
	closed, active := r.St.SQLList("done", "cancelled"), r.St.SQLList("active")
	last := "MAX(updated_at, COALESCE((SELECT MAX(e.created_at) FROM main.task_events e WHERE e.herdr_session=" + sqs + " AND e.task_id=tasks.id), ''))"
	st := dueSQL("due", t)
	parts := []string{
		`SELECT 2 AS p, 'q:'||c.id AS key, c.kind AS sig,
      CASE c.kind WHEN 'question' THEN '質問' ELSE 'ブロック報告' END || ': ' || c.task_id || ' ' || COALESCE(t.title, '') AS what,
      '人' AS who, '' AS due, 'C-'||c.id AS ref, c.created_at AS since
    FROM comments c LEFT JOIN tasks t ON t.id=c.task_id
    WHERE c.kind IN ('question','blocked') AND c.resolved_at IS NULL AND COALESCE(t.status, '') NOT IN (` + closed + `)`,
		`SELECT 2, 'o:'||id, status, '送信の承認待ち: ' || from_name || ' → ' || target || ' (' || kind || ')', '人', '', 'O-'||id, created_at
    FROM outbox WHERE status='pending'`,
		`SELECT 2, 'l:'||id, status, 'agent 操作の承認待ち: ' || op || ' ' || target || ' (' || requested_by || ')', '人', '', 'L-'||id, created_at
    FROM agent_ops WHERE status='pending'`,
		`SELECT 2, 'p:'||id, status, '代行依頼の判断待ち: ' || task_id || ' (' || effect || ')', '人', '', 'proxy '||id, updated_at
    FROM proxy_requests WHERE status='pending-human'`,
		`SELECT 2, 'a:'||id, status, '解析役の起動待ち: ' || title, '人', '', 'A-'||id, last_seen
    FROM alerts WHERE status='awaiting-approval'`,
		`SELECT 3, 'stall:'||id, status || ':' || ` + last + `, '停滞: ' || id || ' ' || title, COALESCE(owner_agent, '-'), '', id, ` + last + `
    FROM tasks WHERE status IN (` + active + `) AND (julianday('now') - julianday(` + last + `)) * 24 > ` + r.Env.StaleHours,
		`SELECT 2, 'prchg:'||e.task_id||':'||e.new, 'E-'||e.id, '変更依頼の PR: ' || e.new || ' (' || e.task_id || ' ' || t.title || ')',
        COALESCE(t.owner_agent, '人'), '', e.task_id, e.created_at
      FROM main.task_events e JOIN tasks t ON t.id=e.task_id
      WHERE e.herdr_session=` + sqs + ` AND e.kind='pr_changes' AND t.status NOT IN (` + closed + `)
        AND e.id = (SELECT MAX(x.id) FROM main.task_events x WHERE x.herdr_session=` + sqs + ` AND x.task_id=e.task_id AND x.new=e.new AND x.kind LIKE 'pr\_%' ESCAPE '\')`,
		`SELECT 4, 'queue:'||id, status, '宛先に届いていない送信: ' || from_name || ' → ' || target || ' (' || kind || ')', target, '', 'O-'||id, created_at
    FROM outbox WHERE status='queued' AND (julianday('now') - julianday(created_at)) * 86400 >= ` + r.Env.QueuedSec,
		`SELECT CASE WHEN ` + st + ` = 'soon' THEN 4 ELSE 1 END, 'due:'||id, (` + st + `) || ':' || due,
        '期限: ' || id || ' ' || title, COALESCE(owner_agent, '人'), due, id, created_at
      FROM tasks WHERE status NOT IN (` + closed + `) AND due IS NOT NULL AND due != ''
        AND date(due) <= date(` + t + `, '+` + r.Env.DueDays + ` days')`,
		`SELECT 4, 'inbox:'||id, 'unread', '未読の保留メッセージ: ' || from_name || ' → ' || recipient || ' (' || kind || ')',
        recipient, '', 'I-'||id, created_at
      FROM main.inbox WHERE herdr_session=` + sqs + ` AND read_at IS NULL`,
	}
	var out []Item
	err := store.Each(r.DB, "SELECT * FROM ("+strings.Join(parts, " UNION ALL ")+")", nil, func(_ []string, v []any) error {
		p, _ := v[0].(int64)
		out = append(out, Item{P: int(p), Key: tickets.Text(v[1]), Sig: tickets.Text(v[2]), What: tickets.Text(v[3]),
			Who: tickets.Text(v[4]), Due: tickets.Text(v[5]), Ref: tickets.Text(v[6]), Since: tickets.Text(v[7])})
		return nil
	})
	return out, err
}

var reDeadline = regexp.MustCompile(`^deadline[[:space:]]*=[[:space:]]*([0-9]{4}-[0-9]{2}-[0-9]{2})[[:space:]]*\|(.*)$`)

// Deadline は期限ファイルの 1 行。
type Deadline struct{ Date, Label string }

func isRegular(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// Deadlines は reminders_deadline_lines。ファイルが無ければ ok=false。
func (e Env) Lines() ([]Deadline, bool) {
	if !isRegular(e.Deadlines) {
		return nil, false
	}
	data, err := os.ReadFile(e.Deadlines)
	if err != nil {
		return nil, true
	}
	s := string(data)
	s = strings.TrimSuffix(s, "\n")
	var out []Deadline
	if s == "" && len(data) == 0 {
		return nil, true
	}
	for _, line := range strings.Split(s, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = config.Trim(line)
		m := reDeadline.FindStringSubmatch(line)
		if m == nil || config.Trim(m[2]) == "" {
			continue
		}
		out = append(out, Deadline{m[1], config.Trim(m[2])})
	}
	return out, true
}

// DeadlineTicket は reminders_deadline_ticket: 同じ題・期日のチケットの ID(無ければ空)。
func DeadlineTicket(db *sql.DB, d, label string) string {
	var id string
	_ = db.QueryRow(`SELECT id FROM tasks WHERE due=? AND title=? ORDER BY id LIMIT 1`, d, label).Scan(&id)
	return id
}

func (r *R) collectFile() []Item {
	lines, _ := r.Env.Lines()
	var out []Item
	t := store.Quote(r.Today)
	for _, l := range lines {
		if DeadlineTicket(r.DB, l.Date, l.Label) != "" {
			continue
		}
		d := store.Quote(l.Date)
		var st string
		_ = r.DB.QueryRow(`SELECT CASE WHEN date(` + d + `) IS NULL THEN 'bad' WHEN date(` + d + `) > date(` + t + `, '+` + r.Env.DueDays + ` days') THEN 'later'
                ELSE ` + dueSQL(d, t) + ` END`).Scan(&st)
		if st == "bad" || st == "later" {
			continue
		}
		sum := sha256.Sum256([]byte(l.Label))
		p := 1
		if st == "soon" {
			p = 4
		}
		out = append(out, Item{P: p, Key: "due:f:" + hex.EncodeToString(sum[:])[:10], Sig: st + ":" + l.Date, What: "期限: " + l.Label,
			Who: "人", Due: l.Date, Ref: "deadline " + l.Date})
	}
	return out
}

// jqStr は jq の (x // "") を文字列にしたもの("\(x)" と同じ書き方)。
func jqStr(raw json.RawMessage) (string, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" || s == "false" {
		return "", false
	}
	var v string
	if s[0] == '"' && json.Unmarshal(raw, &v) == nil {
		return v, true
	}
	return s, true
}

func (r *R) collectPRs() []Item {
	gh := os.Getenv("TB_GH_BIN")
	if gh == "" {
		gh = "gh"
	}
	var out []Item
	for _, repo := range notify.Repos() {
		b, err := exec.Command(gh, "pr", "list", "--repo", repo, "--state", "open", "--limit", "30", "--json", "number,title,isDraft,reviewDecision,createdAt").Output()
		if err != nil {
			continue
		}
		var prs []map[string]json.RawMessage
		if json.Unmarshal(b, &prs) != nil {
			continue
		}
		for _, pr := range prs {
			draft, _ := jqStr(pr["isDraft"])
			if draft != "" { // isDraft が true(null と false は下書きではない)
				continue
			}
			dec, has := jqStr(pr["reviewDecision"])
			if dec != "" && dec != "REVIEW_REQUIRED" {
				continue
			}
			sig := dec
			if !has {
				sig = "none"
			}
			num, _ := jqStr(pr["number"])
			title, _ := jqStr(pr["title"])
			since, _ := jqStr(pr["createdAt"])
			out = append(out, Item{P: 2, Key: "pr:" + repo + "#" + num, Sig: sig, What: "レビュー待ちの PR: " + title,
				Who: "レビュー担当", Ref: repo + "#" + num, Since: since})
		}
	}
	return out
}

func ctlBar(s string) string {
	return strings.Map(func(c rune) rune {
		if unicode.IsControl(c) || c == '|' {
			return ' '
		}
		return c
	}, s)
}

func runes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

var reSpaces = regexp.MustCompile(` +`)

func cleanWhat(s string) string {
	s = reSpaces.ReplaceAllString(ctlBar(s), " ")
	if strings.HasPrefix(s, " ") { // sub("^ | $"; ""): 先頭の空白か、無ければ末尾の空白を 1 つ
		s = s[1:]
	} else {
		s = strings.TrimSuffix(s, " ")
	}
	return runes(s, 80)
}

var reFrac = regexp.MustCompile(`\.[0-9]+Z$`)

func parseISO(s string) (time.Time, error) {
	return time.Parse("2006-01-02T15:04:05Z", s)
}

// age は jq の age($s)。読めない時刻なら err(jq がエラーで止まるのと同じ)。
func age(now, since string) (string, error) {
	if since == "" {
		return "", nil
	}
	n, err := parseISO(reFrac.ReplaceAllString(now, "Z"))
	if err != nil {
		return "", err
	}
	s := reFrac.ReplaceAllString(since, "Z")
	s = strings.TrimSuffix(s, "+00:00")
	if strings.HasSuffix(since, "+00:00") && !strings.HasSuffix(s, "Z") {
		s += "Z"
	}
	t, err := parseISO(s)
	if err != nil {
		return "", errors.New("since")
	}
	d := int64(n.Sub(t).Seconds())
	switch {
	case d >= 86400:
		return strconv.FormatInt(floorDiv(d, 86400), 10) + "d", nil
	case d >= 3600:
		return strconv.FormatInt(floorDiv(d, 3600), 10) + "h", nil
	}
	return strconv.FormatInt(max(floorDiv(d, 60), 0), 10) + "m", nil
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// Collect は reminders_collect: 全項目を優先度・期限・古い順・キーの順に。
func (r *R) Collect(now string) ([]Item, error) {
	db, err := r.collectDB()
	if err != nil {
		return nil, err
	}
	all := append(append(db, r.collectFile()...), r.collectPRs()...)
	for i := range all {
		all[i].What = cleanWhat(all[i].What)
		all[i].Who = runes(ctlBar(all[i].Who), 40)
		a, err := age(now, all[i].Since)
		if err != nil {
			return nil, err
		}
		all[i].Age = a
	}
	// unique_by(.key): キーの順に並べ、同じキーは最初のもの
	sort.SliceStable(all, func(i, j int) bool { return all[i].Key < all[j].Key })
	var u []Item
	for i, it := range all {
		if i > 0 && it.Key == all[i-1].Key {
			continue
		}
		u = append(u, it)
	}
	or := func(s, d string) string {
		if s == "" {
			return d
		}
		return s
	}
	sort.SliceStable(u, func(i, j int) bool {
		a, b := u[i], u[j]
		if a.P != b.P {
			return a.P < b.P
		}
		if x, y := or(a.Due, "9999"), or(b.Due, "9999"); x != y {
			return x < y
		}
		if x, y := or(a.Since, "~"), or(b.Since, "~"); x != y {
			return x < y
		}
		return a.Key < b.Key
	})
	return u, nil
}

// State は reminders:state の 1 項目。
type State struct {
	Sig   string `json:"sig"`
	First string `json:"first"`
}

// Diff は reminders_diff の結果。
type Diff struct {
	Items   []Item
	Removed []string
	Full    bool
	Changed bool
}

func days(a, b string) int {
	x, err1 := time.Parse("2006-01-02", a)
	y, err2 := time.Parse("2006-01-02", b)
	if err1 != nil || err2 != nil {
		return 0
	}
	return int(floorDiv(int64(y.Sub(x).Seconds()), 86400))
}

// MakeDiff は前回の記録と比べる。
func MakeDiff(items []Item, prev map[string]State, today, fullDay string, full bool) Diff {
	d := Diff{Full: full}
	cur := map[string]bool{}
	for _, it := range items {
		o, ok := prev[it.Key]
		switch {
		case !ok:
			it.Status = "new"
		case o.Sig != it.Sig:
			it.Status = "changed"
		default:
			it.Status = "continuing"
		}
		it.First = today
		if ok && o.First != "" {
			it.First = o.First
		}
		it.Day = days(it.First, today) + 1
		cur[it.Key] = true
		d.Items = append(d.Items, it)
		if it.Status != "continuing" {
			d.Changed = true
		}
	}
	for k := range prev {
		if !cur[k] {
			d.Removed = append(d.Removed, k)
		}
	}
	sort.Strings(d.Removed)
	if len(d.Removed) > 0 || (len(items) > 0 && fullDay != today) {
		d.Changed = true
	}
	return d
}

// StateJSON は .state(項目の順の {key: {sig, first}})。
func (d Diff) StateJSON() string {
	var b strings.Builder
	b.WriteByte('{')
	for i, it := range d.Items {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(util.JQString(it.Key) + `:{"sig":` + util.JQString(it.Sig) + `,"first":` + util.JQString(it.First) + "}")
	}
	return b.String() + "}"
}

// ParseState は reminders:state を読む(オブジェクトでなければ空)。
func ParseState(s string) map[string]State {
	m := map[string]State{}
	var raw map[string]json.RawMessage
	if json.Unmarshal([]byte(s), &raw) != nil {
		return m
	}
	for k, v := range raw {
		var st State
		_ = json.Unmarshal(v, &st)
		m[k] = st
	}
	return m
}

var prio = map[int]string{1: "急ぎ", 2: "人の番", 3: "停滞", 4: "待ち・予定"}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// Text は reminders_text。
func (d Diff) Text(now string) string {
	var rows, cont []Item
	nNew, nChg := 0, 0
	for _, it := range d.Items {
		switch it.Status {
		case "new":
			nNew++
		case "changed":
			nChg++
		}
		if d.Full || it.Status != "continuing" {
			rows = append(rows, it)
		} else {
			cont = append(cont, it)
		}
	}
	var lines []string
	h := fmt.Sprintf("[reminders %s / %d 件", now, len(d.Items))
	if d.Full {
		h += " / 全件"
	} else {
		h += fmt.Sprintf(" / 新規 %d・変化 %d・継続 %d", nNew, nChg, len(cont))
	}
	if len(d.Removed) > 0 {
		h += fmt.Sprintf("・解消 %d", len(d.Removed))
	}
	lines = append(lines, h+"]")
	if len(rows) > 0 {
		lines = append(lines, "優先 | 何が | 誰の番 | 期限 | 参照 | 経過")
	}
	for _, it := range rows {
		suf := ""
		if it.Status == "changed" {
			suf = " (変化)"
		} else if it.Status == "new" && !d.Full {
			suf = " (新規)"
		}
		lines = append(lines, prio[it.P]+" | "+it.What+suf+" | "+it.Who+" | "+dash(it.Due)+" | "+it.Ref+" | "+dash(it.Age))
	}
	if len(cont) > 0 {
		var cs []string
		for _, it := range cont {
			cs = append(cs, fmt.Sprintf("%s(%d 日目)", it.Ref, it.Day))
		}
		lines = append(lines, "継続: "+strings.Join(cs, ", "))
	}
	if len(d.Items) == 0 {
		lines = append(lines, "忘れているかもしれないものはありません")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// ItemsJSON は items の {p, key, what, who, due, ref, age, status, day} の配列。
func (d Diff) ItemsJSON() string {
	var b strings.Builder
	b.WriteByte('[')
	for i, it := range d.Items {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"p":%d,"key":%s,"what":%s,"who":%s,"due":%s,"ref":%s,"age":%s,"status":%s,"day":%d}`, it.P,
			util.JQString(it.Key), util.JQString(it.What), util.JQString(it.Who), util.JQString(it.Due), util.JQString(it.Ref),
			util.JQString(it.Age), util.JQString(it.Status), it.Day)
	}
	return b.String() + "]"
}

// RemovedJSON は removed の配列。
func (d Diff) RemovedJSON() string {
	var qs []string
	for _, k := range d.Removed {
		qs = append(qs, util.JQString(k))
	}
	return "[" + strings.Join(qs, ",") + "]"
}

// Redact は題に紛れ込んだ秘密らしい値を伏せる(本文の text と、全項目の what / who)。
func (d *Diff) Redact(text string) string {
	all := text + "\n"
	var ws []string
	for _, it := range d.Items {
		ws = append(ws, it.What, it.Who)
	}
	all += strings.Join(ws, "\n")
	kinds, hits := notify.Scan(all)
	if kinds == "" {
		return text
	}
	text = notify.Redact(text, hits) + "\n(秘密らしい値を伏せました: " + kinds + ")"
	for _, h := range hits {
		for _, l := range strings.Split(h, "\n") {
			if l == "" {
				continue
			}
			for i := range d.Items {
				d.Items[i].What = strings.ReplaceAll(d.Items[i].What, l, "[伏せた秘密]")
				d.Items[i].Who = strings.ReplaceAll(d.Items[i].Who, l, "[伏せた秘密]")
			}
		}
	}
	return text
}
