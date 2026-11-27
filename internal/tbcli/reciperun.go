package tbcli

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	apprecipe "github.com/imohiyoko/herdrorchestrator/internal/app/recipe"
)

// レシピの実行(tb recipe run / runner / queue / cancel)。判定と実行はアプリケーション API(recipe.run / recipe.runner。
// internal/app/recipe/run.go)。ここは引数の読み取りと表示だけ。

var (
	reRunnerMax = regexp.MustCompile(`^[0-9]{1,3}$`)
	reRid       = regexp.MustCompile(`^R-([0-9]{1,18})$`)
)

// cmdRecipeRun は tb recipe-run <名前> [--target A] [--task T-..] [--dry-run] [<名前>=<値> ...]。
// 終了コード 0=済んだか待ち行列に置いた / 1=途中の部品が失敗 / 2=使い方 / 4=実行しない / 20=人の承認が要る
func cmdRecipeRun(args []string, w, stderr io.Writer) error {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return usage("recipe-run <名前> [--target A] [--task T-..] [--dry-run] [<名前>=<値> ...]")
	}
	in := apprecipe.RunReq{Name: args[0]}
	if err := validRecipeName(in.Name); err != nil {
		return err
	}
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--target" || a == "--task":
			v, err := needVal(args, i)
			if err != nil {
				return err
			}
			i++
			if a == "--target" {
				in.Target = v
			} else {
				if err := needID(v); err != nil {
					return err
				}
				in.Task = v
			}
		case a == "--dry-run":
			in.DryRun = true
		case strings.HasPrefix(a, "-"):
			return usage("recipe-run: 不明な引数 %s", a)
		default:
			in.Args = append(in.Args, a)
		}
	}
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	by, err := requesterPrincipal()
	if err != nil {
		return err
	}
	env := appEnv(db, stderr)
	env.Out = w // 部品の出力は実行しながら出す
	env.SchedJob, env.SchedOwner = os.Getenv("SCHED_JOB"), os.Getenv("SCHED_OWNER")
	var r apprecipe.RunResult
	if err := callAppEnv(env, by, "recipe.run", in, &r); err != nil {
		return err
	}
	if r.Pending != nil {
		return pendingExit(w, r.Pending)
	}
	fmt.Fprint(w, r.Out)
	flush(w)
	fmt.Fprint(stderr, r.Err)
	if r.Code != 0 {
		return &exit{r.Code, ""}
	}
	return nil
}

// cmdRecipeRunner は tb recipe-runner [--max N]: 待ち行列の queued を古い順に取り出し、判定し直してから実行する(既定は人の端末からだけ)。
func cmdRecipeRunner(args []string, w, stderr io.Writer) error {
	max := 1
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--max":
			v, err := needVal(args, i)
			if err != nil {
				return err
			}
			if !reRunnerMax.MatchString(v) || atoi(v) < 1 {
				return usage("recipe-runner: --max は 1〜999: %s", v)
			}
			max = atoi(v)
			i++
		default:
			return usage("recipe-runner: 不明な引数 %s", args[i])
		}
	}
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	var r apprecipe.RunnerResult
	if err := callAppWarn(db, "tb recipe-runner", stderr, "recipe.runner", apprecipe.RunnerReq{Max: max}, &r); err != nil {
		return err
	}
	if r.Pending != nil {
		return pendingExit(w, r.Pending)
	}
	fmt.Fprint(stderr, r.Err)
	for _, l := range r.Lines {
		fmt.Fprintln(w, l)
	}
	return nil
}

// cmdRecipeQueue は tb recipe-queue [--all] [--json]。
func cmdRecipeQueue(args []string, w, stderr io.Writer) error {
	where := "status IN ('queued','running')"
	json := false
	for _, a := range args {
		switch a {
		case "--all":
			where = "1=1"
		case "--json":
			json = true
		default:
			return usage("recipe-queue: 不明な引数 %s", a)
		}
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if json {
		rows, err := sqlObjs(db.DB, []string{"plan"}, `SELECT 'R-'||id AS id, status, name, target, requester, via, task_id, plan, 'E-'||event_id AS event, created_at, started_at, finished_at, result FROM recipe_runs WHERE `+where+` ORDER BY recipe_runs.id`)
		if err != nil {
			return err
		}
		compactRows(w, rows)
		return nil
	}
	for _, r := range strRows(db, `SELECT 'R-'||id, status, name, target, requester, created_at, COALESCE(replace(result, char(10), ' '),'-') FROM recipe_runs WHERE `+where+` ORDER BY recipe_runs.id`) {
		fmt.Fprintln(w, strings.Join(r, "\t"))
	}
	return nil
}

// cmdRecipeCancel は tb recipe-cancel R-<n>: 待ち行列の queued を取り消す。人と、頼んだ agent 自身だけ。
func cmdRecipeCancel(args []string, w, stderr io.Writer) error {
	if len(args) != 1 || !reRid.MatchString(args[0]) {
		return usage("recipe-cancel R-<n>")
	}
	idS := reRid.FindStringSubmatch(args[0])[1]
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	by, err := requesterPrincipal()
	if err != nil {
		return err
	}
	var r apprecipe.CancelResult
	if err := callAppEnv(appEnv(db, stderr), by, "recipe.cancel", apprecipe.CancelReq{ID: args[0]}, &r); err != nil {
		return err
	}
	fmt.Fprintf(w, "R-%s cancelled\n", idS)
	return nil
}
