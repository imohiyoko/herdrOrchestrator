package tickets

// チケットの書き込みの中身(tb ticket move / ticket rm)。tb の入口とアプリケーション API(internal/app/ticket)が使う。
// 履歴(task_events)のトリガーは呼ぶ側が先に作っておく(SetupEvents)。

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// Refused は、確かめた結果で断った(tb は文言を出して 1 で終わる)。NotFound はチケットが無かったとき。
type Refused struct {
	Msg      string
	NotFound bool
}

func (e *Refused) Error() string { return e.Msg }

func refuse(format string, a ...any) error { return &Refused{Msg: fmt.Sprintf(format, a...)} }

func notFound(id string) error {
	return &Refused{Msg: "タスクが見つかりません: " + id, NotFound: true}
}

// RowQuerier は *sql.DB と *sql.Tx(トランザクションの中でも確かめられるように)。
type RowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

// SpecialGuard は special_ticket_guard: アラート・提案のチケットの状態は、それぞれのコマンドだけが変える。
// op は断るときの文言に入るコマンド("tb ticket move" など)。
func SpecialGuard(db RowQuerier, id, op string) error {
	var w string
	err := db.QueryRow(`SELECT 'A-'||alert_no FROM alert_details WHERE task_id=?1 UNION ALL SELECT 'P-'||proposal_no FROM proposal_details WHERE task_id=?1 LIMIT 1`, id).Scan(&w)
	if err != nil {
		return nil // 行が無い・表が無い(古い DB)
	}
	switch {
	case strings.HasPrefix(w, "A-"):
		return refuse("%s はアラート %s のチケットです。状態は tb alert-approve / alert-result / alert-close %s で変えます(閉じるのは人だけ。%s では変えません)", id, w, w, op)
	case strings.HasPrefix(w, "P-"):
		return refuse("%s は提案 %s のチケットです。状態は tb proposal resolve %s で変えます(%s では変えません)", id, w, w, op)
	}
	return nil
}

// StatusOf はチケットの今の状態(無ければ Refused の NotFound)。
func StatusOf(db RowQuerier, id string) (string, error) {
	var st string
	err := db.QueryRow(`SELECT status FROM tasks WHERE id=?`, id).Scan(&st)
	if err == sql.ErrNoRows {
		return "", notFound(id)
	}
	return st, err
}

// EnterSets は status_enter_sets: 状態の分類が変わるときに一緒に書く列(done に入れば報告前に戻し、cancelled に入れば報告済みにする)。
func EnterSets(st StatusSet, to, cur, now string) string {
	if st.Is(to, st.Cat(cur)) {
		return ""
	}
	if st.Is(to, "done") {
		return "reported_at=NULL"
	}
	if st.Is(to, "cancelled") {
		return "reported_at=" + store.Quote(now)
	}
	return ""
}

// StrArray は文字列の配列の JSON(成果物の列)。
func StrArray(ss []string) string {
	a := make([]any, len(ss))
	for i, s := range ss {
		a[i] = s
	}
	return jv.Compact(a)
}

// Move は ticket move の中身: id の状態を to にする。
// msg は hasMsg のときだけ書く(閉じた分類なら要約、ほかはメモ。空なら消す)。dels は done の分類へ動かすときの成果物。
// 何も変わらないとき(同じ状態でメモも成果物も無い)は changed=false。
// 入力の形(状態の名前・-d の使い方)は呼ぶ側が確かめてある。
func Move(db *store.DB, st StatusSet, id, to, msg string, hasMsg bool, dels []string, now string) (cur string, changed bool, err error) {
	cur, err = StatusOf(db.DB, id)
	if err != nil {
		return "", false, err
	}
	if err := SpecialGuard(db.DB, id, "tb ticket move"); err != nil {
		return cur, false, err
	}
	if cur == to && !hasMsg && len(dels) == 0 {
		return cur, false, nil
	}
	q := store.Quote
	sets := []string{"status=" + q(to)}
	if es := EnterSets(st, to, cur, now); es != "" {
		sets = append(sets, es)
	}
	if hasMsg {
		col := "note"
		if st.Is(to, "done", "cancelled") {
			col = "summary"
		}
		sets = append(sets, col+"=NULLIF("+q(msg)+",'')")
	}
	if len(dels) > 0 {
		sets = append(sets, "deliverables="+q(StrArray(dels)))
	}
	r, err := db.Exec("UPDATE main.tasks SET "+strings.Join(sets, ", ")+", updated_at=? WHERE herdr_session=? AND id=? AND status=?", now, db.Session, id, cur)
	if err != nil {
		return cur, false, err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return cur, false, refuse("ticket move できません(並行して状態が変わりました): %s", id)
	}
	return cur, true, nil
}

// Removed は 1 枚のチケットを消したときに一緒に消したものの件数。
type Removed struct {
	Comments, Events, Links int64
}

// Remove は ticket rm の中身: 確かめてから消すまでを 1 つの書き込みトランザクションで行う(どれか 1 枚でも断れば、何も消さない)。
// 最初に書き込みのロックを取るので、確かめたあとに子チケットが足されることはない。
// ifCreated は、消した ID がまた使われたあとの古い画面から別のチケットを消さないための確かめ(Web UI が渡す。hasIf のときだけ見る)。
// 消した件数を ids の順に返す。リンクを先に消すので、相手のチケットの履歴には unlink が残る。
func Remove(db *store.DB, ids []string, ifCreated string, hasIf bool, now string) ([]Removed, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	s := db.Session
	if _, err := tx.Exec(`UPDATE main.tasks SET id=id WHERE 0`); err != nil { // 書き込みのロックを先に取る
		return nil, err
	}
	projects := make([]string, len(ids))
	for i, id := range ids {
		var created string
		if err := tx.QueryRow(`SELECT created_at, project FROM main.tasks WHERE herdr_session=? AND id=?`, s, id).Scan(&created, &projects[i]); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, notFound(id)
			}
			return nil, err
		}
		if hasIf && created != ifCreated {
			return nil, refuse("%s は画面を開いたときのチケットではありません(作成 %s。消した ID がまた使われた)。画面を開き直してください", id, created)
		}
		if err := SpecialGuard(tx, id, "tb ticket rm"); err != nil {
			return nil, err
		}
		// 子が残ると親を指したままになるので、子も一緒に消すときだけ通す
		kids, err := childIDs(tx, s, id)
		if err != nil {
			return nil, err
		}
		kids = slices.DeleteFunc(kids, func(k string) bool { return slices.Contains(ids, k) })
		if len(kids) > 0 {
			return nil, refuse("%s には子チケットがあります: %s(子も一緒に tb ticket rm するか、先に tb update で親を外す)", id, strings.Join(kids, " "))
		}
	}
	del := func(q string, args ...any) (int64, error) {
		r, err := tx.Exec(q, args...)
		if err != nil {
			return 0, err
		}
		c, _ := r.RowsAffected()
		return c, nil
	}
	out := make([]Removed, len(ids))
	for i, id := range ids {
		if out[i].Links, err = del(`DELETE FROM main.task_links WHERE herdr_session=?1 AND (from_id=?2 OR to_id=?2)`, s, id); err != nil {
			return nil, err
		}
		if out[i].Comments, err = del(`DELETE FROM main.comments WHERE herdr_session=? AND task_id=?`, s, id); err != nil {
			return nil, err
		}
		if _, err = del(`DELETE FROM main.tasks WHERE herdr_session=? AND id=?`, s, id); err != nil {
			return nil, err
		}
	}
	// 消した番号は使い直さない(tb add は「残っている最大の番号」と、ここに残した「消した最大の番号」の大きいほう + 1 を振る)。
	// 番号は全セッションで 1 つなので、tb add はどのセッションの行も見る(書くのはこのセッションの行)
	for i, id := range ids {
		if _, err := tx.Exec(`INSERT INTO main.tb_flags (herdr_session, key, value, set_by, set_at) VALUES (?4, 'ticket-rm-max:'||?1, CAST(substr(?2, length(?1)+4) AS INTEGER), 'tb ticket rm', ?3)
			ON CONFLICT (herdr_session, key) DO UPDATE SET value=MAX(CAST(value AS INTEGER), CAST(excluded.value AS INTEGER)), set_by=excluded.set_by, set_at=excluded.set_at`,
			projects[i], id, now, s); err != nil {
			return nil, err
		}
	}
	// 履歴は最後に消す(一緒に消すチケットどうしのリンクを消したときの unlink も残さない)
	for i, id := range ids {
		if out[i].Events, err = del(`DELETE FROM main.task_events WHERE herdr_session=? AND task_id=?`, s, id); err != nil {
			return nil, err
		}
	}
	return out, tx.Commit()
}

func childIDs(tx *sql.Tx, session, id string) ([]string, error) {
	rows, err := tx.Query(`SELECT id FROM main.tasks WHERE herdr_session=? AND parent_id=? ORDER BY id`, session, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
