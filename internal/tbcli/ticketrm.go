package tbcli

// tb ticket rm: チケットを消す(記録が貯まったときの整理)。チケットの行と、そのチケットだけのもの
// (コメント・履歴・リンク)を消す。送信・inbox・PR キューなど、ほかのサービスの記録は残す(ADR 0002 の向き)。

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

const ticketRmUsage = "ticket rm <id> [<id> ...]"

func ticketRm(args []string, w, stderr io.Writer) error {
	var ids []string
	for _, a := range args {
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
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	for _, id := range ids {
		if _, err := taskStatus(db, id); err != nil {
			return err
		}
		if err := specialTicketGuard(db.DB, id, "tb ticket rm"); err != nil {
			return err
		}
		// 子が残ると親を指したままになるので、子も一緒に消すときだけ通す
		var kids []string
		rows, err := db.Query(`SELECT id FROM tasks WHERE parent_id=? ORDER BY id`, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				rows.Close()
				return err
			}
			if !slices.Contains(ids, k) {
				kids = append(kids, k)
			}
		}
		rows.Close()
		if len(kids) > 0 {
			return die("%s には子チケットがあります: %s(子も一緒に tb ticket rm するか、先に tb update で親を外す)", id, strings.Join(kids, " "))
		}
	}
	if err := eventActorResolve(db); err != nil {
		return err
	}
	for _, id := range ids {
		n, err := rmTicket(db, id)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "%s を消しました(コメント %d 件・履歴 %d 件・リンク %d 件)\n", id, n[0], n[1], n[2])
	}
	return nil
}

// rmTicket は 1 枚を 1 つのトランザクションで消す。消した (コメント, 履歴, リンク) の件数。
// リンクを先に消すので、相手のチケットの履歴には unlink が残る(相手から見て何が消えたか分かる)。
func rmTicket(db *store.DB, id string) ([3]int64, error) {
	var n [3]int64
	tx, err := db.Begin()
	if err != nil {
		return n, err
	}
	defer tx.Rollback()
	s := db.Session
	del := func(q string, args ...any) (int64, error) {
		r, err := tx.Exec(q, args...)
		if err != nil {
			return 0, err
		}
		c, _ := r.RowsAffected()
		return c, nil
	}
	if n[2], err = del(`DELETE FROM main.task_links WHERE herdr_session=?1 AND (from_id=?2 OR to_id=?2)`, s, id); err != nil {
		return n, err
	}
	if n[0], err = del(`DELETE FROM main.comments WHERE herdr_session=? AND task_id=?`, s, id); err != nil {
		return n, err
	}
	if n[1], err = del(`DELETE FROM main.task_events WHERE herdr_session=? AND task_id=?`, s, id); err != nil {
		return n, err
	}
	c, err := del(`DELETE FROM main.tasks WHERE herdr_session=? AND id=?`, s, id)
	if err != nil {
		return n, err
	}
	if c != 1 {
		return n, die("ticket rm できません(並行して消されました): %s", id)
	}
	return n, tx.Commit()
}
