package agents

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
)

// Kinds は kinds.sh の KIND_ALL、KindDefault は KIND_DEFAULT。
var Kinds = []string{"claude", "codex", "gemini"}

const KindDefault = "claude"

// liveRow は agent_sync が herdr agent list の 1 件から取る項目(jq の tostring と、\x1f・改行を空白にしたもの)。
type liveRow struct{ name, pane, sess, cwd, kind string }

// jqStr は jq の (x // "") | tostring(null と false は空)。
func jqStr(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "false" {
		return ""
	}
	var s string
	if raw[0] == '"' && json.Unmarshal(raw, &s) == nil {
		return s
	}
	var b bytes.Buffer
	if json.Compact(&b, raw) == nil {
		return b.String()
	}
	return string(raw)
}

func clean(s string) string { return strings.NewReplacer("\x1f", " ", "\n", " ").Replace(s) }

// liveRows は load_live と agent_sync の jq: オブジェクトの要素で、名前が空でないもの。
func liveRows(out []byte) ([]liveRow, error) {
	var r struct {
		Result struct {
			Agents []json.RawMessage `json:"agents"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return nil, err
	}
	var rows []liveRow
	for _, raw := range r.Result.Agents {
		var m map[string]json.RawMessage
		if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) || json.Unmarshal(raw, &m) != nil {
			continue
		}
		name := jqStr(m["name"])
		if name == "" {
			continue
		}
		var as map[string]json.RawMessage
		var sess string
		if json.Unmarshal(m["agent_session"], &as) == nil {
			sess = jqStr(as["value"])
		}
		cwd := jqStr(m["foreground_cwd"])
		if cwd == "" {
			cwd = jqStr(m["cwd"])
		}
		rows = append(rows, liveRow{clean(name), clean(jqStr(m["pane_id"])), clean(sess), clean(cwd), clean(jqStr(m["agent"]))})
	}
	return rows, nil
}

// Sync は agent_sync: herdr の一覧を台帳に写す(いる agent は running、台帳で running なのに一覧にいないものは stopped)。
// herdr に届かなければ err(台帳は変えない)。
func Sync(db *sql.DB, session string) error {
	_, err := SyncLive(db, session)
	return err
}

// SyncLive は Sync と同じに台帳を合わせ、読んだ herdr agent list(bash の LIVE_AGENTS)も返す。
// 一覧が読めて台帳の書き込みだけ失敗したときは、一覧とエラーの両方を返す。
func SyncLive(db *sql.DB, session string) ([]herdr.Agent, error) {
	return SyncLiveContext(context.Background(), db, session)
}

// SyncLiveContext は SyncLive に ctx を足したもの(期限を過ぎたら herdr を止めて失敗する)。
func SyncLiveContext(ctx context.Context, db *sql.DB, session string) ([]herdr.Agent, error) {
	out, err := herdr.CallContext(ctx, "agent", "list")
	if err != nil {
		return nil, err
	}
	live, _ := herdr.ParseAgents(out)
	return live, syncRows(db, session, out)
}

func syncRows(db *sql.DB, session string, out []byte) error {
	rows, err := liveRows(out)
	if err != nil {
		return err
	}
	ts := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	ctx := context.Background()
	c, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err := c.ExecContext(ctx, "BEGIN"); err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			_, _ = c.ExecContext(ctx, "ROLLBACK")
		}
	}()
	names := []any{}
	for _, r := range rows {
		names = append(names, r.name) // 台帳の規則に合わない名前(解析役 analyst-A<n> など)も「いる」ものとして扱う
		if !NameRE.MatchString(r.name) {
			continue
		}
		kind := r.kind
		if !slices.Contains(Kinds, kind) {
			kind = ""
		}
		ins := kind
		if ins == "" {
			ins = KindDefault
		}
		// 個体が変わった(herdr が既知の kind を教え、台帳の kind と違う)なら kind / model / session を入れ替える
		if _, err := c.ExecContext(ctx, `INSERT INTO main.agents (name, kind, herdr_session, pane_id, session_id, cwd, state, killable, created_by, created_at, updated_at)
		  VALUES (?1, ?2, ?3, NULLIF(?4,''), NULLIF(?5,''),
		          COALESCE((SELECT cwd FROM main.agent_activity WHERE session_id=?5 AND cwd IS NOT NULL), NULLIF(?6,'')),
		          'running', 'unknown', 'herdr', ?7, ?7)
		  ON CONFLICT(herdr_session, name) DO UPDATE SET state='running', stopped_at=NULL, pane_id=excluded.pane_id,
		    kind=CASE WHEN (?8!='' AND ?8!=kind) THEN ?8 ELSE kind END,
		    model=CASE WHEN (?8!='' AND ?8!=kind) THEN NULL ELSE model END,
		    session_id=CASE WHEN (?8!='' AND ?8!=kind) THEN excluded.session_id ELSE COALESCE(excluded.session_id, session_id) END,
		    cwd=COALESCE(excluded.cwd, cwd), updated_at=?7`,
			r.name, ins, session, r.pane, r.sess, r.cwd, ts, kind); err != nil {
			return err
		}
	}
	ph := "''"
	if len(names) > 0 {
		ph = strings.Repeat("?,", len(names)-1) + "?"
	}
	args := append([]any{ts, ts, session}, names...)
	if _, err := c.ExecContext(ctx, `UPDATE main.agents SET state='stopped', stopped_at=?, updated_at=? WHERE herdr_session=? AND state='running' AND name NOT IN (`+ph+`)`, args...); err != nil {
		return err
	}
	if _, err := c.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	done = true
	return nil
}
