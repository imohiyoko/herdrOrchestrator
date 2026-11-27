package tickets

// チケットの状態を動かす中身(tb claim / done / block / cancel)。履歴のトリガーと通知は呼ぶ側。

import (
	"database/sql"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// Claim は担当を owner にして active の分類へ動かす(todo の分類で、担当が無いか同じときだけ)。
// session / account は空なら今の値のまま。先に終わらせるタスク(blocks)が残っていれば断る。
func Claim(db *store.DB, st StatusSet, id, owner, session, account, now string) error {
	ok, err := Exists(db.DB, id)
	if err != nil {
		return err
	}
	if !ok {
		return notFound(id)
	}
	if err := SpecialGuard(db.DB, id, "tb claim"); err != nil {
		return err
	}
	closed := st.SQLList("done", "cancelled")
	var blockers sql.NullString
	if err := db.QueryRow(`SELECT group_concat(t.id||'('||t.status||')', ', ') FROM task_links l JOIN tasks t ON t.id=l.from_id
		WHERE l.to_id=? AND l.link_type='blocks' AND t.status NOT IN (`+closed+`)`, id).Scan(&blockers); err != nil {
		return err
	}
	if blockers.String != "" {
		return refuse("claim できません(先に終わらせるタスクがあります: %s): %s", blockers.String, id)
	}
	todo := st.SQLList("todo")
	r, err := db.Exec(`UPDATE main.tasks SET status=?1, owner_agent=?2,
		session_id=COALESCE(NULLIF(?3,''), session_id), account=COALESCE(NULLIF(?4,''), account), updated_at=?5
		WHERE herdr_session=?6 AND id=?7 AND status IN (`+todo+`)
		AND (owner_agent IS NULL OR owner_agent='' OR owner_agent=?2)`, st.Main("active"), owner, session, account, now, db.Session, id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		// 0 件の理由を分ける: todo の分類の状態のまま別の担当に割り当て済みか、状態が todo の分類ではないか
		var cur string
		_ = db.QueryRow(`SELECT COALESCE(owner_agent,'') FROM tasks WHERE herdr_session=? AND id=? AND status IN (`+todo+`)`, db.Session, id).Scan(&cur)
		if cur != "" {
			return refuse("claim できません(%s に割り当て済み。付け替えは tb update %s --owner <名前>。次の候補は tb list で探す): %s", cur, id, id)
		}
		return refuse("claim できません(status が %s ではない): %s", strings.Join(st.In("todo"), " / "), id)
	}
	return nil
}

// Done は成果物と要約を書いて done の分類へ動かす(すでに done の分類なら断る)。
func Done(db *store.DB, st StatusSet, id string, dels []string, summary, now string) error {
	cur, err := StatusOf(db.DB, id)
	if err != nil {
		return err
	}
	if st.Is(cur, "done") {
		return refuse("すでに %s です: %s", cur, id)
	}
	if err := SpecialGuard(db.DB, id, "tb done"); err != nil {
		return err
	}
	r, err := db.Exec(`UPDATE main.tasks SET status=?, deliverables=?, summary=?, reported_at=NULL, updated_at=?
		WHERE herdr_session=? AND id=? AND status NOT IN (`+st.SQLList("done")+`)`, st.Main("done"), StrArray(dels), summary, now, db.Session, id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return refuse("done できません(並行して done になりました): %s", id)
	}
	return nil
}

// Block は blocked の分類へ動かす(理由はメモ。空なら消す)。
func Block(db *store.DB, st StatusSet, id, reason, now string) error {
	if _, err := StatusOf(db.DB, id); err != nil {
		return err
	}
	if err := SpecialGuard(db.DB, id, "tb block"); err != nil {
		return err
	}
	r, err := db.Exec(`UPDATE main.tasks SET status=?, note=NULLIF(?,''), updated_at=? WHERE herdr_session=? AND id=?`,
		st.Main("blocked"), reason, now, db.Session, id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return refuse("block できません: %s", id)
	}
	return nil
}

// Cancel はやらないと決めたタスクを閉じる(理由は要約。完了報告には出さないので報告済みにする)。
func Cancel(db *store.DB, st StatusSet, id, reason, now string) error {
	cur, err := StatusOf(db.DB, id)
	if err != nil {
		return err
	}
	if st.Is(cur, "cancelled") {
		return refuse("すでに %s です: %s", cur, id)
	}
	if err := SpecialGuard(db.DB, id, "tb cancel"); err != nil {
		return err
	}
	r, err := db.Exec(`UPDATE main.tasks SET status=?, summary=NULLIF(?,''), reported_at=?, updated_at=?
		WHERE herdr_session=? AND id=? AND status NOT IN (`+st.SQLList("cancelled")+`)`, st.Main("cancelled"), reason, now, now, db.Session, id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return refuse("cancel できません(並行して取り消されました): %s", id)
	}
	return nil
}
