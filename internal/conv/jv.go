package conv

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
)

// jq と同じ形で JSON を読み書きするための小さな値の型(キーの並びと数の書き方を保つ)。
// 値は nil / bool / string / json.Number(書いたままの数)/ float64(計算した数)/ obj / []any。

type kv struct {
	k string
	v any
}

type obj []kv

func (o obj) get(k string) any {
	for i := len(o) - 1; i >= 0; i-- {
		if o[i].k == k {
			return o[i].v
		}
	}
	return nil
}

// set は同じキーがあれば置き換え、無ければ末尾に足す(jq の + と同じ)。
func (o obj) set(k string, v any) obj {
	for i := range o {
		if o[i].k == k {
			o[i].v = v
			return o
		}
	}
	return append(o, kv{k, v})
}

// decodeAll は jq -s の入力(いくつ並んでもよい)。読めなければ err。
func decodeAll(b []byte) ([]any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var out []any
	for {
		v, err := decodeValue(d)
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
}

func decodeValue(d *json.Decoder) (any, error) {
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch x := t.(type) {
	case json.Delim:
		switch x {
		case '{':
			var o obj
			for d.More() {
				kt, err := d.Token()
				if err != nil {
					return nil, err
				}
				v, err := decodeValue(d)
				if err != nil {
					return nil, err
				}
				o = o.set(kt.(string), v)
			}
			_, err := d.Token()
			if o == nil {
				o = obj{}
			}
			return o, err
		case '[':
			a := []any{}
			for d.More() {
				v, err := decodeValue(d)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			_, err := d.Token()
			return a, err
		}
		return nil, io.ErrUnexpectedEOF
	default:
		return x, nil
	}
}

// numStr は計算した数の jq の書き方(整数はそのまま、ほかは最短の 17 桁まで)。
func numStr(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e17 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func num(v any) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case float64:
		return x, true
	case int:
		return float64(x), true
	}
	return 0, false
}

// scalar は jq -r の 1 つの値の出し方(文字列は引用符なし)。
func scalar(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return compact(v)
}

func compact(v any) string {
	var b strings.Builder
	write(&b, v, "", 0)
	return b.String()
}

func pretty(v any) string {
	var b strings.Builder
	write(&b, v, "  ", 0)
	return b.String()
}

func write(b *strings.Builder, v any, ind string, lv int) {
	nl := func(l int) {
		if ind != "" {
			b.WriteByte('\n')
			b.WriteString(strings.Repeat(ind, l))
		}
	}
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case string:
		b.WriteString(util.JQString(x))
	case json.Number:
		b.WriteString(string(x))
	case float64:
		b.WriteString(numStr(x))
	case int:
		b.WriteString(strconv.Itoa(x))
	case obj:
		if len(x) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteByte('{')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			nl(lv + 1)
			b.WriteString(util.JQString(e.k))
			b.WriteByte(':')
			if ind != "" {
				b.WriteByte(' ')
			}
			write(b, e.v, ind, lv+1)
		}
		nl(lv)
		b.WriteByte('}')
	case []any:
		if len(x) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			nl(lv + 1)
			write(b, e, ind, lv+1)
		}
		nl(lv)
		b.WriteByte(']')
	}
}

// tsv は jq の @tsv(\ タブ 改行 CR を \ で書く)。
func tsv(vs ...any) string {
	r := strings.NewReplacer(`\`, `\\`, "\t", `\t`, "\n", `\n`, "\r", `\r`)
	s := make([]string, len(vs))
	for i, v := range vs {
		if v == nil {
			continue
		}
		s[i] = r.Replace(scalar(v))
	}
	return strings.Join(s, "\t")
}

// jqLess は jq の並び(null < false < true < 数 < 文字列 < 配列 < object)。文字列はバイトの順。
func jqLess(a, b any) int {
	rank := func(v any) int {
		switch x := v.(type) {
		case nil:
			return 0
		case bool:
			if x {
				return 2
			}
			return 1
		case json.Number, float64, int:
			return 3
		case string:
			return 4
		case []any:
			return 5
		}
		return 6
	}
	ra, rb := rank(a), rank(b)
	if ra != rb {
		return ra - rb
	}
	switch ra {
	case 3:
		x, _ := num(a)
		y, _ := num(b)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
	case 4:
		return strings.Compare(a.(string), b.(string))
	}
	return 0
}

// jnum は書いたままの数(jq --argjson と同じく、そのままの形で出す)。
func jnum(s string) any { return json.Number(s) }
