package tbcli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	apprecipe "github.com/imohiyoko/herdrorchestrator/internal/app/recipe"
	"github.com/imohiyoko/herdrorchestrator/internal/recipe"
)

// レシピの登録・一覧・中身・履歴(taskboard/lib/recipes.sh の cmd_recipe-set / rm / list / show / history)。
// 実行(recipe-run / runner)と取り消しはアプリケーション API(internal/app/recipe)。一覧の recipe-queue は reciperun.go。

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
	return recipeRead(stderr, "recipe.list", apprecipe.ListReq{JSON: json}, w)
}

func cmdRecipeShow(args []string, w, stderr io.Writer) error {
	if len(args) != 1 {
		return usage("recipe-show <名前>")
	}
	name := args[0]
	if err := validRecipeName(name); err != nil {
		return err
	}
	return recipeRead(stderr, "recipe.show", apprecipe.NameReq{Name: name}, w)
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
	return recipeRead(stderr, "recipe.history", apprecipe.HistoryReq{Name: name, JSON: json}, w)
}

// recipeRead は読むだけの操作 op を呼び、tb の出力(Text)をそのまま出す。
func recipeRead(stderr io.Writer, op string, in any, w io.Writer) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	var r apprecipe.TextResult
	if err := callAppW(db, app.Human(), io.Discard, op, in, &r); err != nil {
		return err
	}
	fmt.Fprint(w, r.Text)
	return nil
}
