package tbcli

import "io"

// tb agent <動詞> と tb recipe <動詞>(サービスの名前。bash の cmd_agent / cmd_recipe と同じ分け方)。
// Go に移し終えた動詞だけを持つ。bin/tb は herdrctl tb --commands の <サービス>.<動詞> を見て、その動詞だけを渡す。
// まだ bash の動詞(agent open / stop など)は bin/tb が bash に回すので、ここには来ない。

type cmdFunc func(args []string, w, stderr io.Writer) error

// agentVerbs は Go に移し終えた tb agent の動詞(2 語目。3 語目で分けるものは agentVerbs2)。
var agentVerbs = map[string]cmdFunc{
	"flag": cmdAgentFlag, "ops": cmdAgentOps, "kinds": cmdAgentKinds,
	"freeze": cmdSpawnFreeze, "unfreeze": cmdSpawnUnfreeze,
	"can": cmdCan, "org": cmdOrg, "peek": cmdPeek, "slash": cmdSlash,
}

var agentVerbs2 = map[string]map[string]cmdFunc{
	"compact": {"check": cmdCompactCheck},
	"role": {"show": cmdRoleShow, "list": cmdRoleList, "write": cmdRoleWrite, "edit": cmdRoleEdit, "unset": cmdRoleUnset,
		"set": cmdRoleSet, "log": cmdRoleLog, "broadcast": cmdRoleBroadcast, "status": cmdRoles},
}

var recipeVerbs = map[string]cmdFunc{
	"set": cmdRecipeSet, "rm": cmdRecipeRm, "list": cmdRecipeList, "show": cmdRecipeShow, "history": cmdRecipeHistory,
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
	return out
}

func runService(svc string, one map[string]cmdFunc, two map[string]map[string]cmdFunc, args []string, w, stderr io.Writer) error {
	if len(args) == 0 {
		return usage("%s: サブコマンドが必要です(tb %s で一覧)", svc, svc)
	}
	if f, ok := one[args[0]]; ok {
		return f(args[1:], w, stderr)
	}
	if m, ok := two[args[0]]; ok && len(args) > 1 {
		if f, ok := m[args[1]]; ok {
			return f(args[2:], w, stderr)
		}
	}
	// bin/tb はここに来る動詞だけを渡す。直に herdrctl tb を叩いたときだけ来る
	return usage("%s: Go ではまだ扱えないサブコマンドです(bash の tb で実行してください)", svc)
}

func cmdAgent(args []string, w, stderr io.Writer) error {
	return runService("agent", agentVerbs, agentVerbs2, args, w, stderr)
}

func cmdRecipe(args []string, w, stderr io.Writer) error {
	return runService("recipe", recipeVerbs, recipeVerbs2, args, w, stderr)
}
