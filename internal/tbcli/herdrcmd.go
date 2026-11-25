package tbcli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/identity"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
)

// tb herdr <対象> <動詞>: herdr の workspace と pane を見る・閉じる(サービス herdr。herdr を呼ぶのは internal/kernel/herdr だけ)。
// 閉じるのは agent も、シェル以外のプロセスも動いていない workspace / pane だけ(--force は無い)。agent は tb agent stop(保護・killable・未完了タスクを確かめる)で止める。
// 権限は入口(commands.tbl の herdr.workspace.close / herdr.pane.close。agent への既定 allow。agent が居ないことはここで必ず確かめる)。

var herdrVerbs2 = map[string]map[string]cmdFunc{
	"workspace": {"list": cmdHerdrWorkspaceList, "close": cmdHerdrWorkspaceClose},
	"pane":      {"close": cmdHerdrPaneClose},
}

var herdrService = service{name: "herdr", two: herdrVerbs2, help: herdrUsageText, twoNG: map[string]string{
	"workspace": "herdr workspace: 不明なサブコマンド %s(list / close)",
	"pane":      "herdr pane: 不明なサブコマンド %s(close)",
}}

func cmdHerdr(args []string, w, stderr io.Writer) error {
	return runService(herdrService, args, w, stderr)
}

const herdrUsageText = `tb herdr workspace list [--json]   herdr の workspace と、その中の agent
tb herdr workspace close <workspace_id>   workspace を閉じる(どの pane でも agent・プロセスが動いていれば閉じない)
tb herdr pane close <pane_id>   pane を閉じる(agent・プロセスが動いていれば閉じない)
`

// herdrAgent は herdr agent list の 1 件(閉じる前の確かめに使う項目)。
type herdrAgent struct {
	name, pane, ws string
}

// herdrLive は herdr agent list を読んで台帳と同期する。読めなければ閉じない(fail-closed)。
func herdrLive(db *store.DB) ([]herdrAgent, error) {
	if _, err := agents.SyncLive(db.DB, db.Session); err != nil {
		return nil, &exit{11, "herdr agent list に失敗しました(agent が居るか確かめられないので閉じません)"}
	}
	out, err := herdr.Call("agent", "list")
	if err != nil {
		return nil, &exit{11, "herdr agent list に失敗しました(agent が居るか確かめられないので閉じません)"}
	}
	var r struct {
		Result struct {
			Agents []map[string]any `json:"agents"`
		} `json:"result"`
	}
	if json.Unmarshal(out, &r) != nil {
		return nil, &exit{11, "herdr agent list を読めません(agent が居るか確かめられないので閉じません)"}
	}
	var as []herdrAgent
	for _, a := range r.Result.Agents {
		s := func(k string) string { v, _ := a[k].(string); return v }
		as = append(as, herdrAgent{name: s("name"), pane: s("pane_id"), ws: s("workspace_id")})
	}
	return as, nil
}

// herdrWorkspaces は herdr workspace list の (id, label)。
func herdrWorkspaces() ([][2]string, error) {
	out, err := herdr.Call("workspace", "list")
	if err != nil {
		return nil, &exit{11, "herdr workspace list に失敗しました"}
	}
	var r struct {
		Result struct {
			Workspaces []map[string]any `json:"workspaces"`
		} `json:"result"`
	}
	if json.Unmarshal(out, &r) != nil || r.Result.Workspaces == nil {
		return nil, &exit{11, "herdr workspace list を読めません"}
	}
	var ws [][2]string
	for _, x := range r.Result.Workspaces {
		id, _ := x["workspace_id"].(string)
		l, _ := x["label"].(string)
		ws = append(ws, [2]string{id, l})
	}
	return ws, nil
}

func agentLabel(a herdrAgent) string {
	if a.name != "" {
		return a.name
	}
	return "(名前なし。pane " + a.pane + ")"
}

func cmdHerdrWorkspaceList(args []string, w, stderr io.Writer) error {
	asJSON := false
	for _, a := range args {
		if a != "--json" {
			return usage("herdr workspace list: 不明な引数 %s", a)
		}
		asJSON = true
	}
	ws, err := herdrWorkspaces()
	if err != nil {
		return err
	}
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	live, _ := herdrLive(db) // 一覧は agent が読めなくても出す(agents は空)
	for _, x := range ws {
		var names []string
		for _, a := range live {
			if a.ws != "" && a.ws == x[0] {
				names = append(names, agentLabel(a))
			}
		}
		if asJSON {
			arr := make([]string, len(names))
			for i, n := range names {
				arr[i] = jqString(n)
			}
			fmt.Fprintf(w, `{"workspace_id":%s,"label":%s,"agents":[%s]}`+"\n", jqString(x[0]), jqString(x[1]), strings.Join(arr, ","))
			continue
		}
		a := "-"
		if len(names) > 0 {
			a = strings.Join(names, ",")
		}
		fmt.Fprintf(w, "%s\t%s\tagents: %s\n", x[0], util.Oneline(x[1], 80), a)
	}
	return nil
}

// closeArgs は <id> を読む(--force は無い。agent が居る所は誰が実行しても閉じない)。
func closeArgs(what string, args []string) (string, error) {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return "", usage("herdr %s close <%s_id>", what, what)
	}
	id := args[0]
	if strings.ContainsAny(id, " \t\r\n") || len(id) > 128 {
		return "", usage("herdr %s close: id に使えない文字があります: %s", what, id)
	}
	return id, nil
}

// refuseAgents は、閉じる先に agent が 1 つでも居れば理由を返す(名前の有無に関わらず)。
func refuseAgents(where string, in []herdrAgent) error {
	if len(in) == 0 {
		return nil
	}
	var names []string
	for _, a := range in {
		names = append(names, agentLabel(a))
	}
	return &exit{1, where + " では agent " + strings.Join(names, ",") + " が動いているので閉じません。agent は tb agent stop <名前> で止めてから閉じます"}
}

func cmdHerdrPaneClose(args []string, w, stderr io.Writer) error {
	pane, err := closeArgs("pane", args)
	if err != nil {
		return err
	}
	if pane == os.Getenv("HERDR_PANE_ID") {
		return die("自分の pane(%s)は閉じません", pane)
	}
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	live, err := herdrLive(db)
	if err != nil {
		return err
	}
	var in []herdrAgent
	for _, a := range live {
		if a.pane == "" {
			// どの pane に居るか分からない agent が居ると、閉じる先に居ないと言い切れない
			return &exit{1, "herdr agent list に pane_id の無い agent(" + agentLabel(a) + ")が居るため、pane " + pane + " に agent が居ないと確かめられません。閉じません"}
		}
		if a.pane == pane {
			in = append(in, a)
		}
	}
	var reg string // 台帳で動作中の agent の pane(同期の後も running なら守る)
	_ = db.QueryRow(`SELECT COALESCE(group_concat(name, ','),'') FROM agents WHERE herdr_session=? AND pane_id=? AND state='running'`, db.Session, pane).Scan(&reg)
	if reg != "" && len(in) == 0 {
		in = append(in, herdrAgent{name: reg, pane: pane})
	}
	if err := refuseAgents("pane "+pane, in); err != nil {
		return err
	}
	if err := paneBusy(pane); err != nil {
		return err
	}
	if out, err := herdr.CallCombined("pane", "close", pane); err != nil {
		return &exit{11, "pane " + pane + " を閉じられません(" + util.Oneline(string(out), 200) + ")"}
	}
	fmt.Fprintf(w, "pane %s closed\n", pane)
	return nil
}

func cmdHerdrWorkspaceClose(args []string, w, stderr io.Writer) error {
	id, err := closeArgs("workspace", args)
	if err != nil {
		return err
	}
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	ws, err := herdrWorkspaces()
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(ws, func(x [2]string) bool { return x[0] == id }) {
		return die("workspace %s はありません(tb herdr workspace list で一覧)", id)
	}
	live, err := herdrLive(db)
	if err != nil {
		return err
	}
	self := os.Getenv("HERDR_PANE_ID")
	var in []herdrAgent
	for _, a := range live {
		if a.ws == "" {
			// どの workspace に居るか分からない agent が居ると、閉じる先に居ないと言い切れない
			return &exit{1, "herdr agent list に workspace_id の無い agent(" + agentLabel(a) + ")が居るため、workspace " + id + " に agent が居ないと確かめられません。閉じません"}
		}
		if a.ws == id {
			if self != "" && a.pane == self {
				return die("自分の pane(%s)がある workspace %s は閉じません", self, id)
			}
			in = append(in, a)
		}
	}
	if err := refuseAgents("workspace "+id, in); err != nil {
		return err
	}
	panes, err := workspacePanes(id)
	if err != nil {
		return err
	}
	for _, p := range panes {
		if self != "" && p == self {
			return die("自分の pane(%s)がある workspace %s は閉じません", self, id)
		}
		if err := paneBusy(p); err != nil {
			return err
		}
	}
	if out, err := herdr.CallCombined("workspace", "close", id); err != nil {
		return &exit{11, "workspace " + id + " を閉じられません(" + util.Oneline(string(out), 200) + ")"}
	}
	fmt.Fprintf(w, "workspace %s closed\n", id)
	return nil
}

// workspacePanes は herdr pane list --workspace の pane_id。読めなければ閉じない。
func workspacePanes(id string) ([]string, error) {
	out, err := herdr.Call("pane", "list", "--workspace", id)
	if err != nil {
		return nil, &exit{11, "herdr pane list に失敗しました(workspace " + id + " の pane で何か動いているか確かめられないので閉じません)"}
	}
	var r struct {
		Result struct {
			Panes []map[string]any `json:"panes"`
		} `json:"result"`
	}
	if json.Unmarshal(out, &r) != nil || r.Result.Panes == nil {
		return nil, &exit{11, "herdr pane list を読めません(workspace " + id + " の pane で何か動いているか確かめられないので閉じません)"}
	}
	var ps []string
	for _, p := range r.Result.Panes {
		pid, _ := p["pane_id"].(string)
		if pid == "" {
			return nil, &exit{11, "herdr pane list に pane_id の無い pane があります(確かめられないので閉じません)"}
		}
		if w, ok := p["workspace_id"].(string); ok && w != id {
			continue
		}
		ps = append(ps, pid)
	}
	return ps, nil
}

// paneBusy は、pane でシェル以外のものが動いていれば理由を返す(agent でなくても。ビルド・サーバ・エディタ・背景のジョブなど)。
// 見るのは herdr pane process-info の前面のプロセスと、ps で見たシェル(shell_pid)の子孫。どちらかを確かめられなければ閉じない。
func paneBusy(pane string) error {
	out, err := herdr.Call("pane", "process-info", "--pane", pane)
	if err != nil {
		return &exit{11, "herdr pane process-info に失敗しました(pane " + pane + " で何か動いているか確かめられないので閉じません)"}
	}
	var r struct {
		Result struct {
			ProcessInfo struct {
				ShellPID *int               `json:"shell_pid"`
				FG       *[]json.RawMessage `json:"foreground_processes"`
			} `json:"process_info"`
		} `json:"result"`
	}
	if json.Unmarshal(out, &r) != nil || r.Result.ProcessInfo.ShellPID == nil || r.Result.ProcessInfo.FG == nil {
		return &exit{1, "pane " + pane + " のプロセスの情報を読めません(何か動いているか確かめられないので閉じません)"}
	}
	shell := *r.Result.ProcessInfo.ShellPID
	var busy []string
	for _, raw := range *r.Result.ProcessInfo.FG {
		pid, name := fgProc(raw)
		if pid != 0 && pid == shell {
			continue // 前面がシェル自身 = 何も動いていない
		}
		busy = append(busy, name)
	}
	kids, ok := identity.Descendants(shell)
	if !ok {
		return &exit{1, "プロセスの一覧(ps)を読めないため、pane " + pane + " のシェルの下で何か動いているか確かめられません。閉じません"}
	}
	sort.Strings(kids)
	busy = append(busy, kids...)
	if len(busy) > 0 {
		return &exit{1, "pane " + pane + " ではプロセスが動いているので閉じません(" + util.Oneline(strings.Join(busy, ", "), 200) + ")。終わらせてから閉じます"}
	}
	return nil
}

// fgProc は foreground_processes の 1 件(数か、pid と name / command を持つ形)から pid と表示名。
func fgProc(raw json.RawMessage) (int, string) {
	var n int
	if json.Unmarshal(raw, &n) == nil {
		return n, strconv.Itoa(n)
	}
	var o map[string]any
	if json.Unmarshal(raw, &o) != nil {
		return 0, util.Oneline(string(raw), 60)
	}
	pid := 0
	if f, ok := o["pid"].(float64); ok {
		pid = int(f)
	}
	name := ""
	for _, k := range []string{"name", "command", "comm", "cmdline"} {
		if v, ok := o[k].(string); ok && v != "" {
			name = v
			break
		}
	}
	return pid, strings.TrimSpace(strconv.Itoa(pid) + " " + name)
}
