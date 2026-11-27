package tbcli

import (
	"fmt"
	"io"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// tb compact-check は app/agent の agent.compact.check を呼ぶ(文脈が大きくなった待機中の agent に /compact を送る)。

// recordOp は record_op(新しい行): agent_ops に結果を 1 行足す。
func recordOp(db *store.DB, op, target, args, by string, rc int, result string) {
	agents.RecordOp(db.DB, db.Session, op, target, args, by, rc, result)
}

func cmdCompactCheck(args []string, w, stderr io.Writer) error {
	apply := false
	for _, a := range args {
		switch a {
		case "--dry-run":
			apply = false
		case "--apply":
			apply = true
		default:
			return usage("compact-check: 不明な引数 %s", a)
		}
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	by, err := requesterPrincipal()
	if err != nil {
		return err
	}
	var r agent.CompactResult
	if err := callAppEnv(appEnv(db, stderr), by, "agent.compact.check", agent.CompactReq{Apply: apply}, &r); err != nil {
		return err
	}
	if r.Pending != nil {
		return pendingExit(w, r.Pending)
	}
	for _, l := range r.Lines {
		fmt.Fprintln(w, l)
	}
	return nil
}
