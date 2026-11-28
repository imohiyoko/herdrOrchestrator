// Package rowfmt は DB の行を tb の出力の形にする(sqlite3 -separator \t と jq -c の形)。tb と API の両方が使う。
package rowfmt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

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
