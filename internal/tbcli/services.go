package tbcli

import (
	"fmt"
	"io"
)

// tb agent <動詞> と tb recipe <動詞>(サービスの名前。bash の cmd_agent / cmd_recipe と同じ分け方)。
// 動詞はすべて Go に移した。bin/tb は tb agent / tb recipe をまるごと渡す(--commands の agent / recipe)。

type cmdFunc func(args []string, w, stderr io.Writer) error

// agentVerbs は Go に移し終えた tb agent の動詞(2 語目。3 語目で分けるものは agentVerbs2)。
var agentVerbs = map[string]cmdFunc{
	"flag": cmdAgentFlag, "stop": cmdAgentStop, "revive": cmdAgentRevive, "restart": cmdAgentRestart, "open": cmdAgentNew, "spawn": cmdAgentSpawn, "shed": cmdAgentShed, "list": cmdAgents, "approve": cmdAgentApprove, "reject": cmdAgentReject, "ops": cmdAgentOps, "kinds": cmdAgentKinds,
	"freeze": cmdSpawnFreeze, "unfreeze": cmdSpawnUnfreeze,
	"can": cmdCan, "org": cmdOrg, "peek": cmdPeek, "slash": cmdSlash,
}

var agentVerbs2 = map[string]map[string]cmdFunc{
	"compact": {"check": cmdCompactCheck},
	"model":   {"set": cmdModelSet},
	"role": {"show": cmdRoleShow, "list": cmdRoleList, "write": cmdRoleWrite, "edit": cmdRoleEdit, "unset": cmdRoleUnset,
		"set": cmdRoleSet, "log": cmdRoleLog, "broadcast": cmdRoleBroadcast, "status": cmdRoles},
}

var recipeVerbs = map[string]cmdFunc{
	"set": cmdRecipeSet, "rm": cmdRecipeRm, "list": cmdRecipeList, "show": cmdRecipeShow, "history": cmdRecipeHistory, "run": cmdRecipeRun, "runner": cmdRecipeRunner, "queue": cmdRecipeQueue, "cancel": cmdRecipeCancel,
}

var recipeVerbs2 = map[string]map[string]cmdFunc{
	"part": {"list": cmdParts, "check": cmdPartCheck, "risk": cmdPartRisk},
}

// serviceCommands は --commands に足す <サービス>.<動詞>(と <サービス>.<動詞>.<動詞>)。
func serviceCommands() []string {
	var out []string
	add := func(svc string, one map[string]cmdFunc, two map[string]map[string]cmdFunc) {
		for v := range one {
			out = append(out, svc+"."+v)
		}
		for v, m := range two {
			for v2 := range m {
				out = append(out, svc+"."+v+"."+v2)
			}
		}
	}
	add("agent", agentVerbs, agentVerbs2)
	add("recipe", recipeVerbs, recipeVerbs2)
	add("herdr", nil, herdrVerbs2)
	return out
}

// service は tb agent / tb recipe の分け方と使い方(bash の cmd_agent / cmd_recipe)。
type service struct {
	name  string
	one   map[string]cmdFunc
	two   map[string]map[string]cmdFunc
	help  string
	twoNG map[string]string // 3 語目が違うときの言い方(%s に 3 語目。空なら「(なし)」)
}

func runService(sv service, args []string, w, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(w, sv.help)
		return &exit{2, ""}
	}
	if f, ok := sv.one[args[0]]; ok {
		return f(args[1:], w, stderr)
	}
	if m, ok := sv.two[args[0]]; ok {
		v := ""
		if len(args) > 1 {
			v = args[1]
		}
		if f, ok := m[v]; ok {
			return f(args[2:], w, stderr)
		}
		if v == "" {
			v = "(なし)"
		}
		return usage(sv.twoNG[args[0]], v)
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(w, sv.help)
		return nil
	}
	return usage("%s: 不明なサブコマンド %s(tb %s で一覧)", sv.name, args[0], sv.name)
}

var agentService = service{name: "agent", one: agentVerbs, two: agentVerbs2, help: agentUsageText, twoNG: map[string]string{
	"model":   "agent model: サブコマンドは set だけです(tb agent model set ...)%.0s",
	"compact": "agent compact: サブコマンドは check だけです(tb agent compact check ...)%.0s",
	"role":    "agent role: 不明なサブコマンド %s(show / list / write / edit / unset / set / log / broadcast / status)",
}}

var recipeService = service{name: "recipe", one: recipeVerbs, two: recipeVerbs2, help: recipeUsageText, twoNG: map[string]string{
	"part": "recipe part: 不明なサブコマンド %s(list / check / risk)",
}}

func cmdAgent(args []string, w, stderr io.Writer) error {
	return runService(agentService, args, w, stderr)
}

func cmdRecipe(args []string, w, stderr io.Writer) error {
	return runService(recipeService, args, w, stderr)
}

const agentUsageText = `tb agent list [--json] [--all] [--group role] [--graph]   台帳と判定材料(古い名前 tb agents)
tb agent open <名前> [--cwd D] [--kind K] [--model M] [--role R] [--prompt P] [--killable yes|no]   作る(古い名前 tb agent-new)
tb agent stop|revive|restart <名前> ...   止める / 同じ会話で戻す / 止めて戻す(古い名前 tb agent-stop など)
tb agent flag <名前> --killable yes|no|unknown [--reason R]   止めてよいかの判定
tb agent ops [--status S] [--json] / tb agent approve|reject L-<n>   操作の記録と承認待ち
tb agent spawn <役割|-> --task T --brief B   規則つきの子 agent(古い名前 tb agent-spawn)
tb agent freeze|unfreeze   子 agent の自動承認を止める / 戻す(人だけ。古い名前 tb spawn-freeze / tb spawn-unfreeze)
tb agent shed / tb agent kinds   絞る候補 / kind の対応表
tb agent can|org|peek ...   できるか / 組織図 / 直近の出力
tb agent model set <名前|@役割|--all> <モデル>|--reset [--dry-run]   モデルを変える(古い名前 tb model-set)
tb agent compact check [--dry-run|--apply]   文脈の圧縮(古い名前 tb compact-check)
tb agent slash <コマンド> <agent 名|@役割>...|--all [--apply] / tb agent slash --list   スラッシュコマンドを送る
tb agent role show|list|write|edit|unset|set|log|broadcast|status ...   役割(古い名前 tb role-<動詞>、status は tb roles)
`

const recipeUsageText = `tb recipe set <名前> [--file F] / tb recipe rm <名前>   登録・置き換え / 消す(人だけ。古い名前 tb recipe-set など)
tb recipe list [--json] / tb recipe show <名前> / tb recipe history [<名前>] [--json]
tb recipe run <名前> [--target A] [--task T-..] [--dry-run] [<名前>=<値> ...]   実行の入口
tb recipe queue [--all] [--json] / tb recipe cancel R-<n> / tb recipe runner [--max N]   待ち行列と外の実行役
tb recipe part list [--json] / tb recipe part check <部品> [<名前>=<値> ...] / tb recipe part risk <部品> ...   部品(古い名前 tb parts / part-check / part-risk)
`
