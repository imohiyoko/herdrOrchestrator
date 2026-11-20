// Package tickets はチケット(tasks 表)の読み書き(ADR 0004 のチケットの通り道)。
// 振る舞いは taskboard/lib/tasks.sh と同じにする。
package tickets

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// 値の一覧(taskboard/lib/core.sh と同じ)
var (
	Statuses   = []string{"todo", "in_progress", "review", "done", "blocked", "cancelled"}
	Types      = []string{"epic", "story", "task", "bug", "subtask"}
	Priorities = []string{"highest", "high", "medium", "low", "lowest"}
)

// PriorityRank は優先度の並び(tasks.sh の PRIORITY_RANK)。
const PriorityRank = "CASE priority WHEN 'highest' THEN 1 WHEN 'high' THEN 2 WHEN 'medium' THEN 3 WHEN 'low' THEN 4 ELSE 5 END"

// Filter は tb list の絞り込み。SQL の WHERE 句と引数に組み立てる。
type Filter struct {
	where []string
	args  []any
}

func (f *Filter) Add(cond string, args ...any) {
	f.where = append(f.where, cond)
	f.args = append(f.args, args...)
}

func (f *Filter) sql() string {
	if len(f.where) == 0 {
		return "1=1"
	}
	return "1=1 AND " + strings.Join(f.where, " AND ")
}

// Order は --sort の値から ORDER BY を返す。不明なら ok=false。
func Order(sort string) (string, bool) {
	switch sort {
	case "":
		return "project, id", true
	case "priority":
		return PriorityRank + ", updated_at DESC, id", true
	case "updated":
		return "updated_at DESC, id", true
	case "created":
		return "created_at DESC, id", true
	case "id":
		return "id", true
	case "due":
		return "due IS NULL, due, " + PriorityRank + ", id", true
	}
	return "", false
}

// Format は tb list の出し方。
type Format int

const (
	Short Format = iota // id status owner title priority(前の 4 列の位置は変えない)
	Long                // --long
	JSON                // --json(1 行 1 件)
)

// List は条件に合うチケットを、このセッションのビュー(tasks)から読む。rows は出す順に呼ばれる。
func List(db *sql.DB, f *Filter, order string, format Format, emit func(cols []string, vals []any) error) error {
	var q string
	switch format {
	case JSON:
		q = "SELECT * FROM tasks"
	case Long:
		q = `SELECT id, status, type, priority, COALESCE(owner_agent,'-'), created_at, updated_at,
        COALESCE((SELECT group_concat(value, ',') FROM json_each(tasks.labels)), '-'), replace(title, char(10), ' '), COALESCE(due, '-'),
        COALESCE('S-'||sprint_id, '-'), COALESCE(points, '-') FROM tasks`
	default:
		q = "SELECT id, status, COALESCE(owner_agent,'-'), replace(replace(title, char(10), ' '), char(9), ' '), priority FROM tasks"
	}
	rows, err := db.Query(q+" WHERE ("+f.sql()+") ORDER BY "+order, f.args...)
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
		if err := emit(cols, vals); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Text は sqlite3 の CLI と同じ書き方で値を文字にする(NULL は空)。
func Text(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', 15, 64)
	case []byte:
		return string(x)
	case string:
		return x
	default:
		return fmt.Sprint(x)
	}
}
