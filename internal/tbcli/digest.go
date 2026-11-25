package tbcli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/capv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/transport"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
	"github.com/imohiyoko/herdrorchestrator/internal/proposal"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// まとめ送信(taskboard/lib/digest.sh の digest / digests)。scheduler から 10 分おきに呼ぶ想定。
// 未配送の報告・未報告の done・tb が観測した事象を、指揮官ごとに 1 通にまとめて送る。
// 観測は worker の自己申告に頼らない: turn が終わったのに in_progress のまま / 長時間 working / blocked(承認待ち)

// digestItem は add_item の 1 件。
type digestItem struct {
	target, typ, ref, key, task, okind, short, full string
}

type digestRun struct {
	db                *store.DB
	st                tickets.StatusSet
	items             []digestItem
	itemMax           int
	idleMin, stuckMin int
	queuedSec         int
	dt                string // project に属さない観測の宛先
}

func (d *digestRun) add(target, typ, ref, key, task, okind, label, text string) {
	d.items = append(d.items, digestItem{target, typ, ref, key, task, okind,
		"[" + label + "] " + itemText(text, d.itemMax), "[" + label + "] " + itemText(text, 2000)})
}

func (d *digestRun) seen(key string) bool {
	var one int
	return d.db.QueryRow(`SELECT 1 FROM observations WHERE key=?`, key).Scan(&one) == nil
}

func (d *digestRun) target(project string) string {
	return message.TargetFor(d.db.DB, d.db.Session, project)
}

// strRows は sql -separator の行(NULL は空文字)。読み終えてから返す(1 本の接続のため)。
func strRows(db *store.DB, q string, args ...any) [][]string {
	var out [][]string
	_ = qrows(db.DB, q, args, func(_ []string, vals []any) error {
		r := make([]string, len(vals))
		for i, v := range vals {
			r[i] = sqlText(v)
		}
		out = append(out, r)
		return nil
	})
	return out
}

// delsJoin は .deliverables | fromjson | join(", ")。
func delsJoin(v any) string {
	var a []any
	if s, ok := v.(string); ok {
		_ = json.Unmarshal([]byte(s), &a)
	}
	parts := make([]string, 0, len(a))
	for _, x := range a {
		if x == nil {
			parts = append(parts, "")
			continue
		}
		parts = append(parts, jv.Scalar(x))
	}
	return strings.Join(parts, ", ")
}

func sqlQuoteList(keys []string) string {
	var b strings.Builder
	for _, k := range keys {
		b.WriteString("'" + strings.ReplaceAll(k, "'", "''") + "',")
	}
	return b.String() + "''"
}

func (d *digestRun) collect() error {
	db, st := d.db, d.st
	// 1. 未配送の報告コメント(kind を問わない。即時送信に失敗したものもここで回収)
	rows, _ := sqlObjs(db.DB, nil, `SELECT c.*, t.project, ag.spawned_by FROM comments c LEFT JOIN tasks t ON t.id=c.task_id
             LEFT JOIN agents ag ON ag.name=c.from_name
             WHERE c.delivered_at IS NULL ORDER BY c.id`)
	for _, r := range rows {
		id, tid, kind := jqStr(r.Get("id")), jqStr(r.Get("task_id")), jqStr(r.Get("kind"))
		from, text, project := jqStr(r.Get("from_name")), jqStr(r.Get("text")), objStr(r, "project")
		if dels := delsJoin(r.Get("deliverables")); dels != "" {
			text += " / 成果物: " + dels
		}
		if sby := objStr(r, "spawned_by"); sby != "" {
			from += " (spawned_by: " + sby + ")"
		}
		d.add(d.target(project), "comment", "C-"+id, "", tid, "", tid+" "+kind+" / "+from, text)
	}
	// 2. done なのに未報告のタスク(未配送の done コメントがあるものは 1. で届く)
	rows, _ = sqlObjs(db.DB, nil, `SELECT * FROM tasks WHERE status IN (`+st.SQLList("done")+`) AND (reported_at IS NULL OR reported_at='')
             AND NOT EXISTS (SELECT 1 FROM comments c WHERE c.task_id=tasks.id AND c.kind='done' AND c.delivered_at IS NULL)
             ORDER BY updated_at, id`)
	for _, r := range rows {
		id := jqStr(r.Get("id"))
		owner := objStr(r, "owner_agent")
		if owner == "" && r.Get("owner_agent") == nil {
			owner = "-"
		}
		d.add(d.target(jqStr(r.Get("project"))), "task", id, "", id, "", id+" done / "+owner, objStr(r, "summary")+" / 成果物: "+delsJoin(r.Get("deliverables")))
	}
	// 2b. アラート: 新しく登録されたもの(まだまとめに入れていない)と、届いていない解析結果
	for _, r := range strRows(db, `SELECT id, status, replace(replace(title, char(10), ' '), char(9), ' ') FROM alerts WHERE status NOT IN ('closed') ORDER BY id`) {
		key := "alert:" + r[0]
		if d.seen(key) {
			continue
		}
		d.add(d.dt, "obs", "A-"+r[0], key, "", "alert", "A-"+r[0]+" アラート / "+r[1], r[2]+" (tb alerts A-"+r[0]+")")
	}
	for _, r := range strRows(db, `SELECT id, severity, replace(replace(title, char(10), ' '), char(9), ' '), replace(replace(result_summary, char(10), ' '), char(9), ' ')
             FROM alerts WHERE status='analyzed' AND result_delivered_at IS NULL ORDER BY id`) {
		d.add(d.dt, "alert", "A-"+r[0], "", "", "", "A-"+r[0]+" 解析結果 / "+r[1], r[2]+" — "+r[3]+" (全文: tb alerts A-"+r[0]+")")
	}
	// 2c. 通知: 秘密らしい値を含むため外へ出さなかったイベント(同じものは二度出さない)
	for _, r := range strRows(db, `SELECT id, COALESCE(blocked_reason,'') FROM notify_events WHERE blocked=1 AND type!='warning' ORDER BY id`) {
		key := "notify-blocked:" + r[0]
		if d.seen(key) {
			continue
		}
		d.add(d.dt, "obs", "N-"+r[0], key, "", "notifyblock", "N-"+r[0]+" 通知を止めた", "秘密らしい値("+r[1]+")を含むため外へ出していません(file には伏せて残した。tb notify events)")
	}
	// 2d. 通知の規則で届け先に conductor を書いたチケットの通知。そのプロジェクトの指揮役へ
	for _, r := range strRows(db, `SELECT n.id, e.task_id, COALESCE(t.project,''), replace(replace(n.text, char(10), ' '), char(9), ' ')
             FROM notify_events n JOIN main.task_events e ON e.herdr_session=? AND n.ref = 'E-' || e.id LEFT JOIN tasks t ON t.id=e.task_id
             WHERE n.type='ticket' AND n.blocked=0 AND (' ' || n.sinks || ' ') LIKE '% conductor %' ORDER BY n.id`, db.Session) {
		key := "notify-conductor:" + r[0]
		if d.seen(key) {
			continue
		}
		d.add(d.target(r[2]), "obs", "N-"+r[0], key, r[1], "rule", r[1]+" 通知の規則", strings.TrimPrefix(r[3], "[チケット] "))
	}
	// 3. 観測: turn が終わって idle_min 分たつのに in_progress のまま
	for _, r := range strRows(db, `SELECT t.id, t.project, COALESCE(t.owner_agent,'-'), 'idle:'||t.id||':'||a.last_stop_at,
             CAST((julianday('now') - julianday(a.last_stop_at)) * 1440 AS INTEGER)
             FROM tasks t JOIN main.agent_activity a ON a.session_id=t.session_id
             WHERE t.status IN (`+st.SQLList("active")+`) AND a.last_stop_at IS NOT NULL
               AND (a.last_prompt_at IS NULL OR a.last_prompt_at <= a.last_stop_at)
               AND (julianday('now') - julianday(a.last_stop_at)) * 1440 >= ? ORDER BY t.id`, d.idleMin) {
		if d.seen(r[3]) {
			continue
		}
		d.add(d.target(r[1]), "obs", r[0], r[3], r[0], "idle", r[0]+" 観測 / "+r[2],
			"turn が終わって "+r[4]+" 分たつが、タスクは "+st.Main("active")+" のまま("+st.Main("done")+"・報告なし)")
	}
	// 3b. 観測: 1 体の agent が active のチケットを複数握っている(epic と完了していない子を持つ親は数えない)
	var multi []string
	for _, r := range strRows(db, `SELECT owner_agent, group_concat(id, ','), MIN(project) FROM (SELECT t.owner_agent, t.id, t.project FROM tasks t
             WHERE t.status IN (`+st.SQLList("active")+`) AND t.owner_agent IS NOT NULL AND t.owner_agent!='' AND t.type!='epic'
               AND NOT EXISTS (SELECT 1 FROM tasks c WHERE c.parent_id=t.id AND c.status NOT IN (`+st.SQLList("done", "cancelled")+`)) ORDER BY t.id)
             GROUP BY owner_agent HAVING COUNT(*) > 1 ORDER BY owner_agent`) {
		owner, list, project := r[0], r[1], r[2]
		key := "multi:" + owner + ":" + list
		multi = append(multi, key)
		if d.seen(key) {
			continue
		}
		first, _, _ := strings.Cut(list, ",")
		n := 0
		for _, p := range strings.Split(list, ",") {
			if p != "" {
				n++
			}
		}
		d.add(d.target(project), "obs", first, key, first, "multi", owner+" 観測 / 複数チケット",
			fmt.Sprintf("%s のチケットを %d 枚握っている(%s)。1 枚に絞るか、ほかを %s に戻すか、別の agent に回す", st.Main("active"), n, strings.ReplaceAll(list, ",", " "), st.Main("todo")))
	}
	_, _ = db.Exec(`DELETE FROM main.observations WHERE herdr_session=? AND kind='multi' AND key NOT IN (`+sqlQuoteList(multi)+`)`, db.Session)
	// 4. 観測: stuck_min 分以上 working のまま
	for _, r := range strRows(db, `SELECT t.id, t.project, COALESCE(t.owner_agent,'-'), 'stuck:'||a.session_id||':'||a.last_prompt_at,
             CAST((julianday('now') - julianday(a.last_prompt_at)) * 1440 AS INTEGER)
             FROM tasks t JOIN main.agent_activity a ON a.session_id=t.session_id
             WHERE t.status IN (`+st.SQLList("active", "review", "blocked")+`) AND a.last_prompt_at IS NOT NULL
               AND (a.last_stop_at IS NULL OR a.last_prompt_at > a.last_stop_at)
               AND (julianday('now') - julianday(a.last_prompt_at)) * 1440 >= ? ORDER BY t.id`, d.stuckMin) {
		if d.seen(r[3]) {
			continue
		}
		d.add(d.target(r[1]), "obs", r[0], r[3], r[0], "stuck", r[0]+" 観測 / "+r[2], r[4]+" 分 working のまま(長い作業か停止かは区別できない)")
	}
	// 4b. 観測: outbox が queued_sec 秒以上 queued のまま(宛先に届いていない)
	for _, r := range strRows(db, `SELECT id, target, 'queued:'||id, CAST((julianday('now') - julianday(created_at)) * 1440 AS INTEGER)
             FROM outbox WHERE status='queued' AND (julianday('now') - julianday(created_at)) * 86400 >= ? ORDER BY id`, d.queuedSec) {
		if d.seen(r[2]) {
			continue
		}
		d.add(d.dt, "obs", "O-"+r[0], r[2], "", "queued", "O-"+r[0]+" 観測 / "+r[1],
			"O-"+r[0]+" が "+r[3]+" 分 queued のまま(宛先に届いていない。tb outbox --status queued で確認、tb flush で再送)")
	}
	// 5. 観測: herdr 上で blocked(承認ダイアログ待ちなど)の agent。担当タスクがあるものだけ
	if out, err := herdr.Call("agent", "list"); err == nil && agentsPresent(out) {
		live, _ := herdr.ParseAgents(out)
		clean := strings.NewReplacer("\x1f", " ", "\n", " ")
		var current []string
		for _, a := range live {
			if s, _ := a.AgentStatus.(string); s != "blocked" {
				continue
			}
			pane, name, sess := clean.Replace(a.PaneID), clean.Replace(a.Name), clean.Replace(a.AgentSession.Value)
			if pane == "" {
				continue
			}
			key := "blocked:" + pane + ":" + sess
			current = append(current, key)
			if d.seen(key) {
				continue
			}
			tr := strRows(db, `SELECT id, project FROM tasks WHERE status NOT IN (`+st.SQLList("done", "cancelled")+`)
               AND ((session_id IS NOT NULL AND session_id=?) OR (owner_agent IS NOT NULL AND owner_agent=?))
               ORDER BY updated_at DESC LIMIT 1`, sess, name)
			if len(tr) == 0 {
				continue
			}
			who := or(name, pane)
			d.add(d.target(tr[0][1]), "obs", tr[0][0], key, tr[0][0], "blocked", tr[0][0]+" 観測 / "+who,
				"blocked のまま(承認ダイアログ待ちの可能性。herdr agent attach "+who+" で確認)")
		}
		// blocked でなくなった agent の記録は消す(再び blocked になったら、また送る)
		_, _ = db.Exec(`DELETE FROM main.observations WHERE herdr_session=? AND kind='blocked' AND key NOT IN (`+sqlQuoteList(current)+`)`, db.Session)
	}
	d.collectSpawn()
	return d.collectSched()
}

// agentsPresent は jq -e '.result.agents'(null / false でない)。
func agentsPresent(out []byte) bool {
	m := firstJSONObj(out)
	r, _ := m["result"].(map[string]any)
	v, ok := r["agents"]
	return ok && v != nil && v != false
}

func firstJSONObj(out []byte) map[string]any {
	var v any
	if json.NewDecoder(strings.NewReader(string(out))).Decode(&v) != nil {
		return nil
	}
	m, _ := v.(map[string]any)
	return m
}

// collectSpawn は collect_spawn_obs: tb agent-spawn の子の上限の手前と、親のタスクが閉じたのに動いている子。
func (d *digestRun) collectSpawn() {
	db, st := d.db, d.st
	var one int
	if db.QueryRow(`SELECT 1 FROM agents WHERE spawned_by IS NOT NULL AND state='running' LIMIT 1`).Scan(&one) != nil {
		_, _ = db.Exec(`DELETE FROM main.observations WHERE herdr_session=? AND kind IN ('spawnlimit','spawnorphan')`, db.Session)
		return
	}
	_ = agents.Sync(db.DB, db.Session)
	p := loadSpawnPolicy(db)
	var current []string
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM agents WHERE spawned_by IS NOT NULL AND state='running'`).Scan(&n)
	if p.total > 0 && n >= p.total-1 {
		key := fmt.Sprintf("spawnlimit:total:%d", n)
		current = append(current, key)
		if !d.seen(key) {
			d.add(d.dt, "obs", "spawn", key, "", "spawnlimit", "spawn 観測 / 全体",
				fmt.Sprintf("子 agent が %d 体動いている(上限 %d。tb agents で確認、止めるのは tb agent-stop)", n, p.total))
		}
	}
	for _, r := range strRows(db, `SELECT spawned_by, COUNT(*) FROM agents WHERE spawned_by IS NOT NULL AND state='running' GROUP BY spawned_by ORDER BY spawned_by`) {
		parent, c := r[0], atoi(r[1])
		if parent == "" || !(p.per > 0 && c >= p.per-1) {
			continue
		}
		key := fmt.Sprintf("spawnlimit:%s:%d", parent, c)
		current = append(current, key)
		if !d.seen(key) {
			d.add(d.dt, "obs", "spawn", key, "", "spawnlimit", "spawn 観測 / "+parent,
				fmt.Sprintf("%s の子が %d 体動いている(1 体の親あたりの上限 %d)", parent, c, p.per))
		}
	}
	for _, r := range strRows(db, `SELECT a.name, a.spawned_by, a.spawn_task, t.status, t.project FROM agents a JOIN tasks t ON t.id=a.spawn_task
             WHERE a.spawned_by IS NOT NULL AND a.state='running' AND t.status IN (`+st.SQLList("done", "cancelled")+`,`+st.SQLList("blocked")+`) ORDER BY a.name`) {
		id, parent, task, s, project := r[0], r[1], r[2], r[3], r[4]
		if id == "" {
			continue
		}
		key := "spawnorphan:" + id + ":" + task + ":" + s
		current = append(current, key)
		if d.seen(key) {
			continue
		}
		d.add(d.target(project), "obs", task, key, task, "spawnorphan", task+" 観測 / "+parent,
			"親のタスクが "+s+" になったが、子 "+id+" はまだ動いている(止めるなら tb agent-stop "+id+")")
	}
	// 条件が外れたものは記録を消す(再び当てはまったら、また送る)
	_, _ = db.Exec(`DELETE FROM main.observations WHERE herdr_session=? AND kind IN ('spawnlimit','spawnorphan') AND key NOT IN (`+sqlQuoteList(current)+`)`, db.Session)
}

// buildDigest は build_digest: 長さの上限に収まる項目だけを入れ、残りは次回に回す。
func buildDigest(items []digestItem, did string, msgMax int) (int, string) {
	acc, n := "", 0
	tail := " (全文: tb digests D-" + did + ")"
	for _, it := range items {
		cand := fmt.Sprintf("(%d) %s", n+1, it.short)
		if acc != "" {
			cand = acc + " | " + cand
		}
		if n > 0 && utf8.RuneCountInString(cand)+utf8.RuneCountInString(tail)+80 > msgMax { // 80 = 見出し・「残り N 件」の分
			break
		}
		acc, n = cand, n+1
	}
	body := fmt.Sprintf("[digest D-%s / %d 件 / %s] %s", did, n, nowTS(), acc)
	if n < len(items) {
		body += fmt.Sprintf(" (残り %d 件は次回)", len(items)-n)
	}
	return n, body + tail
}

func digestEnvInt(k, def string) (string, bool) {
	v := envOr(k, def)
	return v, reDigitsOnly.MatchString(v)
}

// cmdDigest は tb digest [--dry-run]。
func cmdDigest(args []string, w, stderr io.Writer) error {
	dry := false
	for _, a := range args {
		if a != "--dry-run" {
			return usage("digest: 不明な引数 %s", a)
		}
		dry = true
	}
	idle, ok1 := digestEnvInt("TB_DIGEST_IDLE_MIN", "10")
	stuck, ok2 := digestEnvInt("TB_DIGEST_STUCK_MIN", "30")
	queued, ok3 := digestEnvInt("TB_DIGEST_QUEUED_SEC", "300")
	db, st, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if !ok1 || !ok2 || !ok3 {
		return usage("TB_DIGEST_IDLE_MIN / TB_DIGEST_STUCK_MIN / TB_DIGEST_QUEUED_SEC は数字")
	}
	if !dry {
		_ = flushOutbox(db, nil, io.Discard, io.Discard) // 積まれた下りの送信も届ける(結果は outbox / messages に残る)
		alertDispatchAll(db)                             // 承認・上限待ちのアラートを進める
		var ps []int64
		_ = qrows(db.DB, `SELECT id FROM proposals WHERE delivered_at IS NULL AND status='open' ORDER BY id`, nil, func(_ []string, v []any) error {
			if n, ok := v[0].(int64); ok {
				ps = append(ps, n)
			}
			return nil
		})
		for _, p := range ps { // 設定役に届いていない提案を再送
			_, _ = proposal.Deliver(db.DB, db.Session, p, io.Discard)
		}
	}
	d := &digestRun{db: db, st: st, itemMax: capv.Env("TB_DIGEST_ITEM_MAX", 160),
		idleMin: atoi(idle), stuckMin: atoi(stuck), queuedSec: atoi(queued),
		dt: message.TargetFor(db.DB, db.Session, "*")}
	if err := d.collect(); err != nil {
		return err
	}
	if len(d.items) == 0 {
		fmt.Fprintln(w, "digest: 送るものはありません")
		return nil
	}
	msgMax := capv.Env("TB_DIGEST_MSG_MAX", 1800) // unlimited で 1 通にまとめる
	var targets []string
	seenT := map[string]bool{}
	for _, it := range d.items {
		if !seenT[it.target] {
			seenT[it.target] = true
			targets = append(targets, it.target)
		}
	}
	rc := 0
	for _, t := range targets {
		var items []digestItem
		for _, it := range d.items {
			if it.target == t {
				items = append(items, it)
			}
		}
		if dry {
			_, body := buildDigest(items, "?", msgMax)
			fmt.Fprintf(w, "%s\t%s\n", t, body)
			continue
		}
		res, err := db.Exec(`INSERT INTO main.digests (herdr_session, target, item_count, body, created_at) VALUES (?, ?, 0, '', ?)`, db.Session, t, nowTS())
		if err != nil {
			return err
		}
		didN, _ := res.LastInsertId()
		did := strconv.FormatInt(didN, 10)
		n, body := buildDigest(items, did, msgMax)
		sel := items[:n]
		if e := transport.Send(db.DB, db.Session, t, body, "D-"+did, "digest", stderr); e != 0 {
			_, _ = db.Exec(`DELETE FROM main.digests WHERE herdr_session=? AND id=?`, db.Session, didN) // 送れなかった回は残さない。項目は次回に持ち越す
			rc = max(rc, e)
			continue
		}
		fulls := make([]string, len(sel))
		for i, it := range sel {
			fulls[i] = it.full
		}
		_, _ = db.Exec(`UPDATE main.digests SET item_count=?, body=? WHERE herdr_session=? AND id=?`, n, strings.Join(fulls, "\n"), db.Session, didN)
		ts := nowTS()
		for _, it := range sel {
			switch it.typ {
			case "comment":
				cid := strings.TrimPrefix(it.ref, "C-")
				_, _ = db.Exec(`UPDATE main.comments SET delivered_at=? WHERE herdr_session=? AND id=? AND delivered_at IS NULL`, ts, db.Session, cid)
				_, _ = db.Exec(`UPDATE main.tasks SET reported_at=?1, updated_at=?1
					WHERE herdr_session=?2 AND status IN (`+st.SQLList("done")+`) AND reported_at IS NULL
					AND id=(SELECT task_id FROM comments WHERE id=?3 AND kind='done')`, ts, db.Session, cid)
			case "task":
				_, _ = db.Exec(`UPDATE main.tasks SET reported_at=?, updated_at=? WHERE herdr_session=? AND id=? AND reported_at IS NULL`, ts, ts, db.Session, it.ref)
			case "alert":
				_, _ = db.Exec(`UPDATE main.alerts SET result_delivered_at=? WHERE herdr_session=? AND id=? AND result_delivered_at IS NULL`, ts, db.Session, strings.TrimPrefix(it.ref, "A-"))
			case "obs":
				_, _ = db.Exec(`INSERT OR IGNORE INTO main.observations (herdr_session, key, kind, task_id, text, digest_id, created_at)
					VALUES (?, ?, ?, ?, ?, ?, ?)`, db.Session, it.key, it.okind, it.ref, it.full, didN, ts)
			}
		}
		fmt.Fprintf(w, "D-%s sent to %s (%d 件)\n", did, t, n)
	}
	if rc != 0 {
		return &exit{rc, ""}
	}
	return nil
}

// cmdDigests は tb digests [D-<n>] [--json]。
func cmdDigests(args []string, w, stderr io.Writer) error {
	did := ""
	if len(args) > 0 && strings.HasPrefix(args[0], "D-") {
		did = args[0][2:]
		args = args[1:]
		if !reDigitsOnly.MatchString(did) {
			return usage("digest ID は D-<数字>")
		}
	}
	json := false
	for _, a := range args {
		if a != "--json" {
			return usage("digests: 不明な引数 %s", a)
		}
		json = true
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	withDid := func(o jv.Obj) jv.Obj { return o.Set("id", "D-"+jv.Scalar(o.Get("id"))) }
	if did != "" {
		rows, _ := sqlObjs(db.DB, nil, `SELECT * FROM digests WHERE id=?`, did)
		if len(rows) == 0 {
			return die("digest が見つかりません: D-%s", did)
		}
		o := rows[0]
		if json {
			fmt.Fprintln(w, jv.Compact(withDid(o)))
		} else {
			fmt.Fprintf(w, "D-%s → %s (%s 件, %s)\n%s\n", jqStr(o.Get("id")), jqStr(o.Get("target")), jqStr(o.Get("item_count")), jqStr(o.Get("created_at")), jqStr(o.Get("body")))
		}
		return nil
	}
	if json {
		rows, err := sqlObjs(db.DB, nil, `SELECT * FROM digests ORDER BY id DESC LIMIT 50`)
		if err != nil {
			return err
		}
		for _, o := range rows {
			fmt.Fprintln(w, jv.Compact(withDid(o)))
		}
		return nil
	}
	for _, r := range strRows(db, `SELECT 'D-'||id, target, item_count, created_at FROM digests ORDER BY id DESC LIMIT 50`) {
		fmt.Fprintln(w, strings.Join(r, "\t"))
	}
	return nil
}
