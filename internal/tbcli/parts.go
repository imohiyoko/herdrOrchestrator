package tbcli

import (
	"fmt"
	"io"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/recipe"
)

// 部品(taskboard/lib/recipes.sh の cmd_parts / cmd_part-check / cmd_part-risk)。読むだけで DB は使わない。

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func cmdParts(args []string, w, stderr io.Writer) error {
	json := false
	for _, a := range args {
		if a != "--json" {
			return usage("parts: 不明な引数 %s", a)
		}
		json = true
	}
	for _, p := range recipe.Parts() {
		if json {
			ops, as := strings.Fields(p.Ops), strings.Fields(p.Args)
			if ops == nil {
				ops = []string{}
			}
			if as == nil {
				as = []string{}
			}
			fmt.Fprintln(w, jsonC("name", p.Name, "side", p.Side, "risk", p.Risk, "human_only", p.HumanOnly(), "runner", p.Runner,
				"ops", ops, "args", as, "desc", p.Desc))
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", p.Name, p.Side, p.Risk, yesNo(p.HumanOnly()), p.Runner,
			dashIfEmpty(p.Ops), dashIfEmpty(p.Args), p.Desc)
	}
	return nil
}

func cmdPartCheck(args []string, w, stderr io.Writer) error {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return usage("part-check <部品> [<名前>=<値> ...](部品の一覧は tb parts)")
	}
	p, ok := recipe.Lookup(args[0])
	if !ok {
		return die("部品の一覧にありません: %s(tb parts)", args[0])
	}
	val, why := recipe.CheckArgs(p, args[1:])
	if why != "" {
		return usage("part-check: %s", why)
	}
	fmt.Fprintf(w, "part: %s\nrisk: %s\nhuman_only: %s\nrunner: %s\n", p.Name, p.Risk, yesNo(p.HumanOnly()), p.Runner)
	ops, err := recipe.Ops(p, val)
	if err != nil {
		return die("%s", err.Error())
	}
	for _, o := range ops {
		fmt.Fprintf(w, "op: %s\n", o)
	}
	return nil
}

func cmdPartRisk(args []string, w, stderr io.Writer) error {
	if len(args) < 1 {
		return usage("part-risk <部品> ...(部品の並びのリスク階層 = 最大)")
	}
	for _, a := range args {
		if _, ok := recipe.Lookup(a); !ok {
			return die("部品の一覧にありません: %s(tb parts)", a)
		}
	}
	fmt.Fprintln(w, recipe.RiskMax(args))
	return nil
}
