package ticket

// チケットの一覧(tb list)。

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/rowfmt"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// ListReq は ticket.list の入力。Types などはカンマ区切りで、並べたら全部を満たすもの(tb list で同じ引数を繰り返したとき)。
// Today は期日の比較の基準日(YYYY-MM-DD。DueWithin / Overdue のときに要る)。DueWithin は日数のカンマ区切り。
// Format は short(既定)/ long / json。Sort は priority / updated / created / id / due(空なら project, id)。
type ListReq struct {
	Project    string `api:"project,optional"`
	Status     string `api:"status,optional"`
	Types      string `api:"types,optional"`
	Priorities string `api:"priorities,optional"`
	Labels     string `api:"labels,optional"`
	Owners     string `api:"owners,optional"`
	Parents    string `api:"parents,optional"`
	Sprints    string `api:"sprints,optional"` // S-<n> か backlog
	DueWithin  string `api:"due_within,optional"`
	Overdue    bool   `api:"overdue,optional"`
	HasDue     bool   `api:"has_due,optional"`
	Today      string `api:"today,optional"`
	Sort       string `api:"sort,optional"`
	Format     string `api:"format,optional"`
}

// AddTo は v をカンマ区切りの s に足す(tb が引数を繰り返したとき)。
func AddTo(s *string, v ...string) {
	for _, x := range v {
		if *s != "" {
			*s += ","
		}
		*s += x
	}
}

func split(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func listOp(env *app.Env) app.Op {
	return readOp(env, "ticket.list", "cmd:ticket:list", "GET /v1/tickets", "チケットの一覧(tb list)", list)
}

func bad(format string, a ...any) error {
	return define.Errorf(define.CodeInvalidArgument, format, a...)
}

func list(db *store.DB, st tickets.StatusSet, in ListReq) (string, error) {
	if in.Status != "" && !slices.Contains(st.Names, in.Status) {
		return "", bad("status は %s のどれか: %s", strings.Join(st.Names, " / "), in.Status)
	}
	if in.Today != "" {
		if _, err := time.Parse("2006-01-02", in.Today); err != nil || len(in.Today) != 10 {
			return "", bad("期日は YYYY-MM-DD の日付: %s", in.Today)
		}
	}
	for _, v := range split(in.Sprints) {
		if v != "backlog" && !reSprint.MatchString(v) {
			return "", bad("スプリント ID は S-<番号> 形式: %s", v)
		}
	}
	var f tickets.Filter
	closed := st.SQLList("done", "cancelled")
	for _, v := range split(in.Types) {
		f.Add("type=?", v)
	}
	for _, v := range split(in.Priorities) {
		f.Add("priority=?", v)
	}
	for _, l := range split(in.Labels) {
		f.Add("EXISTS (SELECT 1 FROM json_each(tasks.labels) WHERE value=?)", l)
	}
	for _, v := range split(in.Owners) {
		f.Add("owner_agent=?", v)
	}
	for _, v := range split(in.Parents) {
		f.Add("parent_id=?", v)
	}
	for _, v := range split(in.Sprints) {
		if v == "backlog" {
			f.Add("sprint_id IS NULL")
		} else {
			f.Add("sprint_id=?", strings.TrimPrefix(v, "S-"))
		}
	}
	if (in.DueWithin != "" || in.Overdue) && in.Today == "" {
		return "", bad("list: 期日の比較には today(YYYY-MM-DD)が要ります")
	}
	for _, d := range split(in.DueWithin) {
		n, err := strconv.Atoi(d)
		if err != nil || n < 0 || n > 9999 {
			return "", bad("list: --due-within は 0〜9999 の日数: %s", d)
		}
		f.Add("due IS NOT NULL AND due >= ? AND due <= date(?, '+'||?||' days') AND status NOT IN ("+closed+")", in.Today, in.Today, n)
	}
	if in.Overdue {
		f.Add("due IS NOT NULL AND due < ? AND status NOT IN ("+closed+")", in.Today)
	}
	if in.HasDue {
		f.Add("due IS NOT NULL")
	}
	if in.Project != "" {
		f.Add("project=?", in.Project)
	}
	if in.Status != "" {
		f.Add("status=?", in.Status)
	}
	order, ok := tickets.Order(in.Sort)
	if !ok {
		return "", bad("list: --sort は priority / updated / created / id / due のどれか: %s", in.Sort)
	}
	format := tickets.Short
	switch in.Format {
	case "", "short":
	case "long":
		format = tickets.Long
	case "json":
		format = tickets.JSON
	default:
		return "", bad("list: format は short / long / json: %s", in.Format)
	}
	var b strings.Builder
	err := tickets.List(db.DB, &f, order, format, func(cols []string, vals []any) error {
		if format == tickets.JSON {
			s, err := rowfmt.RowJSON(cols, vals)
			if err != nil {
				return err
			}
			b.WriteString(s + "\n")
			return nil
		}
		parts := make([]string, len(vals))
		for i, v := range vals {
			parts[i] = tickets.Text(v)
		}
		b.WriteString(strings.Join(parts, "\t") + "\n")
		return nil
	})
	return b.String(), err
}
