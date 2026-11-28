package alert

// 画面向けの読み取り(Web UI の成果物)。接続は呼び手の rowfmt.Q。

import "github.com/imohiyoko/herdrorchestrator/internal/app/rowfmt"

const docCols = "SELECT id,title,result_path,result_report,result_by,analyzed_at FROM alerts"

// Doc はアラート id の解析の結果の行(無ければ nil)。
func Doc(q rowfmt.Q, id int) rowfmt.Row {
	if r, _ := q(docCols+" WHERE id=?", id); len(r) > 0 {
		return r[0]
	}
	return nil
}

// Docs は解析の結果のあるアラートを新しい順に n 件まで。
func Docs(q rowfmt.Q, n int) []rowfmt.Row {
	r, _ := q(docCols+" WHERE result_path IS NOT NULL OR result_report IS NOT NULL ORDER BY id DESC LIMIT ?", n)
	return r
}

// Analyzed は解析の終わった重大度 critical / high のアラートを新しい順に 50 件まで。
func Analyzed(q rowfmt.Q) []rowfmt.Row {
	r, _ := q("SELECT * FROM alerts WHERE status='analyzed' AND severity IN ('critical','high') ORDER BY id DESC LIMIT 50")
	return r
}

// AwaitingApproval は解析役の起動待ちのアラートを新しい順に 50 件まで。
func AwaitingApproval(q rowfmt.Q) []rowfmt.Row {
	r, _ := q("SELECT * FROM alerts WHERE status='awaiting-approval' ORDER BY id DESC LIMIT 50")
	return r
}
