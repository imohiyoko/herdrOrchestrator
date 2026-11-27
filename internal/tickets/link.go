package tickets

// チケットのリンク(tb link / unlink)とスプリント(tb sprint-add / sprint-start / sprint-close)の中身。

import (
	"context"
	"database/sql"
	"regexp"
	"slices"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// LinkTypes はリンクの種類。
var LinkTypes = []string{"blocks", "relates", "duplicates"}

var (
	reID      = regexp.MustCompile(`^T-[A-Za-z0-9_]+-[0-9]{3,}$`)
	reSprint  = regexp.MustCompile(`^S-[0-9]+$`)
	reProject = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
)

// NeedID は tb の need_id(文言も同じ)。
func NeedID(id string) error {
	if !reID.MatchString(id) {
		return invalid("タスク ID は T-<project>-NNN 形式: %s", id)
	}
	return nil
}

// NeedSprint は need_sprint。
func NeedSprint(id string) error {
	if !reSprint.MatchString(id) {
		return invalid("スプリント ID は S-<番号> 形式: %s", id)
	}
	return nil
}

// NeedProject は need_project。
func NeedProject(p string) error {
	if !reProject.MatchString(p) {
		return invalid("project は英数字と _ のみ: %s", p)
	}
	return nil
}

func requireTask(db *store.DB, id string) error {
	ok, err := Exists(db.DB, id)
	if err != nil {
		return err
	}
	if !ok {
		return notFound(id)
	}
	return nil
}

// LinkCheck は link_args の検査(name は文言に入るコマンド名)。
func LinkCheck(db *store.DB, name, from, typ, to string) error {
	if err := NeedID(from); err != nil {
		return err
	}
	if err := NeedID(to); err != nil {
		return err
	}
	if !slices.Contains(LinkTypes, typ) {
		return invalid("リンクの種類は %s のどれか: %s", strings.Join(LinkTypes, " "), typ)
	}
	if from == to {
		return invalid("自分自身とはリンクできません: %s", from)
	}
	if err := requireTask(db, from); err != nil {
		return err
	}
	return requireTask(db, to)
}

// Link はリンクを足す(by は作った者)。足したら true、既にあれば false。
// blocks は、to から blocks をたどって from に着くなら循環なので断る。検査と挿入は 1 つの書き込みトランザクションで行う。
func Link(db *store.DB, from, typ, to, by, now string) (bool, error) {
	if err := LinkCheck(db, "link", from, typ, to); err != nil {
		return false, err
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	db.RaceHook()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return false, err
	}
	commit := false
	defer func() {
		if !commit {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()
	if typ == "blocks" {
		var cyc int
		err := conn.QueryRowContext(ctx, `SELECT EXISTS (WITH RECURSIVE r(id) AS (SELECT ?1 UNION SELECT l.to_id FROM task_links l JOIN r ON l.from_id=r.id WHERE l.link_type='blocks')
			SELECT 1 FROM r WHERE id=?2)`, to, from).Scan(&cyc)
		if err != nil {
			return false, err
		}
		if cyc != 0 {
			return false, invalid("blocks が循環します: %s → %s", from, to)
		}
	}
	r, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO main.task_links (from_id, link_type, to_id, created_by, created_at, herdr_session)
		VALUES (?, ?, ?, ?, ?, ?)`, from, typ, to, by, now, db.Session)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return false, err
	}
	commit = true
	return n == 1, nil
}

// Unlink はリンクを外す(無ければ断る)。
func Unlink(db *store.DB, from, typ, to string) error {
	if err := LinkCheck(db, "unlink", from, typ, to); err != nil {
		return err
	}
	r, err := db.Exec(`DELETE FROM main.task_links WHERE herdr_session=? AND from_id=? AND link_type=? AND to_id=?`, db.Session, from, typ, to)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return refuse("そのリンクはありません: %s %s %s", from, typ, to)
	}
	return nil
}

// SprintSpec は新しいスプリント(開始・終了は YYYY-MM-DD か空。形は呼ぶ側が確かめる)。
type SprintSpec struct {
	Project, Name, Start, End, Goal string
}

// SprintAdd はスプリントを作る(planned)。番号を返す。
func SprintAdd(db *store.DB, sp SprintSpec, now string) (int64, error) {
	if sp.Project == "" {
		return 0, invalid("sprint-add: -p <project> が必要です")
	}
	if err := NeedProject(sp.Project); err != nil {
		return 0, err
	}
	if strings.TrimFunc(sp.Name, config.IsSpace) == "" {
		return 0, invalid("sprint-add: -n <名前> が必要です")
	}
	if sp.Start != "" && sp.End != "" && sp.End < sp.Start {
		return 0, invalid("sprint-add: --end は --start より後にする")
	}
	r, err := db.Exec(`INSERT INTO main.sprints (project, name, goal, start_date, end_date, created_at, herdr_session)
		VALUES (?, ?, NULLIF(?,''), NULLIF(?,''), NULLIF(?,''), ?, ?)`, sp.Project, sp.Name, sp.Goal, sp.Start, sp.End, now, db.Session)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

// SprintRow は sprint_row: スプリントの state と project。
func SprintRow(db *sql.DB, id string) (state, project string, err error) {
	if err := NeedSprint(id); err != nil {
		return "", "", err
	}
	err = db.QueryRow(`SELECT state, project FROM sprints WHERE id=`+strings.TrimPrefix(id, "S-")).Scan(&state, &project)
	if err == sql.ErrNoRows {
		return "", "", refuse("スプリントが見つかりません: %s", id)
	}
	return state, project, err
}

// SprintStart は planned → active(同じ project の active は 1 つだけ)。
func SprintStart(db *store.DB, id, now string) error {
	st, project, err := SprintRow(db.DB, id)
	if err != nil {
		return err
	}
	if st != "planned" {
		return refuse("planned のスプリントだけ始められます(%s): %s", st, id)
	}
	r, err := db.Exec(`UPDATE main.sprints SET state='active', started_at=?
		WHERE herdr_session=? AND id=`+strings.TrimPrefix(id, "S-")+` AND state='planned'
		AND NOT EXISTS (SELECT 1 FROM sprints WHERE project=? AND state='active')`, now, db.Session, project)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return refuse("project %s には active なスプリントがあります(先に tb sprint-close)", project)
	}
	return nil
}

// SprintForTask は sprint_for_task: project のタスクを入れられるスプリントの番号(閉じたもの・別の project のものは断る)。
func SprintForTask(db *sql.DB, id, project string) (string, error) {
	if err := NeedSprint(id); err != nil {
		return "", err
	}
	n := strings.TrimPrefix(id, "S-")
	var st, p string
	err := db.QueryRow(`SELECT state, project FROM sprints WHERE id=`+n).Scan(&st, &p)
	if err == sql.ErrNoRows {
		return "", refuse("スプリントが見つかりません: %s", id)
	}
	if err != nil {
		return "", err
	}
	if p != project {
		return "", invalid("スプリント %s は project %s のものです(タスクは %s)", id, p, project)
	}
	if st == "closed" {
		return "", invalid("閉じたスプリントには入れられません: %s", id)
	}
	return n, nil
}

// SprintClose はスプリントを閉じ、終わっていないタスクを carry(空ならバックログ)へ移す。移した件数を返す。
func SprintClose(db *store.DB, sts StatusSet, id, carry, now string) (int64, error) {
	st, project, err := SprintRow(db.DB, id)
	if err != nil {
		return 0, err
	}
	if st == "closed" {
		return 0, refuse("すでに閉じています: %s", id)
	}
	to := "NULL"
	if carry != "" {
		if carry == id {
			return 0, invalid("sprint-close: 同じスプリントには移せません")
		}
		if to, err = SprintForTask(db.DB, carry, project); err != nil {
			return 0, err
		}
	}
	n := strings.TrimPrefix(id, "S-")
	// 閉じる側・移す先が閉じていないことを書く時点で確かめる(並行した close と競っても二重に動かさない)
	ok := "EXISTS (SELECT 1 FROM main.sprints WHERE herdr_session=?1 AND id=" + n + " AND state!='closed')"
	if to != "NULL" {
		ok += " AND EXISTS (SELECT 1 FROM main.sprints WHERE herdr_session=?1 AND id=" + to + " AND state!='closed')"
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	db.RaceHook()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return 0, err
	}
	commit := false
	defer func() {
		if !commit {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()
	r, err := conn.ExecContext(ctx, "UPDATE main.tasks SET sprint_id="+to+", updated_at=?2 WHERE herdr_session=?1 AND sprint_id="+n+
		" AND status NOT IN ("+sts.SQLList("done", "cancelled")+") AND "+ok, db.Session, now)
	if err != nil {
		return 0, err
	}
	moved, _ := r.RowsAffected()
	r, err = conn.ExecContext(ctx, "UPDATE main.sprints SET state='closed', closed_at=?2 WHERE herdr_session=?1 AND id="+n+" AND "+ok, db.Session, now)
	if err != nil {
		return 0, err
	}
	closed, _ := r.RowsAffected()
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return 0, err
	}
	commit = true
	if closed != 1 {
		dest := carry
		if dest == "" {
			dest = "移す先"
		}
		return 0, refuse("sprint-close: %s か %s が並行して閉じられました", id, dest)
	}
	return moved, nil
}
