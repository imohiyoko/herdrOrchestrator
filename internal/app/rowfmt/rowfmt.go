// Package rowfmt は DB の行を tb の出力の形にする(sqlite3 -separator \t と jq -c の形)。tb と API の両方が使う。
package rowfmt

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// Value は sqlite3 -json の 1 つの値(数は jv.Number、NULL は nil、ほかは文字列)。
func Value(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case int64, float64:
		return jv.Number(tickets.Text(x))
	default:
		return tickets.Text(v)
	}
}

// Objs は sqlite3 -json の行(列の順のまま)。
func Objs(db *sql.DB, q string, args ...any) ([]jv.Obj, error) {
	return ObjsJSON(db, nil, q, args...)
}

// ObjsJSON は Objs で、fromjson の列は中身の JSON を読んだ値にする(jq の fromjson)。
func ObjsJSON(db *sql.DB, fromjson []string, q string, args ...any) ([]jv.Obj, error) {
	var out []jv.Obj
	err := store.Each(db, q, args, func(cols []string, vals []any) error {
		o := jv.Obj{}
		for i, c := range cols {
			v := Value(vals[i])
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

// jqValue は JSON の値を jq -c と同じ形に書き直す(キーの順は保つ)。
func jqValue(dec *json.Decoder, b *strings.Builder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch t := tok.(type) {
	case json.Delim:
		if t == '[' {
			b.WriteByte('[')
			for i := 0; dec.More(); i++ {
				if i > 0 {
					b.WriteByte(',')
				}
				if err := jqValue(dec, b); err != nil {
					return err
				}
			}
			if _, err := dec.Token(); err != nil { // 閉じ括弧が無い(途中で切れた)
				return err
			}
			b.WriteByte(']')
			return nil
		}
		b.WriteByte('{')
		for i := 0; dec.More(); i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			k, err := dec.Token()
			if err != nil {
				return err
			}
			b.WriteString(util.JQString(k.(string)))
			b.WriteByte(':')
			if err := jqValue(dec, b); err != nil {
				return err
			}
		}
		if _, err := dec.Token(); err != nil {
			return err
		}
		b.WriteByte('}')
	case string:
		b.WriteString(util.JQString(t))
	case json.Number:
		b.WriteString(JQNumber(t))
	case bool:
		b.WriteString(strconv.FormatBool(t))
	case nil:
		b.WriteString("null")
	}
	return nil
}

// JQNumber は json.Number を jq -c と同じ書き方にする。
func JQNumber(n json.Number) string {
	if i, err := strconv.ParseInt(string(n), 10, 64); err == nil {
		return strconv.FormatInt(i, 10)
	}
	f, err := n.Float64()
	if err != nil {
		return string(n)
	}
	if f == float64(int64(f)) && f < 1e17 && f > -1e17 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', 17, 64)
}

// FromJSON は列の JSON 文字列を jq の fromjson と同じく値に戻して書く。
func FromJSON(s string) (string, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(s)))
	dec.UseNumber()
	var b strings.Builder
	if err := jqValue(dec, &b); err != nil {
		return "", fmt.Errorf("JSON として読めません: %w", err)
	}
	if dec.More() {
		return "", fmt.Errorf("JSON の後に余分なものがあります")
	}
	return b.String(), nil
}

// JSONFields は fromjson する列(tasks.sh の list --json と同じ)
var JSONFields = map[string]bool{"deliverables": true, "labels": true, "refs": true}

// RowJSON は 1 行を jq -c と同じ 1 行の JSON にする(列の順を保つ)。
func RowJSON(cols []string, vals []any) (string, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, c := range cols {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(util.JQString(c))
		b.WriteByte(':')
		v := vals[i]
		switch {
		case v == nil:
			if JSONFields[c] {
				return "", fmt.Errorf("%s が NULL です", c)
			}
			b.WriteString("null")
		case JSONFields[c]:
			s, err := FromJSON(tickets.Text(v))
			if err != nil {
				return "", fmt.Errorf("%s: %w", c, err)
			}
			b.WriteString(s)
		default:
			switch x := v.(type) {
			case int64:
				b.WriteString(strconv.FormatInt(x, 10))
			case float64:
				b.WriteString(JQNumber(json.Number(strconv.FormatFloat(x, 'g', 17, 64))))
			default:
				b.WriteString(util.JQString(tickets.Text(v)))
			}
		}
	}
	b.WriteByte('}')
	return b.String(), nil
}

// EmitRow は 1 行を js なら jq -c の形、でなければ sqlite3 の -separator \t の形(NULL は null の文字)で書く。
func EmitRow(w io.Writer, js bool, cols []string, vals []any, null string) error {
	if js {
		s, err := RowJSON(cols, vals)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, s)
		return err
	}
	parts := make([]string, len(vals))
	for i, v := range vals {
		if v == nil {
			parts[i] = null
		} else {
			parts[i] = tickets.Text(v)
		}
	}
	_, err := fmt.Fprintln(w, strings.Join(parts, "\t"))
	return err
}

// Row は読み取りの 1 行(列名 → 値。値は DB の値のまま: int64 / float64 / string / []byte / nil)。
type Row = map[string]any

// Q は読むだけの接続で SQL を流す口。照会できなければ ok=false(DB が無い・表が無いなど)。
// Web UI はセッションの一時ビューの付いた読み取り専用の接続で、SQL の中の __<分類>_STATUSES__ を設定 statuses で埋めて流す。
// 画面向けの読み取り(app/<サービス> の query.go)はこれを受けて SQL を書く(ADR 0008「人が決めること」の案: 読み取りは別ファイル)。
type Q func(q string, args ...any) ([]Row, bool)

// EmptyIn は IN (...) の中身が無いときに入れるもの(どの値にも合わない)。
const EmptyIn = "SELECT NULL WHERE 0"

var reTableName = regexp.MustCompile(`^[a-z_]{1,64}$`)

func nameSet(names []string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		if reTableName.MatchString(n) {
			m[n] = true
		}
	}
	return m
}

// OpenSessionRO は DB を読み取り専用で開き、セッション sess の行だけの一時ビューを作る(表とビューの名前はそのまま)。
// 接続は 1 本だけ(一時ビューは接続ごと)。開けなければ nil。
func OpenSessionRO(path, sess string, busyMS int) *sql.DB {
	db, err := store.OpenRO(path, busyMS)
	if err != nil {
		return nil
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	lit := store.Quote(sess)
	for _, kind := range []struct {
		typ  string
		want map[string]bool
	}{{"table", nameSet(store.SessionTables())}, {"view", nameSet(store.SessionViews())}} {
		var names []string
		rows, err := db.Query("SELECT m.name FROM main.sqlite_master m WHERE m.type=? AND EXISTS "+
			"(SELECT 1 FROM pragma_table_info(m.name) p WHERE p.name='herdr_session')", kind.typ)
		if err != nil {
			db.Close()
			return nil
		}
		bad := false
		for rows.Next() {
			var n string
			if rows.Scan(&n) != nil {
				bad = true
				break
			}
			if kind.want[n] {
				names = append(names, n)
			}
		}
		if bad || rows.Err() != nil { // 途中で読めなくなったら、包めない表が残るので開かない
			rows.Close()
			db.Close()
			return nil
		}
		rows.Close()
		for _, t := range names {
			if _, err := db.Exec("CREATE TEMP VIEW " + t + " AS SELECT * FROM main." + t + " WHERE herdr_session=" + lit); err != nil {
				db.Close()
				return nil
			}
		}
	}
	if _, err := db.Exec("PRAGMA query_only=ON"); err != nil {
		db.Close()
		return nil
	}
	return db
}
