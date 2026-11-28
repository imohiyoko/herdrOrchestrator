package tbcli

import (
	"github.com/imohiyoko/herdrorchestrator/internal/app/rowfmt"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
)

// jqString は jq -c と同じ書き方の JSON 文字列(util.JQString)。
func jqString(s string) string { return util.JQString(s) }

// fromJSON / rowJSON は rowfmt と同じ(tb と API で出力の形を 1 つにする)。
func fromJSON(s string) (string, error)                 { return rowfmt.FromJSON(s) }
func rowJSON(cols []string, vals []any) (string, error) { return rowfmt.RowJSON(cols, vals) }
