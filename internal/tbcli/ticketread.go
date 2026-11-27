package tbcli

// チケットの読むだけのコマンド(taskboard/lib/tasks.sh の show / tree / labels / sprints / board / history / events)。
// 出し方は bash と同じ(sqlite3 の -separator と -json を jq で整えたもの)。

import (
	"database/sql"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/app/ticket"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

var (
	reEventID = regexp.MustCompile(`^E-([0-9]{1,18})$`)
	reKind    = regexp.MustCompile(`^[a-z_]{1,32}$`)
	reEvLimit = regexp.MustCompile(`^[0-9]{1,6}$`)
)

func needID(v string) error {
	if !reID.MatchString(v) {
		return usage("タスク ID は T-<project>-NNN 形式: %s", v)
	}
	return nil
}

func needProject(v string) error {
	if !reProject.MatchString(v) {
		return usage("project は英数字と _ のみ: %s", v)
	}
	return nil
}

func needSprint(v string) error {
	if !reSprint.MatchString(v) {
		return usage("スプリント ID は S-<番号> 形式: %s", v)
	}
	return nil
}

// argv は need_arg のある引数の読み方(値が無ければ「<名前> に値が必要です」)。
func argv(a []string, i int) (string, error) {
	if i+1 >= len(a) {
		return "", usage("%s に値が必要です", a[i])
	}
	return a[i+1], nil
}

// qrows は行ごとに cols と vals を渡す。
func qrows(db *sql.DB, q string, args []any, f func(cols []string, vals []any) error) error {
	rows, err := db.Query(q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		if err := f(cols, vals); err != nil {
			return err
		}
	}
	return rows.Err()
}

// sqlTSV は sql -separator $'\t'(NULL は空)。
func sqlTSV(db *sql.DB, w io.Writer, q string, args ...any) error {
	return qrows(db, q, args, func(_ []string, vals []any) error {
		s := make([]string, len(vals))
		for i, v := range vals {
			s[i] = tickets.Text(v)
		}
		fmt.Fprintln(w, strings.Join(s, "\t"))
		return nil
	})
}

// jvValue は sqlite3 -json の 1 つの値。
func jvValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case int64:
		return jv.Number(tickets.Text(x))
	case float64:
		return jv.Number(tickets.Text(x))
	default:
		return tickets.Text(v)
	}
}

// sqlObjs は sql -json の行(列の順のまま)。fromjson の列は JSON として読む。
func sqlObjs(db *sql.DB, fromjson []string, q string, args ...any) ([]jv.Obj, error) {
	var out []jv.Obj
	err := qrows(db, q, args, func(cols []string, vals []any) error {
		o := jv.Obj{}
		for i, c := range cols {
			v := jvValue(vals[i])
			if slices.Contains(fromjson, c) {
				s, _ := v.(string)
				vs, err := jv.DecodeAll([]byte(s))
				if err != nil || len(vs) != 1 {
					return fmt.Errorf("jq: error: %s を JSON として読めません", c)
				}
				v = vs[0]
			}
			o = append(o, jv.KV{K: c, V: v})
		}
		out = append(out, o)
		return nil
	})
	return out, err
}

func compactRows(w io.Writer, os []jv.Obj) {
	for _, o := range os {
		fmt.Fprintln(w, jv.Compact(o))
	}
}

// cmdShow は tb show <id>: 1 件(担当 agent の最終活動とリンクつき)。
func cmdShow(args []string, w, stderr io.Writer) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(args) != 1 {
		return usage("show <id>")
	}
	id := args[0]
	if err := needID(id); err != nil {
		return err
	}
	var r ticket.ShowResult
	if err := callApp(db, "tb show", "ticket.show", ticket.GetReq{ID: id}, &r); err != nil {
		return err
	}
	fmt.Fprintln(w, r.Text)
	return nil
}

// readText は読むだけの操作を呼んで、Text をそのまま出す。
func readText(db *store.DB, what, op string, in any, w io.Writer) error {
	var r ticket.TextResult
	if err := callApp(db, what, op, in, &r); err != nil {
		return err
	}
	fmt.Fprint(w, r.Text)
	return nil
}

// cmdTree は tb tree <id> | tree -p <project>。
func cmdTree(args []string, w, stderr io.Writer) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	var in ticket.TreeReq
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-p" || a == "--project":
			v, err := argv(args, i)
			if err != nil {
				return err
			}
			in.Project = v
			i++
		case strings.HasPrefix(a, "-"):
			return usage("tree: 不明な引数 %s", a)
		default:
			if in.ID != "" {
				return usage("tree <id> | tree -p <project>")
			}
			if err := needID(a); err != nil {
				return err
			}
			in.ID = a
		}
	}
	if in.ID == "" && in.Project == "" {
		return usage("tree <id> | tree -p <project>")
	}
	return readText(db, "tb tree", "ticket.tree", in, w)
}

// cmdLabels は tb labels [-p project] [--defined] [--json]。
func cmdLabels(args []string, w, stderr io.Writer) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	var in ticket.LabelsReq
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p", "--project":
			v, err := argv(args, i)
			if err != nil {
				return err
			}
			in.Project = v
			i++
		case "--defined":
			in.Defined = true
		case "--json":
			in.JSON = true
		default:
			return usage("labels: 不明な引数 %s", args[i])
		}
	}
	return readText(db, "tb labels", "ticket.labels", in, w)
}

// cmdSprints は tb sprints [-p project] [--state S] [--json]。
func cmdSprints(args []string, w, stderr io.Writer) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	var in ticket.SprintsReq
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p", "--project", "--state":
			v, err := argv(args, i)
			if err != nil {
				return err
			}
			if args[i] == "--state" {
				if !slices.Contains([]string{"planned", "active", "closed"}, v) {
					return usage("state は planned / active / closed のどれか: %s", v)
				}
				in.State = v
			} else {
				in.Project = v
			}
			i++
		case "--json":
			in.JSON = true
		default:
			return usage("sprints: 不明な引数 %s", args[i])
		}
	}
	return readText(db, "tb sprints", "ticket.sprints", in, w)
}

// boardCols は ST_BOARD: 分類の順、同じ分類は書いた順。
func boardCols(st tickets.StatusSet) []string {
	var out []string
	for _, c := range tickets.Categories {
		for _, k := range st.Names {
			if st.Cat(k) == c {
				out = append(out, k)
			}
		}
	}
	return out
}

// cmdBoard は tb board [S-<n> | -p <project>] [-l ラベル] [--json]。
func cmdBoard(args []string, w, stderr io.Writer) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	var in ticket.BoardReq
	var ls []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-p" || a == "--project":
			v, err := argv(args, i)
			if err != nil {
				return err
			}
			in.Project = v
			i++
		case a == "-l" || a == "--label":
			v, err := argv(args, i)
			if err != nil {
				return err
			}
			l, err := labels(v)
			if err != nil {
				return err
			}
			ls = append(ls, l...)
			i++
		case a == "--json":
			in.JSON = true
		case strings.HasPrefix(a, "S-"):
			if err := needSprint(a); err != nil {
				return err
			}
			in.Sprint = a
		default:
			return usage("board: 不明な引数 %s", a)
		}
	}
	if in.Sprint == "" && in.Project == "" {
		return usage("board S-<n> | board -p <project>")
	}
	in.Labels = strings.Join(ls, ",")
	return readText(db, "tb board", "ticket.board", in, w)
}

// cmdHistory は tb history <id> [--json]。
func cmdHistory(args []string, w, stderr io.Writer) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	var in ticket.HistoryReq
	for _, a := range args {
		switch {
		case a == "--json":
			in.JSON = true
		case strings.HasPrefix(a, "-"):
			return usage("history: 不明な引数 %s", a)
		default:
			if in.ID != "" {
				return usage("history <id> [--json]")
			}
			if err := needID(a); err != nil {
				return err
			}
			in.ID = a
		}
	}
	if in.ID == "" {
		return usage("history <id> [--json]")
	}
	return readText(db, "tb history", "ticket.history", in, w)
}

// cmdEvents は tb events [--since E-n] [--kind K] [-p project] [--limit N] [--json]: 最新の N 件を古い順に。
func cmdEvents(args []string, w, stderr io.Writer) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	var in ticket.EventsReq
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--since", "--kind", "-p", "--project", "--limit":
			v, err := argv(args, i)
			if err != nil {
				return err
			}
			i++
			switch a {
			case "--since":
				if !reEventID.MatchString(v) {
					return usage("events: --since は E-<数字>: %s", v)
				}
				in.Since = v
			case "--kind":
				if !reKind.MatchString(v) {
					return usage("events: --kind が不正です: %s", v)
				}
				in.Kind = v
			case "--limit":
				n := strings.TrimLeft(v, "0")
				if !reEvLimit.MatchString(v) || n == "" {
					return usage("events: --limit は 1〜999999: %s", v)
				}
				in.Limit, _ = strconv.Atoi(n)
			default:
				if err := needProject(v); err != nil {
					return err
				}
				in.Project = v
			}
		case "--json":
			in.JSON = true
		default:
			return usage("events: 不明な引数 %s", a)
		}
	}
	return readText(db, "tb events", "ticket.events", in, w)
}
