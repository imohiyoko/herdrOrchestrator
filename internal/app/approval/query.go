package approval

// 画面向けの読み取り(Web UI の承認の記録)。接続は呼び手の rowfmt.Q。

import (
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/app/rowfmt"
)

// statusSQL は Q-<n> の決まり方(pending / approved / rejected / running / done / failed / allowed / denied)。
// 送信・agent の操作は、元の行(outbox・agent_ops)がまだ承認待ちなら pending。決めた記録が Q にあればそれ(tb が承認・却下のときに残す)、
// 無い古い行は元の行から読む(却下なら rejected、それ以外は approved)。届いたか・実行できたかは resultSQL。
const statusSQL = `CASE r.source
  WHEN 'outbox' THEN CASE COALESCE((SELECT o.status FROM outbox o WHERE o.id=r.source_id), '')
    WHEN 'pending' THEN 'pending' WHEN '' THEN r.status
    ELSE CASE WHEN r.status IN ('approved','rejected') THEN r.status
      WHEN (SELECT o.status FROM outbox o WHERE o.id=r.source_id)='denied' THEN 'rejected' ELSE 'approved' END END
  WHEN 'agent_ops' THEN CASE COALESCE((SELECT a.status FROM agent_ops a WHERE a.id=r.source_id), '')
    WHEN 'pending' THEN 'pending' WHEN '' THEN r.status
    ELSE CASE WHEN r.status IN ('approved','rejected') THEN r.status
      WHEN (SELECT a.status FROM agent_ops a WHERE a.id=r.source_id)='rejected' THEN 'rejected' ELSE 'approved' END END
  ELSE r.status END`

// resultSQL は承認のあとどうなったか(送信: queued / sent / denied(届けられなかった)、agent の操作: done / failed)。
// 却下されたもの・まだ決まっていないもの・コマンド(状態がそのまま結果)は空。
const resultSQL = `CASE r.source
  WHEN 'outbox' THEN COALESCE((SELECT o.status FROM outbox o WHERE o.id=r.source_id AND o.status<>'pending'
    AND (o.status<>'denied' OR r.status='approved')), '')
  WHEN 'agent_ops' THEN COALESCE((SELECT a.status FROM agent_ops a WHERE a.id=r.source_id AND a.status NOT IN ('pending','rejected')), '')
  ELSE '' END`

// Filter は承認の記録(Q-<n>)の一覧の絞り込み。Tier が空なら allow を除く(all はすべて)。
type Filter struct {
	Tier, Status, Requester, By, ID string
}

// where は絞り込みの条件。tier が空なら allow を除く(allow は tb を叩くたびに残るので既定では畳む)。
func (f Filter) where(withStatus bool) (string, []any) {
	var w []string
	var a []any
	switch f.Tier {
	case "":
		w = append(w, "r.tier<>'allow'")
	case "all":
	default:
		w = append(w, "r.tier=?")
		a = append(a, f.Tier)
	}
	if f.Requester != "" {
		w = append(w, "r.requester=?")
		a = append(a, f.Requester)
	}
	if f.By != "" {
		w = append(w, "COALESCE(r.decided_by,'')=?")
		a = append(a, f.By)
	}
	if f.ID != "" {
		w = append(w, "r.id=?")
		a = append(a, f.ID)
	}
	if withStatus && f.Status != "" {
		w = append(w, "("+statusSQL+")=?")
		a = append(a, f.Status)
	}
	if len(w) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(w, " AND "), a
}

// TierCounts は判定(tier)ごとの件数(列は tier・n。Tier では絞らない)。
func TierCounts(q rowfmt.Q, f Filter) []rowfmt.Row {
	f.Tier = "all"
	w, a := f.where(true)
	r, _ := q("SELECT r.tier, COUNT(*) AS n FROM requests r"+w+" GROUP BY r.tier", a...)
	return r
}

// StatusCounts は状態ごとの件数(列は st・n。Status では絞らない)。
func StatusCounts(q rowfmt.Q, f Filter) []rowfmt.Row {
	w, a := f.where(false)
	r, _ := q("SELECT "+statusSQL+" AS st, COUNT(*) AS n FROM requests r"+w+" GROUP BY st", a...)
	return r
}

// Rows は承認の記録を新しい順に max 件まで(状態 status と、承認のあとどうなったか outcome つき)。
func Rows(q rowfmt.Q, f Filter, max int) []rowfmt.Row {
	w, a := f.where(true)
	r, _ := q("SELECT r.id, r.source, r.source_id, r.tier, "+statusSQL+" AS status, r.requester, r.op, r.target, r.priority, "+
		"COALESCE(r.decided_by,'') AS decided_by, COALESCE(r.rule,'') AS rule, r.created_at, COALESCE(r.decided_at,'') AS decided_at, "+
		"r.detail, COALESCE(r.result,'') AS result, "+resultSQL+" AS outcome FROM requests r"+w+" ORDER BY r.id DESC LIMIT ?", append(a, max)...)
	return r
}

// StatusOf は Q-<id> の今の状態(無ければ空)。
func StatusOf(q rowfmt.Q, id string) string {
	if r, _ := q("SELECT "+statusSQL+" AS st FROM requests r WHERE r.id=?", id); len(r) > 0 {
		if s, ok := r[0]["st"].(string); ok {
			return s
		}
	}
	return ""
}

// Changes は承認の規則の変更の記録を新しい順に limit 件まで。
func Changes(q rowfmt.Q, limit int) []rowfmt.Row {
	r, _ := q("SELECT id, pattern, value, created_at FROM approval_changes ORDER BY id DESC LIMIT ?", limit)
	return r
}

// PendingRows はまだ決まっていない承認待ち(Q-<n>。tb approval list と同じ条件)を、優先度の高い順・同じなら古い順に limit 件まで。
func PendingRows(q rowfmt.Q, limit int) []rowfmt.Row {
	r, _ := q("SELECT r.id, r.source, r.source_id, r.requester, r.op, r.target, r.priority, r.detail, r.created_at FROM requests r WHERE "+
		reqPending+" ORDER BY r.priority DESC, r.id LIMIT ?", limit)
	return r
}

// RequestOf は元の行(outbox・agent_ops の id)の承認待ちの番号の行(列は id。無ければ空)。
func RequestOf(q rowfmt.Q, source string, id any) []rowfmt.Row {
	r, _ := q("SELECT id FROM requests WHERE source=? AND source_id=?", source, id)
	return r
}
