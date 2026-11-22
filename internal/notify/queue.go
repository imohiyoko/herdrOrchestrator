package notify

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/transport"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
)

func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }

// Q は通知のキュー(このセッションの notify_events)。
type Q struct {
	DB   *sql.DB
	Sess string
	C    *Cfg
	// ActiveSQL は進行中の分類(active・review)の状態の SQL の並び(定期まとめに使う)。
	ActiveSQL string
}

// New は設定を読んだキュー。
func New(db *sql.DB, session string) *Q { return &Q{DB: db, Sess: session, C: Load(db, session)} }

// Event は積むイベント(notify_enqueue の引数)。
type Event struct {
	Type, Ref, Sev string
	A, B, C, D     string
	Force          bool   // off でも積む(NOTIFY_FORCE=1)
	Sinks          string // 届け先を決めて渡すとき(通知の規則。空なら種別の既定)
}

func hasWord(list, w string) bool { return strings.Contains(" "+list+" ", " "+w+" ") }

// Enqueue は notify_enqueue: 秘匿フィルタ・整形をして積み、file / stdout へ書く。off・重複のときは何もせず 0。
// 知らない種別なら err。
func (q *Q) Enqueue(e Event) (int64, error) {
	ok := false
	for _, t := range Types {
		ok = ok || t == e.Type
	}
	if !ok {
		return 0, errors.New("不明な種別: " + e.Type)
	}
	if !e.Force && !q.C.On() {
		return 0, nil
	}
	key := e.Type + ":" + e.Ref
	if win := q.C.Int("dedupe_sec"); e.Ref != "" && win > 0 {
		var one int
		err := q.DB.QueryRow(`SELECT 1 FROM notify_events WHERE dedupe_key=? AND (julianday('now') - julianday(created_at)) * 86400 < ? LIMIT 1`, key, win).Scan(&one)
		if err == nil {
			return 0, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}
	ref, sev, ra, rb, rc, rd := e.Ref, e.Sev, e.A, e.B, e.C, e.D
	blocked, reason := false, ""
	if e.Type != "test" {
		if kinds, hits := Scan(ref + "\n" + sev + "\n" + ra + "\n" + rb + "\n" + rc + "\n" + rd); kinds != "" {
			blocked, reason = true, kinds
			ra, rb, rc, rd, ref = Redact(ra, hits), Redact(rb, hits), Redact(rc, hits), Redact(rd, hits), Redact(ref, hits)
			sev = Redact(sev, hits) // 重大度も本文に入るので伏せる
		}
	}
	strip := q.C.Flag("strip_raw_logs")
	field := func(s string, n int) string { return Clip(Lines(s, strip), n) }
	text := q.C.Finish(Compose(e.Type, ref, sev, field(ra, 120), field(rb, 200), field(rc, 200), field(rd, 120)))
	if !blocked && e.Type != "test" { // 整形後にも念のためもう一度確かめる
		if kinds, hits := Scan(text); kinds != "" {
			blocked, reason, text = true, kinds, Redact(text, hits)
		}
	}
	sinks := e.Sinks
	if sinks == "" {
		sinks = q.C.SinksOf(e.Type)
	}
	if blocked {
		var keep []string
		for _, s := range strings.Split(sinks, " ") {
			if s != "slack" {
				keep = append(keep, s)
			}
		}
		if sinks = strings.Join(keep, " "); sinks == "" {
			sinks = "file"
		}
	}
	slack := "none"
	if hasWord(sinks, "slack") {
		slack = "pending"
	}
	b := 0
	if blocked {
		b = 1
	}
	r, err := q.DB.Exec(`INSERT INTO main.notify_events (herdr_session, type, ref, severity, text, hash, blocked, blocked_reason, sinks, dedupe_key, slack_status, created_at)
    VALUES (?, ?, NULLIF(?,''), NULLIF(?,''), ?, ?, ?, NULLIF(?,''), ?, ?, ?, ?)`, q.Sess, e.Type, ref, sev, text, Hash(text), b, reason, sinks, key, slack, now())
	if err != nil {
		return 0, err
	}
	id, err := r.LastInsertId()
	if err != nil {
		return 0, err
	}
	q.localSinks(id)
	if blocked && e.Type != "warning" {
		bid := strconv.FormatInt(id, 10)
		// bash と同じく、呼び出し元の NOTIFY_FORCE / NOTIFY_SINKS はこの警告にも効く
		_, _ = q.Enqueue(Event{Type: "warning", Ref: "N-" + bid, Force: e.Force, Sinks: e.Sinks,
			A: "N-" + bid + "(" + e.Type + ")は秘密らしい値(" + reason + ")を含むため、外へ出していません。file には伏せて残しました"})
	}
	return id, nil
}

// Emit は notify_emit: どんな失敗も呼び出し元に返さない。
func (q *Q) Emit(e Event) { _, _ = q.Enqueue(e) }

func ensureDir() {
	if os.MkdirAll(Dir(), 0o755) == nil {
		_ = os.Chmod(Dir(), 0o700)
	}
}

func appendFile(path, s string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	_, err = f.WriteString(s)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// localSinks は notify_local_sinks: file と stdout だけ(外には出ない)。書けなくても止めない。
func (q *Q) localSinks(id int64) {
	var sinks, text, typ string
	var blocked int
	if q.DB.QueryRow(`SELECT sinks, text, type, blocked FROM notify_events WHERE id=?`, id).Scan(&sinks, &text, &typ, &blocked) != nil {
		return
	}
	mark := ""
	if blocked == 1 {
		mark = " [外へは出していない: 秘密を検出]"
	}
	ts := now()
	ensureDir()
	if hasWord(sinks, "file") {
		_ = appendFile(filepath.Join(Dir(), time.Now().Format("2006-01-02")+".md"), fmt.Sprintf("- %s N-%d [%s]%s %s\n", ts, id, typ, mark, text))
		_, _ = q.DB.Exec(`UPDATE main.notify_events SET file_at=? WHERE herdr_session=? AND id=?`, ts, q.Sess, id)
	}
	if hasWord(sinks, "stdout") {
		_ = appendFile(filepath.Join(Dir(), "stdout.log"), fmt.Sprintf("%s N-%d %s%s\n", ts, id, text, mark))
	}
}

func (q *Q) sentLog(id int64, via, hash, text string) {
	ensureDir()
	_ = appendFile(filepath.Join(Dir(), "sent.log"), fmt.Sprintf("%s\tN-%d\t%s\t%s\t%s\n", now(), id, via, hash, text))
}

func (q *Q) recordSend(id int64, via string) {
	_, _ = q.DB.Exec(`INSERT INTO main.notify_sends (herdr_session, event_id, via, at) VALUES (?, ?, ?, ?)`, q.Sess, id, via, now())
}

func (q *Q) exec(stmt string, args ...any) { _, _ = q.DB.Exec(stmt, args...) }

// Budget は notify_budget: 1 分・1 時間の残りの枠(小さいほう)。
func (q *Q) Budget() int {
	var um, uh int
	_ = q.DB.QueryRow(`SELECT COUNT(*) FROM notify_sends WHERE (julianday('now') - julianday(at)) * 86400 < 60`).Scan(&um)
	_ = q.DB.QueryRow(`SELECT COUNT(*) FROM notify_sends WHERE (julianday('now') - julianday(at)) * 86400 < 3600`).Scan(&uh)
	return min(q.C.Int("rate_per_min")-um, q.C.Int("rate_per_hour")-uh)
}

// fail は notify_fail。
func (q *Q) fail(id int64, why string) {
	q.exec(`UPDATE main.notify_events SET slack_status='failed', note=? WHERE herdr_session=? AND id=?`, why, q.Sess, id)
	n := strconv.FormatInt(id, 10)
	q.Emit(Event{Type: "warning", Ref: "N-" + n + "-failed", A: "N-" + n + " を slack へ送れませんでした(" + why + ")。情報は file と webapp に残っています"})
}

var reHTTP2xx = regexp.MustCompile(`^2[0-9][0-9]$`)

// webhookPost は notify_webhook_post: URL は 0600 のファイルから読み、引数・ログ・記録には出さない。失敗なら理由(URL を含めない)。
func (q *Q) webhookPost(text string) string {
	f := q.C.Get("webhook_file")
	if f == "" {
		return "webhook_file が無い(通常ファイルのみ)"
	}
	st, err := os.Lstat(f)
	if err != nil || !st.Mode().IsRegular() {
		return "webhook_file が無い(通常ファイルのみ)"
	}
	if st.Mode()&(os.ModePerm|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0o600 {
		return "webhook_file の権限が 0600 ではない"
	}
	b, _ := os.ReadFile(f)
	first, _, _ := strings.Cut(string(b), "\n")
	url := strings.NewReplacer("\r", "", "\n", "", " ", "").Replace(first)
	if !strings.HasPrefix(url, "https://") && !(os.Getenv("TB_NOTIFY_ALLOW_HTTP") != "" && strings.HasPrefix(url, "http://127.0.0.1")) {
		return "webhook の URL が https ではない"
	}
	tmp := os.Getenv("TMPDIR")
	if tmp == "" {
		tmp = "/tmp"
	}
	body, err := os.CreateTemp(tmp, "tb-notify.")
	if err != nil {
		return "webhook の POST に失敗(curl=1, http=なし)"
	}
	defer os.Remove(body.Name())
	cfg, err := os.CreateTemp(tmp, "tb-notify.")
	if err != nil {
		body.Close()
		return "webhook の POST に失敗(curl=1, http=なし)"
	}
	defer os.Remove(cfg.Name())
	_, _ = body.WriteString(`{"text":` + util.JQString(text) + "}\n")
	_, _ = cfg.WriteString(`url = "` + url + "\"\n")
	body.Close()
	cfg.Close()
	bin := os.Getenv("TB_CURL")
	if bin == "" {
		bin = "curl"
	}
	cmd := exec.Command(bin, "-sS", "--max-time", "10", "-o", "/dev/null", "-w", "%{http_code}", "-X", "POST",
		"-H", "Content-Type: application/json", "--data-binary", "@"+body.Name(), "-K", cfg.Name())
	var out bytes.Buffer
	cmd.Stdout = &out
	rc := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			rc = ee.ExitCode()
		} else {
			rc = 127
		}
	}
	code := strings.TrimRight(out.String(), "\n")
	if rc != 0 || !reHTTP2xx.MatchString(code) {
		if code == "" {
			code = "なし"
		}
		return fmt.Sprintf("webhook の POST に失敗(curl=%d, http=%s)", rc, code)
	}
	return ""
}

// SlackSend は notify_slack_send: 1 件を slack へ出す。0 送った / 10 宛先が使用中(未送信) / 1 失敗(記録済み)。
func (q *Q) SlackSend(id int64) int {
	var text, hash string
	var blocked, att int
	if q.DB.QueryRow(`SELECT text, hash, blocked, slack_attempts FROM notify_events WHERE id=?`, id).Scan(&text, &hash, &blocked, &att) != nil {
		return 1
	}
	if blocked != 0 { // 防御: blocked は外へ出さない
		return 1
	}
	retries := q.C.Int("ack_retries")
	if q.C.Get("slack_via") == "webhook" {
		if why := q.webhookPost(text); why == "" {
			q.exec(`UPDATE main.notify_events SET slack_status='sent', slack_attempts=slack_attempts+1, slack_sent_at=?, note=NULL WHERE herdr_session=? AND id=?`, now(), q.Sess, id)
			q.recordSend(id, "webhook")
			q.sentLog(id, "webhook", hash, text)
			return 0
		} else {
			q.exec(`UPDATE main.notify_events SET slack_attempts=slack_attempts+1, note=? WHERE herdr_session=? AND id=?`, why, q.Sess, id)
			if att+1 >= retries {
				q.fail(id, why)
			}
			return 1
		}
	}
	target, agent := q.C.Get("slack_target"), q.C.Get("notifier_agent")
	if target == "" {
		q.fail(id, "slack_target が未設定")
		return 1
	}
	n := strconv.FormatInt(id, 10)
	msg := "[from: tb / notify / N-" + n + " / info] POST channel=" + target + " hash=" + hash + " text=<<<" + text +
		">>> (本文をそのまま 1 通だけ投稿し、投稿した本文で tb notify hash を出して tb notify ack N-" + n + " <hash> を実行)"
	switch transport.Send(q.DB, q.Sess, agent, msg, "N-"+n, "notify", io.Discard) {
	case transport.Sent:
		q.exec(`UPDATE main.notify_events SET slack_status='awaiting-ack', slack_attempts=slack_attempts+1, slack_sent_at=?, note=NULL WHERE herdr_session=? AND id=?`, now(), q.Sess, id)
		q.recordSend(id, "agent")
		q.sentLog(id, "agent", hash, text)
		return 0
	case transport.Busy:
		return 10
	}
	q.exec(`UPDATE main.notify_events SET slack_attempts=slack_attempts+1, note='notifier に送れない' WHERE herdr_session=? AND id=?`, q.Sess, id)
	if att+1 >= retries {
		q.fail(id, "notifier に送れない")
	}
	return 1
}

func (q *Q) ids(query string, args ...any) []int64 {
	rows, err := q.DB.Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out
}

// makeDigest は notify_make_digest: 枠が足りないとき、残りを 1 通にまとめる。まとめの event id(作れなければ 0)。
func (q *Q) makeDigest(ids []int64) int64 {
	line := ""
	for _, i := range ids {
		var t string
		_ = q.DB.QueryRow(`SELECT text FROM notify_events WHERE id=?`, i).Scan(&t)
		if line != "" {
			line += " | "
		}
		line += fmt.Sprintf("N-%d %s", i, Clip(strings.TrimRight(t, "\n"), 50))
		if utf8.RuneCountInString(line) > q.C.Int("max_len")-100 {
			line += " ..."
			break
		}
	}
	did, _ := q.Enqueue(Event{Type: "digest", Ref: fmt.Sprintf("batch-%d-%d", time.Now().Unix(), os.Getpid()), Force: true,
		A: fmt.Sprintf("頻度の上限のため %d 件をまとめました: %s (全文: tb notify events)", len(ids), line)})
	if did == 0 {
		return 0
	}
	for _, i := range ids {
		q.exec(`UPDATE main.notify_events SET slack_status='batched', batch_id=? WHERE herdr_session=? AND id=?`, did, q.Sess, i)
	}
	return did
}

// FlushSlack は notify_flush_slack: 返事の無い送信の再送と、新しいものの送り出し(枠が足りなければ残りを 1 通に)。
func (q *Q) FlushSlack() {
	budget := q.Budget()
	timeout, retries := q.C.Int("ack_timeout_min"), q.C.Int("ack_retries")
	type ra struct{ id, att int64 }
	var resend []ra
	if rows, err := q.DB.Query(`SELECT id, slack_attempts FROM notify_events WHERE slack_status='awaiting-ack' AND blocked=0
           AND (julianday('now') - julianday(slack_sent_at)) * 1440 >= ? ORDER BY id`, timeout); err == nil {
		for rows.Next() {
			var r ra
			if rows.Scan(&r.id, &r.att) == nil {
				resend = append(resend, r)
			}
		}
		rows.Close()
	}
	for _, r := range resend {
		if r.att >= int64(retries) {
			q.fail(r.id, fmt.Sprintf("ack が来ない(%d 回送った)", r.att))
			continue
		}
		if budget <= 0 {
			continue
		}
		if q.SlackSend(r.id) == 0 {
			budget--
		}
	}
	ids := q.ids(`SELECT id FROM notify_events WHERE slack_status='pending' AND blocked=0 ORDER BY id`)
	if len(ids) == 0 || budget <= 0 {
		return
	}
	if len(ids) <= budget {
		for _, id := range ids {
			q.SlackSend(id)
		}
		return
	}
	single := budget - 1
	for _, id := range ids[:single] {
		q.SlackSend(id)
	}
	if did := q.makeDigest(ids[single:]); did != 0 {
		q.SlackSend(did)
	}
}

// SeenGet は notify_seen_get。
func (q *Q) SeenGet(key string) string {
	var v string
	_ = q.DB.QueryRow(`SELECT value FROM notify_seen WHERE key=?`, key).Scan(&v)
	return v
}

// SeenSet は notify_seen_set。
func (q *Q) SeenSet(key, value string) {
	q.exec(`INSERT INTO main.notify_seen (herdr_session, key, value, updated_at) VALUES (?, ?, ?, ?)
    ON CONFLICT(herdr_session, key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`, q.Sess, key, value, now())
}

func text(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case int64:
		return strconv.FormatInt(x, 10)
	case []byte:
		return string(x)
	case string:
		return x
	}
	return fmt.Sprint(v)
}

// Periodic は notify_periodic: 承認待ち(L-/O- の番号と件数だけ)と定期まとめ。
func (q *Q) Periodic() error {
	active := q.ActiveSQL
	if active == "" {
		active = "SELECT NULL WHERE 0"
	}
	var v [8]any
	err := q.DB.QueryRow(`SELECT
      trim(COALESCE((SELECT group_concat(x, ' ') FROM (SELECT 'O-'||id AS x FROM outbox WHERE status='pending' ORDER BY id)), '') || ' ' ||
           COALESCE((SELECT group_concat(x, ' ') FROM (SELECT 'L-'||id AS x FROM agent_ops WHERE status='pending' ORDER BY id)), '')),
      COALESCE((SELECT value FROM notify_seen WHERE key='approvals-last'), ''),
      COALESCE((SELECT CAST((julianday('now') - julianday(updated_at)) * 1440 AS INTEGER) FROM notify_seen WHERE key='summary-at'), ''),
      (SELECT COUNT(*) FROM agents WHERE state='running'),
      (SELECT COUNT(*) FROM alerts WHERE status NOT IN ('closed')),
      (SELECT COUNT(*) FROM comments WHERE kind IN ('question','blocked') AND resolved_at IS NULL),
      (SELECT COUNT(*) FROM tasks WHERE status IN (`+active+`)),
      COALESCE((SELECT value FROM notify_seen WHERE key='summary-last'), '')`).Scan(&v[0], &v[1], &v[2], &v[3], &v[4], &v[5], &v[6], &v[7])
	if err != nil {
		return err
	}
	ids, last, age, run, al, hu, tk, slast := text(v[0]), text(v[1]), text(v[2]), text(v[3]), text(v[4]), text(v[5]), text(v[6]), text(v[7])
	if ids != last {
		q.SeenSet("approvals-last", ids)
		if ids != "" {
			sum := sha256.Sum256([]byte(ids))
			q.Emit(Event{Type: "approvals_pending", Ref: hex.EncodeToString(sum[:])[:8], A: fmt.Sprintf("承認待ち %d 件: %s", len(strings.Fields(ids)), ids)})
		}
	}
	iv := q.C.Int("summary_interval_min")
	if iv <= 0 {
		return nil
	}
	if a, err := strconv.Atoi(age); age != "" && err == nil && a < iv {
		return nil
	}
	cur := "動作中 agent " + run + " / 進行中タスク " + tk + " / 未クローズのアラート " + al + " / 人の対応待ち " + hu
	q.SeenSet("summary-at", now())
	if cur != slast {
		q.SeenSet("summary-last", cur)
		q.Emit(Event{Type: "summary", Ref: strconv.FormatInt(time.Now().Unix(), 10), A: cur})
	}
	return nil
}
