package tbcli

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
)

// モデルの切り替え(taskboard/lib/models.sh の cmd_model-set / model_set_precheck / model_set_run / do_model_set)。
// 承認待ち(op=revive、args.model_set=1)の承認は、まだ bash の tb agent-approve が実行する。

const modelCacheNote = "注意: モデルを変えると、その会話のプロンプトキャッシュは使えなくなります(切り替えた直後の 1 回は、会話の全体を読み直す分の費用がかかります)"

var reModelSel = regexp.MustCompile(`^[A-Za-z0-9*?._+-]{1,64}$`)

func liveStatus(a herdr.Agent) string {
	s, _ := a.AgentStatus.(string)
	return s
}

// modelSetPrecheck は model_set_precheck: 承認の前に分かる結果。0 なら計画、3(skipped)か 12(not supported)なら理由。
func modelSetPrecheck(db *store.DB, live []herdr.Agent, name, model string, reset bool) (int, string) {
	reg := agentRegRow(db, name)
	a, isLive := liveOf(live, name)
	if !reg.ok && !isLive {
		if msg, other := otherSession(db, name); other {
			return 3, "skipped (" + msg + ")"
		}
		return 3, "skipped (台帳にありません)"
	}
	kind := reg.kind
	if !reg.ok {
		kind = kindDefault
	}
	old := reg.model
	if reset {
		if old == "" {
			return 3, "skipped (台帳にモデルがありません)"
		}
		return 0, "reset (" + old + " -> 役割の既定 " + roleModelFor(db, name, "") + ")"
	}
	if kind != "claude" {
		return 12, "not supported (kind=" + kind + " はモデルの切り替えに対応していません)"
	}
	if !modelRE.MatchString(model) {
		return 12, "not supported (kind=" + kind + " のモデル名として使えません: " + model + ")"
	}
	if old == model {
		return 3, "skipped (すでに " + model + ")"
	}
	if isLive {
		if st := liveStatus(a); st != "idle" && st != "done" {
			return 3, "skipped (" + or(st, "状態不明") + "。idle か done のときだけ切り替えます)"
		}
		return 0, "change (" + or(old, "既定") + " -> " + model + "、止めて同じ会話で再開)"
	}
	return 0, "change (" + or(old, "既定") + " -> " + model + "、停止中なので台帳だけ)"
}

// doModelSet は do_model_set: 1 体を切り替える。0=changed 3=skipped 11=herdr の失敗 12=not supported
func doModelSet(db *store.DB, name, model, by string, stderr io.Writer) opResult {
	live, err := agents.SyncLive(db.DB, db.Session) // 承認後の実行でも、今の状態で判断する
	if err != nil {
		return opResult{code: 11, out: "herdr agent list に失敗しました"}
	}
	reg := agentRegRow(db, name)
	a, isLive := liveOf(live, name)
	if !reg.ok && !isLive {
		if msg, other := otherSession(db, name); other {
			return opResult{code: 3, out: msg}
		}
		return opResult{code: 3, out: name + " は台帳にありません"}
	}
	kind := reg.kind
	if !reg.ok {
		kind = kindDefault
	}
	if kind != "claude" {
		return opResult{code: 12, out: "not supported (kind=" + kind + " はモデルの切り替えに対応していません)"}
	}
	if !modelRE.MatchString(model) {
		return opResult{code: 12, out: "not supported (kind=" + kind + " のモデル名として使えません: " + model + ")"}
	}
	old := reg.model
	if !isLive { // 止まっている: 台帳の値だけを変える(次の tb agent-revive から効く)
		_, _ = db.Exec(`UPDATE main.agents SET model=?, updated_at=? WHERE herdr_session=? AND name=?`, model, nowTS(), db.Session, name)
		return opResult{code: 0, out: "changed (" + or(old, "既定") + " -> " + model + "。停止中のため、次の tb agent-revive " + name + " から効きます)"}
	}
	if st := liveStatus(a); st != "idle" && st != "done" {
		return opResult{code: 3, out: "skipped (" + or(st, "状態不明") + "。idle か done のときだけ切り替えます)"}
	}
	if old == model {
		return opResult{code: 3, out: "skipped (すでに " + model + ")"}
	}
	pane, sess := a.PaneID, a.AgentSession.Value
	if sess == "" {
		sess = reg.sess
	}
	switch {
	case pane == "":
		return opResult{code: 3, out: "skipped (pane が分かりません)"}
	case sess == "":
		return opResult{code: 3, out: "skipped (session id が台帳にありません。同じ会話に戻れないので止めません)"}
	case reg.cwd == "" || !isDir(reg.cwd):
		return opResult{code: 3, out: "skipped (作業ディレクトリがありません: " + or(reg.cwd, "未記録") + "。同じ会話に戻れないので止めません)"}
	}
	if r := closePane(db, name, pane, sess); r != nil {
		return *r
	}
	_, _ = agents.SyncLive(db.DB, db.Session)
	if rev := doAgentRevive(db, name, false, model, "", by, stderr); rev.code != 0 {
		return opResult{code: 11, out: name + " を停止しましたが、" + model + " での復活に失敗: " + rev.out + "。停止のままです。戻すなら: tb agent-revive " + name + " --model " + model}
	}
	return opResult{code: 0, out: "changed (" + or(old, "既定") + " -> " + model + "、同じ会話で再開)"}
}

// modelSetRun は model_set_run: 承認後にも使う実行の本体。
func modelSetRun(db *store.DB, name, args, by string, stderr io.Writer) opResult {
	var a struct {
		Reset int    `json:"reset"`
		Model string `json:"model"`
	}
	_ = json.Unmarshal([]byte(args), &a)
	if a.Reset == 1 {
		_, _ = db.Exec(`UPDATE main.agents SET model=NULL, updated_at=? WHERE herdr_session=? AND name=?`, nowTS(), db.Session, name)
		d := roleModelFor(db, name, "")
		use := "agent の CLI の既定"
		if d != "" {
			use = "役割の既定 " + d
		}
		return opResult{code: 0, out: "reset (台帳のモデルを消しました。次の起動・復活から " + use + " を使います)"}
	}
	return doModelSet(db, name, a.Model, by, stderr)
}

func cmdModelSet(args []string, w, stderr io.Writer) error {
	u := "model-set <名前 | @役割 | --all> <モデル> [--dry-run] | model-set <名前 | @役割 | --all> --reset [--dry-run]"
	if len(args) < 2 {
		return usage("%s", u)
	}
	sel, model, reset, dry := args[0], "", false, false
	for _, a := range args[1:] {
		switch {
		case a == "--reset":
			reset = true
		case a == "--dry-run":
			dry = true
		case strings.HasPrefix(a, "-"):
			return usage("model-set: 不明な引数 %s", a)
		default:
			if model != "" {
				return usage("%s", u)
			}
			model = a
		}
	}
	if reset && model != "" {
		return usage("model-set: --reset とモデル名は同時に指定できません")
	}
	if !reset && model == "" {
		return usage("%s", u)
	}
	if model != "" && !modelRE.MatchString(model) {
		return usage("model-set: モデル名に使えない文字があります: %s", model)
	}
	all := false
	switch {
	case sel == "--all":
		all = true
	case strings.HasPrefix(sel, "@"):
		if !reModelSel.MatchString(sel[1:]) {
			return usage("model-set: @<役割> に使えない文字があります: %s", sel)
		}
	default:
		if err := validAgentName(sel); err != nil {
			return err
		}
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	by, err := requester()
	if err != nil {
		return err
	}
	live, err := agents.SyncLive(db.DB, db.Session)
	if err != nil {
		return &exit{11, "herdr agent list に失敗しました"}
	}
	// 対象を集める(このセッションの台帳と、今動いているもの)
	var names []string
	_ = qrows(db.DB, `SELECT name FROM agents`, nil, func(_ []string, v []any) error {
		names = append(names, fmt.Sprint(v[0]))
		return nil
	})
	for _, a := range live {
		names = append(names, a.Name)
	}
	names = slices.DeleteFunc(names, func(n string) bool { return !agents.NameRE.MatchString(n) })
	sort.Strings(names)
	names = slices.Compact(names)
	var targets []string
	switch {
	case strings.HasPrefix(sel, "@"):
		roles := agents.NewRoles(db.DB, db.Session)
		for _, n := range names {
			switch roles.Match(n, sel) {
			case 0:
				targets = append(targets, n)
			case 2:
				return &exit{11, "台帳を読めず、" + n + " の役割を確かめられません"}
			}
		}
	case all:
		targets = names
	default:
		targets = []string{sel}
	}
	if len(targets) == 0 {
		return die("対象の agent がいません: %s", sel)
	}
	conductors := message.ConductorNames(db.DB, db.Session)
	changed, skipped, unsup, failed, pending := 0, 0, 0, 0, 0
	for _, n := range targets {
		if n == by {
			fmt.Fprintln(w, n+" skipped (自分自身は切り替えません)")
			skipped++
			continue
		}
		if slices.Contains(conductors, n) {
			fmt.Fprintln(w, n+" skipped (指揮官は切り替えません。人が tb agent-revive "+n+" --model M で行う)")
			skipped++
			continue
		}
		kv := []any{"model_set", 1}
		if reset {
			kv = append(kv, "reset", 1)
		} else {
			kv = append(kv, "model", model)
		}
		if all { // --all の承認待ちは人だけが決める(tb agent-approve)
			kv = append(kv, "all", 1)
		}
		opArgs := jsonC(kv...)
		// 先に、承認に回す前に分かる「対応していない」「今は切り替えない」を見る(--dry-run の計画もここで出す)
		r, line := modelSetPrecheck(db, live, n, model, reset)
		if r != 0 {
			fmt.Fprintln(w, n+" "+line)
			if r == 12 {
				unsup++
			} else {
				skipped++
			}
			continue
		}
		// --all は承認待ちにだけ積む(自動では実行しない)。agent の pane からは agent:model:<名前> が allow のときだけ実行
		hold, why := false, ""
		if all {
			hold, why = true, "--all は人の承認が要ります"
		} else if by != "human" {
			v, rule := approval.NewJudge(db.DB, db.Session).Of("agent:model:"+n, by)
			if v == "deny" {
				fmt.Fprintln(w, n+" denied(認可: "+rule+")")
				skipped++
				continue
			}
			if v != "allow" {
				hold, why = true, "認可: "+rule
			}
		}
		if dry {
			if hold {
				fmt.Fprintln(w, n+" would hold for approval ("+line+" / "+why+")")
			} else {
				fmt.Fprintln(w, n+" would "+line)
			}
			continue
		}
		if hold {
			res, err := db.Exec(`INSERT INTO main.agent_ops (herdr_session, op, target, args, requested_by, status, result, created_at) VALUES (?, 'revive', ?, ?, ?, 'pending', ?, ?)`,
				db.Session, n, opArgs, by, why, nowTS())
			if err != nil {
				return err
			}
			lid, _ := res.LastInsertId()
			q := pendingQ(db.DB, "agent_ops", lid)
			fmt.Fprintf(w, "%s pending L-%d %s (人の承認待ち: tb approval approve %s)\n", n, lid, q, q)
			pending++
			continue
		}
		res := modelSetRun(db, n, opArgs, by, stderr)
		recordOp(db, "revive", n, opArgs, by, res.code, "model-set: "+res.out)
		fmt.Fprintln(w, n+" "+res.out)
		switch res.code {
		case 0:
			changed++
		case 3:
			skipped++
		case 12:
			unsup++
		default:
			failed++
		}
	}
	if dry {
		fmt.Fprintln(w, "(dry-run: 何も変えていません)")
	}
	if changed > 0 && !reset {
		fmt.Fprintln(w, modelCacheNote)
	}
	switch {
	case failed > 0:
		return &exit{11, ""}
	case pending > 0:
		return &exit{20, ""}
	case unsup > 0:
		return &exit{12, ""}
	case changed == 0 && skipped > 0 && len(targets) == 1:
		return &exit{1, ""}
	}
	return nil
}
