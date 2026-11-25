package tbcli

// tb ticket rm: チケットを消す(記録が貯まったときの整理)。チケットの行と、そのチケットだけのもの
// (コメント・履歴・リンク)を消す。送信・inbox・PR キューなど、ほかのサービスの記録は残す(ADR 0002 の向き)。

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

const ticketRmUsage = "ticket rm <id> [<id> ...] [--if-created <作成時刻>]"

func ticketRm(args []string, w, stderr io.Writer) error {
	var ids []string
	ifCreated, hasIf := "", false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--if-created" {
			v, err := argv(args, i)
			if err != nil {
				return err
			}
			i++
			ifCreated, hasIf = v, true
			continue
		}
		if strings.HasPrefix(a, "-") {
			return usage("ticket rm: 不明な引数 %s(%s)", a, ticketRmUsage)
		}
		if err := needID(a); err != nil {
			return err
		}
		if !slices.Contains(ids, a) {
			ids = append(ids, a)
		}
	}
	if len(ids) == 0 {
		return usage(ticketRmUsage)
	}
	if hasIf && len(ids) != 1 {
		return usage("ticket rm: --if-created はチケット 1 枚のときだけ使えます")
	}
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := eventActorResolve(db); err != nil {
		return err
	}
	counts, err := rmTickets(db, ids, ifCreated, hasIf)
	if err != nil {
		return err
	}
	for i, id := range ids {
		n := counts[i]
		fmt.Fprintf(w, "%s を消しました(コメント %d 件・履歴 %d 件・リンク %d 件)\n", id, n[0], n[1], n[2])
	}
	return nil
}

// rmTickets は、確かめてから消すまでを 1 つの書き込みトランザクションで行う(どれか 1 枚でも断れば、何も消さない)。
// 最初に書き込みのロックを取るので、確かめたあとに子チケットが足されることはない。
// ifCreated は、消した ID がまた使われたあとの古い画面から別のチケットを消さないための確かめ(Web UI が渡す)。
// 消した (コメント, 履歴, リンク) の件数を ids の順に返す。リンクを先に消すので、相手のチケットの履歴には unlink が残る。
func rmTickets(db *store.DB, ids []string, ifCreated string, hasIf bool) ([][3]int64, error) {
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
				return nil, die("タスクが見つかりません: %s", id)
			}
			return nil, err
		}
		if hasIf && created != ifCreated {
			return nil, die("%s は画面を開いたときのチケットではありません(作成 %s。消した ID がまた使われた)。画面を開き直してください", id, created)
		}
		if err := specialTicketGuard(tx, id, "tb ticket rm"); err != nil {
			return nil, err
		}
		// 子が残ると親を指したままになるので、子も一緒に消すときだけ通す
		kids, err := childIDs(tx, s, id)
		if err != nil {
			return nil, err
		}
		kids = slices.DeleteFunc(kids, func(k string) bool { return slices.Contains(ids, k) })
		if len(kids) > 0 {
			return nil, die("%s には子チケットがあります: %s(子も一緒に tb ticket rm するか、先に tb update で親を外す)", id, strings.Join(kids, " "))
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
	out := make([][3]int64, len(ids))
	for i, id := range ids {
		if out[i][2], err = del(`DELETE FROM main.task_links WHERE herdr_session=?1 AND (from_id=?2 OR to_id=?2)`, s, id); err != nil {
			return nil, err
		}
		if out[i][0], err = del(`DELETE FROM main.comments WHERE herdr_session=? AND task_id=?`, s, id); err != nil {
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
			projects[i], id, now(), s); err != nil {
			return nil, err
		}
	}
	// 履歴は最後に消す(一緒に消すチケットどうしのリンクを消したときの unlink も残さない)
	for i, id := range ids {
		if out[i][1], err = del(`DELETE FROM main.task_events WHERE herdr_session=? AND task_id=?`, s, id); err != nil {
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
