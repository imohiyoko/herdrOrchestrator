package tbcli

// tb ticket rm: チケットを消す(記録が貯まったときの整理)。中身はアプリケーション API の ticket.rm(ADR 0008)。チケットの行と、そのチケットだけのもの
// (コメント・履歴・リンク)を消す。送信・inbox・PR キューなど、ほかのサービスの記録は残す(ADR 0002 の向き)。

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/app/ticket"
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
	var res ticket.RmResult
	if err := callApp(db, "tb ticket rm", "ticket.rm", ticket.RmReq{IDs: ids, IfCreated: ifCreated, HasIfCreated: hasIf}, &res); err != nil {
		return err
	}
	if res.Pending != nil {
		return pendingExit(w, res.Pending)
	}
	for _, n := range res.Removed {
		fmt.Fprintf(w, "%s を消しました(コメント %d 件・履歴 %d 件・リンク %d 件)\n", n.ID, n.Comments, n.Events, n.Links)
	}
	return nil
}
