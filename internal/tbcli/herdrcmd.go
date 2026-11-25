package tbcli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
)

// tb herdr <対象> <動詞>: herdr の workspace と pane を見る・閉じる(サービス herdr。herdr を呼ぶのは internal/kernel/herdr だけ)。
// 閉じるのは agent の居ない workspace / pane だけ。agent は tb agent stop(保護・killable・未完了タスクを確かめる)で止める。
// 権限は入口(commands.tbl の herdr.workspace.close / herdr.pane.close。agent への既定 ask = 承認待ち Q-<n>)。

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
tb herdr workspace close <workspace_id> [--force]   workspace を閉じる(agent が居れば閉じない)
tb herdr pane close <pane_id> [--force]   pane を閉じる(agent が居れば閉じない)
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

// closeArgs は <id> [--force] を読む。--force は人だけ(名前の無い agent が居ても閉じる。名前のある agent は --force でも閉じない)。
func closeArgs(what string, args []string) (id string, force bool, err error) {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return "", false, usage("herdr %s close <%s_id> [--force]", what, what)
	}
	id = args[0]
	if strings.ContainsAny(id, " \t\r\n") || len(id) > 128 {
		return "", false, usage("herdr %s close: id に使えない文字があります: %s", what, id)
	}
	for _, a := range args[1:] {
		if a != "--force" {
			return "", false, usage("herdr %s close: 不明な引数 %s", what, a)
		}
		force = true
	}
	return id, force, nil
}

// refuseAgents は、閉じる先に agent が居るなら理由を返す。名前のある agent は常に止める(tb agent stop へ)。
// 名前の無い agent(tb の台帳に無い)は --force のときだけ通す。
func refuseAgents(where string, in []herdrAgent, force bool) error {
	var named, unnamed []string
	for _, a := range in {
		if a.name != "" {
			named = append(named, a.name)
		} else {
			unnamed = append(unnamed, a.pane)
		}
	}
	if len(named) > 0 {
		return &exit{1, where + " には agent " + strings.Join(named, ",") + " が居るので閉じません。agent は tb agent stop <名前> で止めます(保護・killable・未完了タスクを確かめる)"}
	}
	if len(unnamed) > 0 && !force {
		return &exit{1, where + " には名前の無い agent(pane " + strings.Join(unnamed, ",") + ")が居るので閉じません。閉じるなら人が --force を付けます"}
	}
	return nil
}

func closePrep(force bool) (*store.DB, error) {
	by, err := requester()
	if err != nil {
		return nil, err
	}
	if force && by != "human" {
		return nil, die("--force は人が自分の端末で使います(agent '%s' の pane からは使えません)", by)
	}
	return openDB()
}

func cmdHerdrPaneClose(args []string, w, stderr io.Writer) error {
	pane, force, err := closeArgs("pane", args)
	if err != nil {
		return err
	}
	if pane == os.Getenv("HERDR_PANE_ID") {
		return die("自分の pane(%s)は閉じません", pane)
	}
	db, err := closePrep(force)
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
		if a.pane == pane {
			in = append(in, a)
		}
	}
	var reg string // 台帳で動作中の agent の pane(herdr の一覧に出ていなくても守る)
	_ = db.QueryRow(`SELECT COALESCE(group_concat(name, ','),'') FROM agents WHERE herdr_session=? AND pane_id=? AND state='running'`, db.Session, pane).Scan(&reg)
	if reg != "" && !slices.ContainsFunc(in, func(a herdrAgent) bool { return a.name != "" }) {
		in = append(in, herdrAgent{name: reg, pane: pane})
	}
	if err := refuseAgents("pane "+pane, in, force); err != nil {
		return err
	}
	if out, err := herdr.CallCombined("pane", "close", pane); err != nil {
		return &exit{11, "pane " + pane + " を閉じられません(" + util.Oneline(string(out), 200) + ")"}
	}
	fmt.Fprintf(w, "pane %s closed\n", pane)
	return nil
}

func cmdHerdrWorkspaceClose(args []string, w, stderr io.Writer) error {
	id, force, err := closeArgs("workspace", args)
	if err != nil {
		return err
	}
	db, err := closePrep(force)
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
		if a.ws == "" && (a.name != "" || !force) {
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
	if err := refuseAgents("workspace "+id, in, force); err != nil {
		return err
	}
	if out, err := herdr.CallCombined("workspace", "close", id); err != nil {
		return &exit{11, "workspace " + id + " を閉じられません(" + util.Oneline(string(out), 200) + ")"}
	}
	fmt.Fprintf(w, "workspace %s closed\n", id)
	return nil
}
