package conv

import "github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"

type (
	kv  = jv.KV
	obj = jv.Obj
)

func decodeAll(b []byte) ([]any, error) { return jv.DecodeAll(b) }
func num(v any) (float64, bool)         { return jv.Num(v) }
func scalar(v any) string               { return jv.Scalar(v) }
func compact(v any) string              { return jv.Compact(v) }
func pretty(v any) string               { return jv.Pretty(v) }
func tsv(vs ...any) string              { return jv.TSV(vs...) }
func jqLess(a, b any) int               { return jv.Less(a, b) }
func jnum(s string) any                 { return jv.Number(s) }
