package reminder

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// NudgeIdleMin / NudgeMax は TB_NUDGE_IDLE_MIN / TB_NUDGE_MAX の既定つきの値。
func NudgeIdleMin() string { return envOr("TB_NUDGE_IDLE_MIN", "15") }
func NudgeMax() string     { return envOr("TB_NUDGE_MAX", "3") }

// envOr は ${k:-d}(空なら既定)。
func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// NudgeRow は 1 件の結果(sent|would|skip|queued|held|blocked)。
type NudgeRow struct{ Agent, Task, Result, Reason string }

// Nudge は cmd_nudge の 1 回。
type Nudge struct {
	DB      *sql.DB
	Session string
	St      tickets.StatusSet
	Stderr  io.Writer
	Targets []string
	Pool    bool
	Dry     bool
	Idle    int
	Max     int

	idleS, maxS string
	roles       *agents.Roles
	judge       *approval.Judge
	labels      map[string]string
}

// ErrConfig は設定 nudge が壊れているとき(不正な行は Stderr に出した)。
type ErrConfig struct{}

func (ErrConfig) Error() string {
	return "nudge: 設定 nudge に不正な行があります(tb config edit nudge で直す)"
}

// ErrLive は herdr agent list に失敗したとき(何も送っていない)。
type ErrLive struct{}

func (ErrLive) Error() string {
	return "nudge: herdr agent list に失敗しました(何も送っていません)"
}

// SetLimits は --idle-min / --max の文字列(表示はそのまま、比べるのは数)。
func (n *Nudge) SetLimits(idle, max string) {
	n.idleS, n.maxS = idle, max
	n.Idle, _ = strconv.Atoi(idle)
	n.Max, _ = strconv.Atoi(max)
}

func (n *Nudge) quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func (n *Nudge) sqlList(names []string) string {
	var q []string
	for _, w := range names {
		q = append(q, n.quote(w))
	}
	if len(q) == 0 {
		return "SELECT NULL WHERE 0"
	}
	return strings.Join(q, ",")
}

// stMain は st_main(その分類で最初の状態)。
func (n *Nudge) stMain(cat string) string {
	if s := n.St.In(cat); len(s) > 0 {
		return s[0]
	}
	return ""
}

// systemLabel は system_label_of team <team>。
func (n *Nudge) systemLabel(use, team string) string {
	if n.labels == nil {
		n.labels = map[string]string{}
		for k, v := range tickets.SystemLabelBuiltin {
			n.labels[k] = v
		}
		if body, _, ok := config.Lookup(n.DB, n.Session, "system-labels"); ok {
			if tickets.ValidateSystemLabels(body) == "" {
				for _, line := range config.Lines(body) {
					line = config.Trim(config.StripComment(line))
					if k, v, ok := strings.Cut(line, "="); ok {
						n.labels[config.Trim(k)] = config.Trim(v)
					}
				}
			} else {
				fmt.Fprintln(n.Stderr, "tb: 設定 system-labels に不正な行があるので組み込みのラベルを使います(tb config edit system-labels で直す)")
			}
		}
	}
	l := n.labels[use]
	if use == "team" {
		l = strings.ReplaceAll(l, "{team}", team)
	}
	return l
}

func (n *Nudge) roleNames(a string) ([]string, bool) {
	s, ok := n.roles.NameOf(a)
	if !ok || s == "" {
		return nil, ok
	}
	return strings.Split(s, ","), true
}

// match は nudge_match: 0 当たる / 1 当たらない / 2 役割を確かめられない。
func (n *Nudge) match(a, pat string) int {
	if strings.HasPrefix(pat, "@") {
		have, ok := n.roleNames(a)
		if !ok {
			return 2
		}
		for _, r := range have {
			if util.Glob(pat[1:], strings.TrimSuffix(r, ".md")) {
				return 0
			}
		}
		return 1
	}
	if util.Glob(pat, a) {
		return 0
	}
	return 1
}

// teamSelf は nudge_team_self。
func (n *Nudge) teamSelf(a string) (string, bool) {
	have, _ := n.roleNames(a)
	for _, r := range have {
		r = strings.TrimSuffix(r, ".md")
		if strings.HasPrefix(r, "domain-") && len(r) > len("domain-") {
			return r[len("domain-"):], true
		}
	}
	if strings.HasSuffix(a, "-lead") && len(a) > len("-lead") {
		return strings.TrimSuffix(a, "-lead"), true
	}
	return "", false
}

// teamOf は nudge_team_of(本人、だめなら組織図の上司を上へ)。
func (n *Nudge) teamOf(a string) (string, bool) {
	if t, ok := n.teamSelf(a); ok {
		return t, true
	}
	chain, ok := n.judge.Chain(a)
	if !ok {
		return "", false
	}
	for _, m := range chain {
		if m == "" {
			continue
		}
		if t, ok := n.teamSelf(m); ok {
			return t, true
		}
	}
	return "", false
}

// protectReason は protect_reason(保護されていれば理由と true)。
func (n *Nudge) protectReason(a string) (string, bool) {
	body, _, ok := config.Lookup(n.DB, n.Session, "protect")
	if !ok {
		return "", false
	}
	var have []string
	loaded := false
	for _, line := range config.Lines(body) {
		line = config.Trim(config.StripComment(line))
		if line == "" {
			continue
		}
		k, v, has := strings.Cut(line, "=")
		k = config.Trim(k)
		if has {
			v = config.Trim(v)
		}
		why := v
		if why == "" {
			why = "protect: " + k
		}
		if strings.HasPrefix(k, "role:") {
			if !loaded {
				loaded = true
				have, _ = n.roleNames(a)
			}
			for _, r := range have {
				if util.Glob(k[len("role:"):], r) {
					return why, true
				}
			}
		} else if util.Glob(k, a) {
			return why, true
		}
	}
	return "", false
}

// msg は msg <識別子> <既定の文面> 名前=値 ...(kv は 名前, 値 の組の並び)。
func (n *Nudge) msg(code, def string, kv ...string) string {
	var ps []string
	for i := 0; i+1 < len(kv); i += 2 {
		ps = append(ps, kv[i]+"="+kv[i+1])
	}
	return config.Msg(n.DB, n.Session, code, def, ps...)
}

func (n *Nudge) multiText(task, others string) string {
	return n.msg("nudge.multi", "あなたは {active} のチケットをほかにも持っています({others})。1 枚ずつ進めるのが原則なので、まず {task} を進め、今やっていないものは tb update <ID> --status {todo} で戻すか、tb comment で指示役に相談してください。",
		"task", task, "others", others, "active", n.stMain("active"), "todo", n.stMain("todo"))
}

func (n *Nudge) text(task, title, minutes, others, status string) string {
	if status == "" {
		status = n.stMain("active")
	}
	t := n.msg("nudge.text", "[催促] {task}「{title}」が {status} のまま、turn が終わって {minutes} 分止まっています。続きを進めてください。終わったら tb done {task} -d <成果物> -m <要約> と tb report {task}、進められない理由があれば tb block {task} -m <理由> か tb comment {task} --kind question --text <聞きたいこと> で知らせてください。",
		"task", task, "title", title, "minutes", minutes, "status", status, "active", n.stMain("active"))
	if others != "" {
		t += n.multiText(task, others)
	}
	return t
}

func (n *Nudge) startText(task, title, minutes, others, status, agent, sess string) string {
	t := n.msg("nudge.start", "[催促] {task}「{title}」はあなたに割り当てられていますが、まだ着手されていません({status})。turn が終わって {minutes} 分たちました。始めるなら tb claim {task} --owner {agent} --session {session} で着手してください。始められない理由があれば tb comment {task} --kind question --text <聞きたいこと> で知らせてください。",
		"task", task, "title", title, "minutes", minutes, "status", status, "agent", agent, "session", sess)
	if others != "" {
		t += n.multiText(task, others)
	}
	return t
}

func (n *Nudge) poolText(task, title, agent, sess string) string {
	return n.msg("nudge.pool", "[取りに行って] 自分の team ラベルの担当なし {todo} のうち、優先度の高い順(highest→lowest、同順位は古い順)に 1 件 claim して進めてください。1 件ずつです。候補: {task}「{title}」。tb claim {task} --owner {agent} --session {session} で着手してください(先に取られていたら、tb list で次の候補を見てください)。",
		"task", task, "title", title, "agent", agent, "session", sess, "todo", n.stMain("todo"))
}

// wakeNames は nudge_load_config の NUDGE_WAKE_NAMES(分類は中の状態に広げ、知らない名前は落とす)。
func (n *Nudge) wakeNames(wake []string) []string {
	if len(wake) == 0 {
		wake = []string{"active"}
	}
	var names, out []string
	for _, w := range wake {
		if slices.Contains(tickets.Categories, w) {
			names = append(names, n.St.In(w)...)
		} else if n.St.Cat(w) != "" {
			names = append(names, w)
		}
	}
	for _, w := range names {
		if !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	return out
}

type nudgeTask struct {
	id, title, project, status string
	active                     bool
	blockers                   int
}

func (n *Nudge) str(q string, args ...any) string {
	var v sql.NullString
	_ = n.DB.QueryRow(q, args...).Scan(&v)
	return v.String
}

// Run は cmd_nudge(引数の検査と require_human は呼ぶ側)。
func (n *Nudge) Run() ([]NudgeRow, error) {
	n.roles = agents.NewRoles(n.DB, n.Session)
	n.judge = approval.NewJudge(n.DB, n.Session)
	live, err := herdr.Agents()
	if err != nil {
		return nil, ErrLive{}
	}
	var out []NudgeRow
	row := func(a, t, r, w string) { out = append(out, NudgeRow{a, t, r, w}) }
	var ignores, wake []string
	body, _, has := config.Lookup(n.DB, n.Session, "nudge")
	if has {
		var bad string
		ignores, wake, bad = message.ParseNudge(n.St, body)
		if bad != "" {
			fmt.Fprint(n.Stderr, bad)
			return nil, ErrConfig{}
		}
	}
	wakeNames := n.wakeNames(wake)
	wakeSQL := n.sqlList(wakeNames)
	closed := n.St.SQLList("done", "cancelled")
	// 移行: 設定 nudge がまだ無いあいだは、以前の既定の除外(指揮役・保護 protect)を保つ
	legacy := !has
	var conds []string
	if legacy {
		conds = message.ConductorNames(n.DB, n.Session)
	}
	owners := map[string]bool{}
	rs, err := n.DB.Query(`SELECT DISTINCT owner_agent FROM tasks WHERE status IN (` + wakeSQL + `) AND owner_agent IS NOT NULL AND owner_agent!=''`)
	if err != nil {
		return nil, err
	}
	for rs.Next() {
		var o string
		if rs.Scan(&o) == nil {
			owners[o] = true
		}
	}
	rs.Close()
	if n.Pool { // チケットを握っていない agent も候補にする(herdr にいるもの)
		for _, a := range live {
			owners[a.Name] = true
		}
	}
	var list []string
	for o := range owners {
		if o != "" {
			list = append(list, o)
		}
	}
	sort.Strings(list)
	hitT := make([]bool, len(n.Targets))
	var offered []string
	for _, owner := range list {
		if len(n.Targets) > 0 {
			hit := false
			for i, t := range n.Targets {
				if n.match(owner, t) == 0 {
					hit, hitT[i] = true, true
				}
			}
			if !hit {
				continue
			}
		} else {
			hit, pat := 0, ""
			for _, p := range ignores {
				rc := n.match(owner, p)
				if rc == 1 {
					continue
				}
				hit, pat = 1, p
				if rc == 2 {
					hit = 2
				}
				break
			}
			if hit == 2 {
				row(owner, "-", "skip", "役割を確かめられない(設定 nudge の ignore "+pat+")")
				continue
			}
			if hit != 0 {
				continue
			}
			if legacy {
				if slices.Contains(conds, owner) {
					row(owner, "-", "skip", "指揮役(設定 nudge が無いあいだの以前の除外。tb config edit nudge で置くと対象になる)")
					continue
				}
				if w, ok := n.protectReason(owner); ok {
					row(owner, "-", "skip", "保護("+w+"。設定 nudge が無いあいだの以前の除外)")
					continue
				}
			}
		}
		mode := "hold"
		var rows []nudgeTask
		trs, err := n.DB.Query(`SELECT t.id, t.title, COALESCE(t.project,''), t.status, t.status IN (`+n.St.SQLList("active")+`) AS active,
        (SELECT COUNT(*) FROM task_links l JOIN tasks b ON b.id=l.from_id WHERE l.to_id=t.id AND l.link_type='blocks' AND b.status NOT IN (`+closed+`)) AS blockers
      FROM tasks t WHERE t.status IN (`+wakeSQL+`) AND t.owner_agent=?
        AND t.type!='epic' AND NOT EXISTS (SELECT 1 FROM tasks c WHERE c.parent_id=t.id AND c.status NOT IN (`+closed+`))
      ORDER BY active DESC, `+tickets.PriorityRank+`, t.updated_at, t.id`, owner)
		if err != nil {
			return nil, err
		}
		for trs.Next() {
			var t nudgeTask
			var title sql.NullString
			if trs.Scan(&t.id, &title, &t.project, &t.status, &t.active, &t.blockers) == nil {
				t.title = title.String
				rows = append(rows, t)
			}
		}
		trs.Close()
		pick := ""
		for _, t := range rows {
			if t.blockers == 0 {
				pick = t.id
				break
			}
		}
		var othersL []string
		nact := 0
		for _, t := range rows {
			if t.active {
				nact++
				if t.id != pick {
					othersL = append(othersL, t.id)
				}
			}
		}
		others := strings.Join(othersL, " ")
		dash := pick
		if dash == "" {
			dash = "-"
		}
		var la *herdr.Agent
		for i := range live {
			if live[i].Name == owner {
				la = &live[i]
				break
			}
		}
		if la == nil {
			row(owner, dash, "skip", "herdr にいない")
			continue
		}
		st := ""
		if s, ok := la.AgentStatus.(string); ok {
			st = s
		} else if la.AgentStatus != nil && la.AgentStatus != false {
			st = fmt.Sprint(la.AgentStatus)
		}
		sess := la.AgentSession.Value
		if st != "idle" && st != "done" {
			if st == "" {
				st = "不明"
			}
			row(owner, dash, "skip", "herdr の状態が "+st)
			continue
		}
		if w := n.str(`SELECT group_concat('C-'||c.id, ' ') FROM comments c LEFT JOIN tasks t ON t.id=c.task_id
          WHERE c.from_name=? AND c.kind IN ('question','blocked') AND c.resolved_at IS NULL AND COALESCE(t.status,'') NOT IN (`+closed+`)`, owner); w != "" {
			row(owner, dash, "skip", "人の返事待ち("+w+")")
			continue
		}
		if pick == "" {
			if len(rows) > 0 {
				row(owner, rows[0].id, "skip", "先行チケット(blocks)待ち")
				continue
			}
			if !n.Pool {
				row(owner, "-", "skip", "握っているのは親(epic など)だけ")
				continue
			}
			// 自分が担当の todo・active・review・blocked があれば、新しい仕事を取りに行かせない(親は数えない)
			if w := n.str(`SELECT group_concat(t.id, ' ') FROM tasks t WHERE t.owner_agent=? AND t.status IN (`+n.St.SQLList("todo", "active", "review", "blocked")+`) AND t.type!='epic'
            AND NOT EXISTS (SELECT 1 FROM tasks c WHERE c.parent_id=t.id AND c.status NOT IN (`+closed+`))`, owner); w != "" {
				row(owner, "-", "skip", "担当のチケットがある("+w+"。--pool では取りに行かせない)")
				continue
			}
			team, ok := n.teamOf(owner)
			if !ok {
				row(owner, "-", "skip", "team を決められない(--pool)")
				continue
			}
			label := n.systemLabel("team", team)
			off := "[" + strings.Join(offered, ",") + "]"
			pick = n.str(`SELECT t.id FROM tasks t WHERE t.status IN (`+n.St.SQLList("todo")+`) AND (t.owner_agent IS NULL OR t.owner_agent='') AND t.type!='epic'
          AND t.id NOT IN (SELECT value FROM json_each(?))
          AND NOT EXISTS (SELECT 1 FROM alert_details a WHERE a.task_id=t.id) AND NOT EXISTS (SELECT 1 FROM proposal_details p WHERE p.task_id=t.id)
          AND EXISTS (SELECT 1 FROM json_each(t.labels) WHERE value=?)
          AND NOT EXISTS (SELECT 1 FROM tasks c WHERE c.parent_id=t.id AND c.status NOT IN (`+closed+`))
          AND NOT EXISTS (SELECT 1 FROM task_links l JOIN tasks b ON b.id=l.from_id WHERE l.to_id=t.id AND l.link_type='blocks' AND b.status NOT IN (`+closed+`))
          ORDER BY `+tickets.PriorityRank+`, t.created_at, t.id LIMIT 1`, off, label)
			if pick == "" {
				row(owner, "-", "skip", "担当なしの todo が無い("+label+")")
				continue
			}
			mode = "pool"
			var t nudgeTask
			var title, project sql.NullString
			_ = n.DB.QueryRow(`SELECT id, title, project, status FROM tasks WHERE id=?`, pick).Scan(&t.id, &title, &project, &t.status)
			t.title, t.project = title.String, project.String
			rows, nact, others = []nudgeTask{t}, 0, ""
		}
		var stopAt, promptAt, mins string
		_ = n.DB.QueryRow(`SELECT COALESCE(last_stop_at,''), COALESCE(last_prompt_at,''),
          COALESCE(CAST((julianday('now') - julianday(last_stop_at)) * 1440 AS INTEGER), '') FROM agent_activity WHERE session_id=?`, sess).Scan(&stopAt, &promptAt, &mins)
		if stopAt == "" {
			row(owner, pick, "skip", "活動記録が無い(hook)")
			continue
		}
		if promptAt != "" && n.str(`SELECT julianday(?) > julianday(?)`, promptAt, stopAt) == "1" {
			row(owner, pick, "skip", "turn の途中")
			continue
		}
		if m, _ := strconv.Atoi(mins); m < n.Idle {
			row(owner, pick, "skip", "休んで "+mins+" 分("+n.idleS+" 分から催促)")
			continue
		}
		if n.str(`SELECT 1 FROM outbox WHERE from_pane='nudge' AND target=? AND julianday(created_at) >= julianday(?) LIMIT 1`, owner, stopAt) != "" {
			row(owner, pick, "skip", "この休みで催促済み")
			continue
		}
		if c, _ := strconv.Atoi(n.str(`SELECT COUNT(*) FROM outbox WHERE from_pane='nudge' AND target=? AND task_id=? AND status='sent'`, owner, pick)); c >= n.Max {
			row(owner, pick, "skip", "催促 "+n.maxS+" 回で止めた(指揮役は tb digest の観測で見る)")
			continue
		}
		var cur nudgeTask
		for _, t := range rows {
			if t.id == pick {
				cur = t
				break
			}
		}
		sender := message.TargetFor(n.DB, n.Session, cur.project)
		// 送り手が本人と同じなら、既定の宛先から送る。それも本人なら送らない
		if sender == owner {
			sender = message.TargetFor(n.DB, n.Session, "*")
		}
		if sender == owner {
			row(owner, pick, "skip", "送り手が本人と同じ")
			continue
		}
		d := message.Mode(n.judge, sender, owner, "task", false)
		if d.Mode != "now" {
			row(owner, pick, "skip", "delivery が "+d.Mode+"("+d.Rule+")")
			continue
		}
		title := util.Oneline(cur.title, 80)
		var text string
		switch {
		case mode == "pool":
			offered = append(offered, util.JQString(pick)) // dry-run でも同じ選び方になるよう、ここで印を付ける
			text = n.poolText(pick, title, owner, sess)
		case n.St.Cat(cur.status) == "todo":
			text = n.startText(pick, title, mins, others, cur.status, owner, sess)
		default:
			text = n.text(pick, title, mins, others, cur.status)
		}
		w := ""
		if nact > 1 {
			w = fmt.Sprintf(" / %s %d 枚", n.stMain("active"), nact)
		}
		if n.Dry {
			row(owner, pick, "would", "休んで "+mins+" 分 / "+sender+" から"+w)
			continue
		}
		res, err := n.DB.Exec(`INSERT INTO main.outbox (herdr_session, from_name, from_pane, from_session, target, kind, task_id, text, status, reason, created_at)
      VALUES (?, ?, 'nudge', '', ?, 'task', ?, ?, 'queued', ?, ?)`, n.Session, sender, owner, pick, text,
			"nudge: 休んで "+mins+" 分"+w+" / "+d.Rule, Now())
		if err != nil {
			return nil, err
		}
		oid, _ := res.LastInsertId()
		r := message.Deliver(n.judge, oid, true, io.Discard, io.Discard)
		o := fmt.Sprintf("O-%d", oid)
		switch {
		case r.Code == 0 && r.Blocked != "":
			row(owner, pick, "blocked", o+"(届ける直前に delivery で止まった)")
		case r.Code == 0 && r.Held != "":
			row(owner, pick, "held", o+" → "+r.Held)
		case r.Code == 0:
			row(owner, pick, "sent", o+" / 休んで "+mins+" 分"+w)
		default:
			// 届かなかった催促は後で届けない(flush で遅れて届くと、もう動いている agent への古い催促になる)
			_, _ = n.DB.Exec(`UPDATE main.outbox SET status='denied', reason=reason||' / nudge: 届かず取り下げ(rc '||?||')' WHERE herdr_session=? AND id=? AND status='queued'`,
				r.Code, n.Session, oid)
			row(owner, pick, "skip", fmt.Sprintf("%s 届かず取り下げ(rc %d)", o, r.Code))
		}
	}
	// 明示した宛先で、どの agent にも当たらなかったものを黙って捨てない
	for i, t := range n.Targets {
		if !hitT[i] {
			row(t, "-", "skip", "宛先に当たる agent がいない("+orDefault(strings.Join(wakeNames, "、"), "(wake に当たる状態なし)")+" のチケットを握っていない・--pool なら herdr にいない)")
		}
	}
	return out, nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// Now は tb の now(UTC の秒まで)。
func Now() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }
