package message

// 画面向けの読み取り(Web UI の送信の記録・送信箱・保留した送信)。接続は呼び手の rowfmt.Q。

import (
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/app/rowfmt"
)

// LedgerFilter は送信の記録(messages)の絞り込み。空の項目は絞らない。
type LedgerFilter struct{ Target, From, To, Kind, Result string }

// ledgerWhere は WHERE 句と引数。Result が空で hideFailed なら failed を除く。
func ledgerWhere(f LedgerFilter, hideFailed bool) (string, []any) {
	var w []string
	var a []any
	add := func(cond, val string) {
		if val != "" {
			w = append(w, cond)
			a = append(a, val)
		}
	}
	add("target=?", f.Target)
	add("substr(created_at,1,10)>=?", f.From)
	add("substr(created_at,1,10)<=?", f.To)
	add("kind=?", f.Kind)
	if f.Result != "" {
		add("result=?", f.Result)
	} else if hideFailed {
		w = append(w, "result!='failed'")
	}
	if len(w) == 0 {
		return "", a
	}
	return " WHERE " + strings.Join(w, " AND "), a
}

// LedgerKinds は送信の記録にある種類(名前の順)。
func LedgerKinds(q rowfmt.Q) []rowfmt.Row {
	r, _ := q("SELECT DISTINCT kind FROM messages ORDER BY kind")
	return r
}

// LedgerRows は送信の記録を新しい順に max 件まで。
func LedgerRows(q rowfmt.Q, f LedgerFilter, hideFailed bool, max int) []rowfmt.Row {
	where, a := ledgerWhere(f, hideFailed)
	r, _ := q("SELECT id, kind, ref, target, body, result, created_at FROM messages"+where+" ORDER BY id DESC LIMIT ?", append(a, max)...)
	return r
}

// LedgerSummary は種類と結果ごとの件数(Kind と Result では絞らない)。
func LedgerSummary(q rowfmt.Q, f LedgerFilter) []rowfmt.Row {
	where, a := ledgerWhere(LedgerFilter{Target: f.Target, From: f.From, To: f.To}, false)
	r, _ := q("SELECT kind, result, COUNT(*) AS n FROM messages"+where+" GROUP BY kind, result ORDER BY kind, result", a...)
	return r
}

// OutboxRows は送信箱を新しい順に max 件まで。
func OutboxRows(q rowfmt.Q, max int) []rowfmt.Row {
	r, _ := q("SELECT id, from_name, target, kind, task_id, status, text, created_at FROM outbox ORDER BY id DESC LIMIT ?", max)
	return r
}

const heldCols = "SELECT id, outbox_id, from_name, recipient, kind, task_id, created_at, read_at"

// HeldRows は保留した送信(inbox)をセッション sess で新しい順に max 件まで(本文なし)。
func HeldRows(q rowfmt.Q, sess string, max int) []rowfmt.Row {
	r, _ := q(heldCols+" FROM main.inbox WHERE herdr_session=? ORDER BY id DESC LIMIT ?", sess, max)
	return r
}

// HeldRow は保留した送信 1 件(本文つき)。無ければ nil。
func HeldRow(q rowfmt.Q, sess string, id int64) rowfmt.Row {
	if r, _ := q(heldCols+", text FROM main.inbox WHERE herdr_session=? AND id=?", sess, id); len(r) > 0 {
		return r[0]
	}
	return nil
}
