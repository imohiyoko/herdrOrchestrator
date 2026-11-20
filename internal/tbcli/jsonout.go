package tbcli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// jqString は jq -c と同じ書き方の JSON 文字列(< > & や非 ASCII はそのまま、制御文字と DEL だけ \u で書く)。
func jqString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case utf8.RuneError:
			b.WriteString(`�`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
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
			dec.Token()
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
			b.WriteString(jqString(k.(string)))
			b.WriteByte(':')
			if err := jqValue(dec, b); err != nil {
				return err
			}
		}
		dec.Token()
		b.WriteByte('}')
	case string:
		b.WriteString(jqString(t))
	case json.Number:
		b.WriteString(jqNumber(t))
	case bool:
		b.WriteString(strconv.FormatBool(t))
	case nil:
		b.WriteString("null")
	}
	return nil
}

func jqNumber(n json.Number) string {
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

// fromJSON は列の JSON 文字列を jq の fromjson と同じく値に戻して書く。
func fromJSON(s string) (string, error) {
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

// jsonFields は fromjson する列(tasks.sh の list --json と同じ)
var jsonFields = map[string]bool{"deliverables": true, "labels": true, "refs": true}

// rowJSON は 1 行を jq -c と同じ 1 行の JSON にする(列の順を保つ)。
func rowJSON(cols []string, vals []any) (string, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, c := range cols {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(jqString(c))
		b.WriteByte(':')
		v := vals[i]
		switch {
		case v == nil:
			if jsonFields[c] {
				return "", fmt.Errorf("%s が NULL です", c)
			}
			b.WriteString("null")
		case jsonFields[c]:
			s, err := fromJSON(tickets.Text(v))
			if err != nil {
				return "", fmt.Errorf("%s: %w", c, err)
			}
			b.WriteString(s)
		default:
			switch x := v.(type) {
			case int64:
				b.WriteString(strconv.FormatInt(x, 10))
			case float64:
				b.WriteString(jqNumber(json.Number(strconv.FormatFloat(x, 'g', 17, 64))))
			default:
				b.WriteString(jqString(tickets.Text(v)))
			}
		}
	}
	b.WriteByte('}')
	return b.String(), nil
}
