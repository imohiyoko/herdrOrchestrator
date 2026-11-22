package tickets

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// SetupEvents は db.sh の event_setup: この接続に一時トリガーを作り、tasks / comments / task_links への書き込みと
// 同じトランザクションで task_events に 1 件ずつ残す(変えた者 actor を埋め込む)。task_events が無い DB では何もしない。
// 2 回目に呼ぶと前のトリガーを作り直す(変えた者を引き直したとき)。
func SetupEvents(db *sql.DB, actor string) error {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM main.sqlite_master WHERE type='table' AND name='task_events'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	for _, t := range []string{"_tb_ev_tasks_i", "_tb_ev_tasks_u", "_tb_ev_comments_i", "_tb_ev_links_i", "_tb_ev_links_d"} {
		if _, err := db.Exec("DROP TRIGGER IF EXISTS temp." + t); err != nil {
			return err
		}
	}
	for _, s := range eventTriggers(actor) {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("tickets: 履歴のトリガー: %w", err)
		}
	}
	return nil
}

func eventTriggers(actor string) []string {
	a := store.Quote(actor)
	ts := "strftime('%Y-%m-%dT%H:%M:%SZ','now')"
	ins := "INSERT INTO task_events (herdr_session, task_id, kind, old, new, actor, ref, created_at)"
	var b strings.Builder
	col := func(kind, c string) {
		fmt.Fprintf(&b, "%s SELECT NEW.herdr_session, NEW.id, '%s', OLD.%s, NEW.%s, %s, NULL, %s WHERE OLD.%s IS NOT NEW.%s;", ins, kind, c, c, a, ts, c, c)
	}
	var out []string
	b.WriteString("CREATE TEMP TRIGGER _tb_ev_tasks_i AFTER INSERT ON main.tasks BEGIN ")
	fmt.Fprintf(&b, "%s SELECT NEW.herdr_session, NEW.id, 'created', NULL, NEW.status, %s, NULL, %s;", ins, a, ts)
	fmt.Fprintf(&b, "%s SELECT NEW.herdr_session, NEW.id, 'label_added', NULL, value, %s, NULL, %s FROM json_each(NEW.labels);", ins, a, ts)
	fmt.Fprintf(&b, "%s SELECT NEW.herdr_session, NEW.id, 'ref_added', NULL, json_extract(value,'$.url'), %s, NULL, %s FROM json_each(NEW.refs);", ins, a, ts)
	b.WriteString("END;")
	out = append(out, b.String())
	b.Reset()
	b.WriteString("CREATE TEMP TRIGGER _tb_ev_tasks_u AFTER UPDATE ON main.tasks BEGIN ")
	col("status", "status")
	col("title", "title")
	col("type", "type")
	col("priority", "priority")
	col("owner", "owner_agent")
	col("due", "due")
	col("parent", "parent_id")
	col("points", "points")
	// スプリント(019)は S-<n> で残す(NULL = バックログ)
	fmt.Fprintf(&b, "%s SELECT NEW.herdr_session, NEW.id, 'sprint', 'S-'||OLD.sprint_id, 'S-'||NEW.sprint_id, %s, NULL, %s WHERE OLD.sprint_id IS NOT NEW.sprint_id;", ins, a, ts)
	// 説明は本文を履歴に写さない(変わったことだけ)
	fmt.Fprintf(&b, "%s SELECT NEW.herdr_session, NEW.id, 'description', NULL, NULL, %s, NULL, %s WHERE OLD.description IS NOT NEW.description;", ins, a, ts)
	fmt.Fprintf(&b, "%s SELECT NEW.herdr_session, NEW.id, 'label_added', NULL, value, %s, NULL, %s FROM json_each(NEW.labels) WHERE value NOT IN (SELECT value FROM json_each(OLD.labels));", ins, a, ts)
	fmt.Fprintf(&b, "%s SELECT NEW.herdr_session, NEW.id, 'label_removed', value, NULL, %s, NULL, %s FROM json_each(OLD.labels) WHERE value NOT IN (SELECT value FROM json_each(NEW.labels));", ins, a, ts)
	fmt.Fprintf(&b, "%s SELECT NEW.herdr_session, NEW.id, 'ref_added', NULL, json_extract(value,'$.url'), %s, NULL, %s FROM json_each(NEW.refs) WHERE json_extract(value,'$.url') NOT IN (SELECT json_extract(value,'$.url') FROM json_each(OLD.refs));", ins, a, ts)
	fmt.Fprintf(&b, "%s SELECT NEW.herdr_session, NEW.id, 'ref_removed', json_extract(value,'$.url'), NULL, %s, NULL, %s FROM json_each(OLD.refs) WHERE json_extract(value,'$.url') NOT IN (SELECT json_extract(value,'$.url') FROM json_each(NEW.refs));", ins, a, ts)
	b.WriteString("END;")
	out = append(out, b.String())
	// コメントは報告者(herdr から引いた名前)を変えた者にする。本文は写さない(種類と C-<n> だけ)
	out = append(out, fmt.Sprintf("CREATE TEMP TRIGGER _tb_ev_comments_i AFTER INSERT ON main.comments BEGIN %s SELECT NEW.herdr_session, NEW.task_id, 'comment', NULL, NEW.kind, NEW.from_name, 'C-'||NEW.id, %s; END;", ins, ts))
	// リンクは両方のタスクに、それぞれから見た向きで残す
	inv := func(x string) string {
		return fmt.Sprintf("CASE %[1]s.link_type WHEN 'blocks' THEN 'blocked_by' WHEN 'duplicates' THEN 'duplicated_by' ELSE %[1]s.link_type END", x)
	}
	out = append(out, fmt.Sprintf(`CREATE TEMP TRIGGER _tb_ev_links_i AFTER INSERT ON main.task_links BEGIN
	%s SELECT NEW.herdr_session, NEW.from_id, 'link', NULL, NEW.link_type||' '||NEW.to_id, NEW.created_by, NULL, %s;
	%s SELECT NEW.herdr_session, NEW.to_id, 'link', NULL, %s||' '||NEW.from_id, NEW.created_by, NULL, %s; END;`, ins, ts, ins, inv("NEW"), ts))
	out = append(out, fmt.Sprintf(`CREATE TEMP TRIGGER _tb_ev_links_d AFTER DELETE ON main.task_links BEGIN
	%s SELECT OLD.herdr_session, OLD.from_id, 'unlink', OLD.link_type||' '||OLD.to_id, NULL, %s, NULL, %s;
	%s SELECT OLD.herdr_session, OLD.to_id, 'unlink', %s||' '||OLD.from_id, NULL, %s, NULL, %s; END;`, ins, a, ts, ins, inv("OLD"), a, ts))
	return out
}
