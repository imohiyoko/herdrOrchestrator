package tbcli

import (
	"fmt"
	"io"
	"os"
	"strings"

	apprecipe "github.com/imohiyoko/herdrorchestrator/internal/app/recipe"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/recipe"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// レシピの登録・一覧・中身・履歴(taskboard/lib/recipes.sh の cmd_recipe-set / rm / list / show / history)。
// 実行(recipe-run / queue / runner / cancel)は bash に残る。

func validRecipeName(n string) error {
	if !recipe.NameRE.MatchString(n) {
		return usage("レシピ名は英小文字で始まる英小文字・数字・- の 32 字以内(: は使えない): %s", n)
	}
	return nil
}

func cmdRecipeSet(args []string, w, stderr io.Writer) error {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return usage("recipe-set <名前> [--file F](本文は省くと標準入力。書式は docs/recipes.md)")
	}
	name, file := args[0], ""
	if err := validRecipeName(name); err != nil {
		return err
	}
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--file":
			if i+1 >= len(args) {
				return usage("--file に値が必要です")
			}
			file = args[i+1]
			i++
		default:
			return usage("recipe-set: 不明な引数 %s", args[i])
		}
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := requireHuman("tb recipe-set"); err != nil {
		return err
	}
	var body string
	if file != "" {
		st, err := os.Stat(file)
		b, rerr := os.ReadFile(file)
		if err != nil || !st.Mode().IsRegular() || rerr != nil {
			return die("recipe-set: ファイルを読めません: %s", file)
		}
		body = strings.TrimRight(string(b), "\n")
	} else if body, err = readStdin(); err != nil {
		return err
	}
	if _, why := recipe.Parse(body); why != "" { // 誤りは行ごとに出す(API の中でも同じ確かめをする)
		for _, l := range strings.Split(why, "\n") {
			fmt.Fprintf(stderr, "tb: recipe-set: %s\n", l)
		}
		fmt.Fprintln(stderr, "tb: recipe-set: 登録しませんでした")
		return &exit{exitUsage, ""}
	}
	var r apprecipe.SetResult
	if err := callAppWarn(db, "tb recipe-set", stderr, "recipe.set", apprecipe.SetReq{Name: name, Body: body}, &r); err != nil {
		return err
	}
	if r.Pending != nil {
		return pendingExit(w, r.Pending)
	}
	if r.Decl != "" && r.Decl != r.Risk {
		fmt.Fprintf(stderr, "tb: recipe-set: 宣言 risk %s は、中の部品の最大 %s より低いので %s にしました\n", r.Decl, r.Risk, r.Risk)
	}
	what := "updated"
	if r.Added {
		what = "added"
	}
	fmt.Fprintf(w, "%s %s (risk %s, steps %d, op recipe:%s:<対象>)\n", name, what, r.Risk, r.Steps, name)
	return nil
}

func cmdRecipeRm(args []string, w, stderr io.Writer) error {
	if len(args) != 1 {
		return usage("recipe-rm <名前>")
	}
	name := args[0]
	if err := validRecipeName(name); err != nil {
		return err
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := requireHuman("tb recipe-rm"); err != nil {
		return err
	}
	var r apprecipe.RmResult
	if err := callAppWarn(db, "tb recipe-rm", stderr, "recipe.rm", apprecipe.NameReq{Name: name}, &r); err != nil {
		return err
	}
	if r.Pending != nil {
		return pendingExit(w, r.Pending)
	}
	fmt.Fprintf(w, "%s removed\n", name)
	return nil
}

func cmdRecipeList(args []string, w, stderr io.Writer) error {
	json := false
	for _, a := range args {
		if a != "--json" {
			return usage("recipe-list: 不明な引数 %s", a)
		}
		json = true
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := sqlObjs(db.DB, nil, `SELECT name, body, risk, steps, updated_by, updated_at FROM recipes ORDER BY name`)
	if err != nil {
		return err
	}
	for _, o := range rows {
		body, _ := o.Get("body").(string)
		risk, desc := "invalid", "(今のコードでは検査を通らない。実行しない)"
		if r, why := recipe.Parse(body); why == "" {
			risk, desc = r.Risk, r.Desc
		}
		if json {
			fmt.Fprintln(w, jv.Compact(jv.Obj{}.Set("name", o.Get("name")).Set("risk", risk).Set("risk_at_set", o.Get("risk")).
				Set("steps", o.Get("steps")).Set("updated_by", o.Get("updated_by")).Set("updated_at", o.Get("updated_at")).Set("desc", desc)))
			continue
		}
		if risk != tickets.Text(o.Get("risk")) {
			risk += "!"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", tickets.Text(o.Get("name")), risk, tickets.Text(o.Get("steps")),
			tickets.Text(o.Get("updated_by")), tickets.Text(o.Get("updated_at")), dashIfEmpty(desc))
	}
	return nil
}

func cmdRecipeShow(args []string, w, stderr io.Writer) error {
	if len(args) != 1 {
		return usage("recipe-show <名前>")
	}
	name := args[0]
	if err := validRecipeName(name); err != nil {
		return err
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	var body, by, at string
	if db.QueryRow(`SELECT body, updated_by, updated_at FROM recipes WHERE name=?`, name).Scan(&body, &by, &at) != nil {
		return die("レシピが登録されていません: %s", name)
	}
	fmt.Fprintf(w, "name: %s\nop: recipe:%s:<対象>(既定の対象は依頼者自身)\nupdated: %s %s\n", name, name, by, at)
	if r, why := recipe.Parse(body); why == "" {
		fmt.Fprintf(w, "risk: %s\n", r.Risk)
		for i, s := range r.Steps {
			p, _ := recipe.Lookup(recipe.StepPart(s))
			fmt.Fprintf(w, "step %d: %s(%s / %s / %s / 操作名 %s)\n", i+1, s, p.Side, p.Risk, p.Runner, dashIfEmpty(p.Ops))
		}
	} else {
		fmt.Fprintln(w, "risk: invalid(今のコードでは検査を通らないため、実行しない)")
		for _, l := range strings.Split(why, "\n") {
			fmt.Fprintf(w, "error: %s\n", l)
		}
	}
	fmt.Fprintf(w, "---\n%s\n", body)
	return nil
}

func cmdRecipeHistory(args []string, w, stderr io.Writer) error {
	name, json := "", false
	for _, a := range args {
		switch {
		case a == "--json":
			json = true
		case strings.HasPrefix(a, "-"):
			return usage("recipe-history: 不明な引数 %s", a)
		default:
			if name != "" {
				return usage("recipe-history [<名前>] [--json]")
			}
			if err := validRecipeName(a); err != nil {
				return err
			}
			name = a
		}
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	where, qa := "1=1", []any{}
	if name != "" {
		where, qa = "name=?", []any{name}
	}
	if json {
		rows, err := sqlObjs(db.DB, nil, `SELECT id, name, how, old_body, new_body, risk, changed_by, changed_at FROM recipe_changes WHERE `+where+` ORDER BY id`, qa...)
		if err != nil {
			return err
		}
		compactRows(w, rows)
		return nil
	}
	return sqlTSV(db.DB, w, `SELECT changed_at, name, how, COALESCE(risk,'-'), changed_by FROM recipe_changes WHERE `+where+` ORDER BY id`, qa...)
}
