// Package proposal は会計役 → 設定役(人事)への改善提案(taskboard/lib/review.sh の proposal*)。
// 提案は project proposal のチケットでもある(020。proposals ビューの INSTEAD OF トリガーがチケットと proposal_details に書く)。
package proposal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/transport"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
)

func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }

// Target は proposal_target: 提案の宛先・担当。設定 projects の proposal=<名前> の行(最後の行が勝つ)。
// 無ければ既定の宛先(projects の * の行)。どちらも無ければ空(役割の名前をコードで決め打ちしない)。
func Target(db *sql.DB, session string) string {
	t, star := "", ""
	body, _, ok := config.Lookup(db, session, "projects")
	if !ok {
		return ""
	}
	for _, line := range config.Lines(body) {
		k, v := readKV(line)
		k, v = dropSpace(k), dropSpace(v)
		if k == "proposal" && v != "" {
			t = v
		}
		if k == "*" && v != "" {
			star = v
		}
	}
	if t == "" {
		t = star
	}
	return t
}

// readKV は IFS='=' read -r k v と同じ分け方(残りが「値=」の 1 つだけなら末尾の = を落とす)。
func readKV(line string) (string, string) {
	k, v, found := strings.Cut(line, "=")
	if !found {
		return k, ""
	}
	if strings.HasSuffix(v, "=") && !strings.Contains(v[:len(v)-1], "=") {
		v = v[:len(v)-1]
	}
	return k, v
}

func dropSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if config.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

// Add は提案を保存し、チケットの担当を target にして、振った番号(P-<n> の n)を返す。
// ビュー経由では last_insert_rowid() が使えないので、同じトランザクション(BEGIN IMMEDIATE)で振った番号を読む。
func Add(db *sql.DB, session, role, title, text, by, target string) (id int64, err error) {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()
	if _, err = conn.ExecContext(ctx, `INSERT INTO main.proposals (herdr_session, role_file, title, text, status, by_name, created_at) VALUES (?, ?, ?, ?, 'open', ?, ?)`,
		session, role, title, text, by, now()); err != nil {
		return 0, err
	}
	if _, err = conn.ExecContext(ctx, `UPDATE main.tasks SET owner_agent=? WHERE herdr_session=? AND id=(SELECT task_id FROM main.proposal_details WHERE proposal_no=(SELECT MAX(proposal_no) FROM main.proposal_details))`,
		target, session); err != nil {
		return 0, err
	}
	if err = conn.QueryRowContext(ctx, `SELECT MAX(proposal_no) FROM main.proposal_details`).Scan(&id); err != nil {
		return 0, err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return id, err
}

// Row は proposals ビューの 1 行(このセッションのもの)。
type Row struct {
	ID                                    int64
	RoleFile, Title, Text, Status, ByName sql.NullString
	Note, DeliveredAt, CreatedAt, TaskID  sql.NullString
}

// ErrNotFound は提案が無いとき。
var ErrNotFound = errors.New("not found")

// Get は session_row proposals id <n>。
func Get(db *sql.DB, id int64) (Row, error) {
	var r Row
	err := db.QueryRow(`SELECT id, role_file, title, text, status, by_name, note, delivered_at, created_at, task_id FROM proposals WHERE id=?`, id).
		Scan(&r.ID, &r.RoleFile, &r.Title, &r.Text, &r.Status, &r.ByName, &r.Note, &r.DeliveredAt, &r.CreatedAt, &r.TaskID)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// jqs は jq の文字列の差し込み(\(.x))と同じ(NULL は null)。
func jqs(s sql.NullString) string {
	if !s.Valid {
		return "null"
	}
	return s.String
}

// Deliver は proposal_deliver: まだ届けていなければ設定役へ送り、届いたら delivered_at を書く。
// 戻り値は transport.Send と同じ(届け済み・無いときは 0)。
func Deliver(db *sql.DB, session string, id int64, stderr io.Writer) (int, error) {
	r, err := Get(db, id)
	if errors.Is(err, ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if r.DeliveredAt.Valid && r.DeliveredAt.String != "" {
		return 0, nil
	}
	n := strconv.FormatInt(id, 10)
	msg := "[from: tb / proposal / P-" + n + " / info] " + jqs(r.ByName) + " から roles/" + jqs(r.RoleFile) + " の改善提案: " + util.Oneline(jqs(r.Title), 120) +
		" (全文: tb proposal show P-" + n + "。対応後に tb proposal resolve P-" + n + ")"
	if rc := transport.Send(db, session, Target(db, session), msg, "P-"+n, "proposal", stderr); rc != transport.Sent {
		return rc, nil
	}
	_, err = db.Exec(`UPDATE main.proposals SET delivered_at=? WHERE herdr_session=? AND id=?`, now(), session, id)
	return 0, err
}

// Show は tb proposal show の本文(jq で組んでいたものと同じ)。
func Show(r Row) string {
	task := "-"
	if r.TaskID.Valid {
		task = r.TaskID.String
	}
	note := ""
	if r.Note.Valid {
		note = "\n対応: " + r.Note.String
	}
	return fmt.Sprintf("P-%d [%s] roles/%s  %s\n提案: %s / %s  チケット: %s%s\n\n%s",
		r.ID, jqs(r.Status), jqs(r.RoleFile), jqs(r.Title), jqs(r.ByName), jqs(r.CreatedAt), task, note, jqs(r.Text))
}

// List は提案の一覧(新しい順に 100 件)。status が空なら全部。NULL は -(sqlite3 -nullvalue -)。
func List(db *sql.DB, status string, fn func(cols []string, vals []any) error) error {
	where, args := "1=1", []any{}
	if status != "" {
		where, args = "status=?", []any{status}
	}
	return store.Each(db, "SELECT 'P-'||id, status, role_file, by_name, created_at, title FROM proposals WHERE ("+where+") ORDER BY id DESC LIMIT 100", args, fn)
}

// Status は提案の状態(無ければ空)。
func Status(db *sql.DB, id int64) (string, error) {
	var s sql.NullString
	err := db.QueryRow(`SELECT status FROM proposals WHERE id=?`, id).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return s.String, err
}

// Resolve は提案を applied / rejected にする(チケットは done になる。トリガー)。
func Resolve(db *sql.DB, session string, id int64, status, note string) error {
	_, err := db.Exec(`UPDATE main.proposals SET status=?, note=NULLIF(?,''), resolved_at=? WHERE herdr_session=? AND id=?`, status, note, now(), session, id)
	return err
}
