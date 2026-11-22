// Package activity は hook からの活動記録(tb touch / tb activity。ADR 0004 の独立した機能のモジュール)。
// 振る舞いは taskboard/lib/activity.sh と同じにする。
package activity

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

var reID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// Touch は tb touch の引数。
type Touch struct {
	Event, SessionID, PaneID, TabID, WorkspaceID, Cwd, PromptID string
}

// ParseTouch は touch_impl と同じ順に引数を検査する。失敗は理由コード(入力値を含まない)を返す。
func ParseTouch(args []string) (Touch, string) {
	var t Touch
	for i := 0; i < len(args); i += 2 {
		if i+1 >= len(args) {
			return t, "invalid-args"
		}
		v := args[i+1]
		switch args[i] {
		case "--event":
			t.Event = v
		case "--session-id":
			t.SessionID = v
		case "--pane-id":
			t.PaneID = v
		case "--tab-id":
			t.TabID = v
		case "--workspace-id":
			t.WorkspaceID = v
		case "--cwd":
			t.Cwd = v
		case "--prompt-id":
			t.PromptID = v
		default:
			return t, "invalid-args"
		}
	}
	if t.Event != "UserPromptSubmit" && t.Event != "Stop" {
		return t, "invalid-event"
	}
	if t.SessionID == "" {
		return t, "missing-session-id"
	}
	if !reID.MatchString(t.SessionID) {
		return t, "invalid-session-id"
	}
	for _, v := range []string{t.PaneID, t.TabID, t.WorkspaceID, t.PromptID} {
		if v != "" && !reID.MatchString(v) {
			return t, "invalid-field"
		}
	}
	if utf8.RuneCountInString(t.Cwd) > 1024 || strings.Contains(t.Cwd, "\n") {
		return t, "invalid-cwd"
	}
	return t, ""
}

// LogEvent は失敗の記録に書く event(正しい --event の値だけ。最後のもの。無ければ -)。
func LogEvent(args []string) string {
	ev := "-"
	for i := 1; i < len(args); i++ {
		if args[i-1] == "--event" && (args[i] == "UserPromptSubmit" || args[i] == "Stop") {
			ev = args[i]
		}
	}
	return ev
}

// Upsert は session_id の行を作るか更新する。行は Claude の session_id で一意で、
// 別の herdr セッションで再開されたら行ごとこのセッションへ移す(見張りなしの接続で呼ぶ)。
func Upsert(db *sql.DB, session string, t Touch) error {
	ts := "strftime('%Y-%m-%dT%H:%M:%fZ','now')"
	pa, sa := "NULL", "NULL"
	if t.Event == "UserPromptSubmit" {
		pa = ts
	} else {
		sa = ts
	}
	_, err := db.Exec(`INSERT INTO main.agent_activity
      (herdr_session, session_id, pane_id, tab_id, workspace_id, cwd, last_prompt_id, last_prompt_at, last_stop_at, updated_at)
    VALUES (?, ?, NULLIF(?,''), NULLIF(?,''), NULLIF(?,''), NULLIF(?,''), NULLIF(?,''), `+pa+`, `+sa+`, `+ts+`)
    ON CONFLICT(session_id) DO UPDATE SET
      herdr_session=excluded.herdr_session,
      pane_id=COALESCE(excluded.pane_id, pane_id), tab_id=COALESCE(excluded.tab_id, tab_id),
      workspace_id=COALESCE(excluded.workspace_id, workspace_id), cwd=COALESCE(excluded.cwd, cwd),
      last_prompt_id=COALESCE(excluded.last_prompt_id, last_prompt_id),
      last_prompt_at=COALESCE(excluded.last_prompt_at, last_prompt_at),
      last_stop_at=COALESCE(excluded.last_stop_at, last_stop_at),
      updated_at=excluded.updated_at`,
		session, t.SessionID, t.PaneID, t.TabID, t.WorkspaceID, t.Cwd, t.PromptID)
	return err
}

// Reason は DB のエラーを理由コードにする(touch_impl と同じ分け方)。
func Reason(err error) string {
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "locked") || strings.Contains(s, "busy"):
		return "db-locked"
	case strings.Contains(s, "readonly") || strings.Contains(s, "read-only") || strings.Contains(s, "unable to open"):
		return "db-readonly"
	}
	return "db-error"
}

var reMin = regexp.MustCompile(`^[0-9]+$`)

// ValidMinutes は --stuck-min の値(act_select と同じ: 数字だけ)。
func ValidMinutes(s string) bool { return reMin.MatchString(s) }

// selectSQL は act_select と同じ: working の推定と、stuckMin 分以上の滞留を付けた SELECT。
// stuckMin は ValidMinutes で確かめた数字の並び(bash と同じく SQL にそのまま埋める)。
func selectSQL(stuckMin string) string {
	w := "last_prompt_at IS NOT NULL AND (last_stop_at IS NULL OR last_prompt_at > last_stop_at)"
	return fmt.Sprintf(`SELECT session_id, pane_id, tab_id, workspace_id, cwd, last_prompt_id, last_prompt_at, last_stop_at, updated_at,
    CASE WHEN %[1]s THEN 1 ELSE 0 END AS working,
    CASE WHEN %[1]s AND (julianday('now') - julianday(last_prompt_at)) * 1440 >= %[2]s THEN 1 ELSE 0 END AS stuck,
    CASE WHEN last_prompt_at IS NULL THEN NULL
         ELSE CAST((julianday('now') - julianday(last_prompt_at)) * 1440 AS INTEGER) END AS since_prompt_min
    FROM main.agent_activity`, w, stuckMin)
}

// List は session の行を新しい順に返す。full=false なら tb activity の表の列(null は "-")だけ。
func List(db *sql.DB, session, stuckMin string, full bool, row func(cols []string, vals []any) error) error {
	q := selectSQL(stuckMin) + " WHERE herdr_session=? ORDER BY updated_at DESC"
	if !full {
		q = "SELECT session_id, COALESCE(pane_id,'-'), working, stuck, last_prompt_at, last_stop_at, COALESCE(cwd,'-') FROM (" +
			selectSQL(stuckMin) + " WHERE herdr_session=?) ORDER BY updated_at DESC"
	}
	rows, err := db.Query(q, session)
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
		if err := row(cols, vals); err != nil {
			return err
		}
	}
	return rows.Err()
}
