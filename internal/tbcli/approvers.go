package tbcli

// 承認待ち(Q-<n>)を決められる agent(policy の <承認者> approve:<操作> <依頼者> = allow に当たる agent)を見つける。
// tb approval list --mine の絞り込みと、承認待ちができたときの承認者への知らせに使う。

import (
	"database/sql"
	"io"

	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/transport"
)

// decideAct は承認の権限の行(approve:<act> <依頼者>)に使う操作名。空なら人だけが決められる。
// 送信とコマンドは r.act()。agent の操作は承認の入口(tb agent-approve)と同じ agentOpAction で決める。deny の記録は人だけ。
func decideAct(db *store.DB, r request) string {
	if r.status == "denied" {
		return ""
	}
	if r.source == "agent_ops" {
		o, ok := opRowOf(db, r.sourceID.Int64)
		if !ok {
			return ""
		}
		return agentOpAction(db, o)
	}
	return r.act()
}

// canDecide は agent who が承認待ち r を決められるか(自分の依頼は決められない。人は常に決められる)。
func canDecide(db *store.DB, j *approval.Judge, r request, who string) bool {
	if who == "human" {
		return true
	}
	if who == "" || who == r.requester {
		return false
	}
	act := decideAct(db, r)
	if act == "" {
		return false
	}
	_, ok := j.GrantOf(who, "approve:"+act, r.requester)
	return ok
}

// notifyApprovers は承認待ち Q-<n> ができたことを、それを決められる agent の pane に 1 通ずつ知らせる(tb からの知らせ。送信の経路は通さない)。
// 同じ Q-<n> を 2 度知らせない(messages の kind=approval-pending, ref=Q-<n> で見る)。使用中・不在の agent には送らない(tb approval list --mine で見られる)。
// 本文は依頼者・要る権限・対象だけ(送る文面やコマンドの引数は載せない)。
func notifyApprovers(sdb *sql.DB, session string, qid int64) {
	db := &store.DB{DB: sdb, Session: session}
	r, ok, err := loadRequest(sdb, qid)
	if err != nil || !ok {
		return
	}
	var n int
	if sdb.QueryRow(`SELECT count(*) FROM messages WHERE kind='approval-pending' AND ref=?`, r.qid()).Scan(&n) != nil || n > 0 {
		return
	}
	as, err := herdr.Agents()
	if err != nil {
		return
	}
	j := approval.NewJudge(sdb, session)
	for _, a := range as {
		if a.Name == "" || !canDecide(db, j, r, a.Name) {
			continue
		}
		msg := "[tb approval / " + r.qid() + "] " + r.requester + " からの承認待ちです(" + r.op + " " + r.target + ")。tb approval approve " + r.qid() +
			" か tb approval reject " + r.qid() + " で決めてください(自分が決められる一覧: tb approval list --mine)"
		transport.Send(sdb, session, a.Name, msg, r.qid(), "approval-pending", io.Discard)
	}
}
