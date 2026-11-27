package digest

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// tb の読み取りの小さな部品(sqlite3 -json / jq の値の写し)。

var reDigits = regexp.MustCompile(`^[0-9]+$`)

func nowTS() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func or(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// itemText は item_text: 1 行にして [ ] | を ( ) / に。
func itemText(s string, n int) string {
	return strings.NewReplacer("[", "(", "]", ")", "|", "/").Replace(util.Oneline(s, n))
}

// jqStr は jq の文字列埋め込み \(.x) の値(null は "null")。
func jqStr(v any) string {
	if v == nil {
		return "null"
	}
	return jv.Scalar(v)
}

// objStr は .x // "" の値。
func objStr(o jv.Obj, k string) string {
	v := o.Get(k)
	if v == nil || v == false {
		return ""
	}
	return jv.Scalar(v)
}

// jvValue は sqlite3 -json の 1 つの値。
func jvValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case int64:
		return jv.Number(tickets.Text(x))
	case float64:
		return jv.Number(tickets.Text(x))
	default:
		return tickets.Text(v)
	}
}

func sqlText(v any) string {
	if v == nil {
		return ""
	}
	return jv.Scalar(jvValue(v))
}

// sqlObjs は sql -json の行(列の順のまま)。fromjson の列は JSON として読む。
func sqlObjs(db *store.DB, fromjson []string, q string, args ...any) ([]jv.Obj, error) {
	var out []jv.Obj
	err := store.Each(db.DB, q, args, func(cols []string, vals []any) error {
		o := jv.Obj{}
		for i, c := range cols {
			v := jvValue(vals[i])
			if slices.Contains(fromjson, c) {
				s, _ := v.(string)
				vs, err := jv.DecodeAll([]byte(s))
				if err != nil || len(vs) != 1 {
					return fmt.Errorf("jq: error: %s を JSON として読めません", c)
				}
				v = vs[0]
			}
			o = append(o, jv.KV{K: c, V: v})
		}
		out = append(out, o)
		return nil
	})
	return out, err
}
