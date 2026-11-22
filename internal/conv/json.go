package conv

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// lastValue は jq がいくつかの値を読んだときの最後の値(-e は最後の出力で決まる)。読めなければ false。
func lastValue(b []byte) (json.RawMessage, bool) {
	d := json.NewDecoder(bytes.NewReader(b))
	var last json.RawMessage
	seen := false
	for {
		var v json.RawMessage
		if err := d.Decode(&v); err == io.EOF {
			break
		} else if err != nil {
			return nil, false
		}
		last, seen = v, true
	}
	return last, seen
}

// object は JSON のオブジェクトを、キーの最初に出た順(jq の keys_unsorted)と値(重なったキーは後の値)で読む。
func object(raw json.RawMessage) (keys []string, vals map[string]json.RawMessage, ok bool) {
	d := json.NewDecoder(bytes.NewReader(raw))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return nil, nil, false
	}
	vals = map[string]json.RawMessage{}
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return nil, nil, false
		}
		k, _ := t.(string)
		var v json.RawMessage
		if err := d.Decode(&v); err != nil {
			return nil, nil, false
		}
		if _, dup := vals[k]; !dup {
			keys = append(keys, k)
		}
		vals[k] = v
	}
	return keys, vals, true
}

func asString(v json.RawMessage) (string, bool) {
	var s string
	if len(v) == 0 || v[0] != '"' || json.Unmarshal(v, &s) != nil {
		return "", false
	}
	return s, true
}

var stdinKeys = []string{"event", "counterpart", "depth", "severity", "deadline"}

// stdinObject は conv-route --stdin の JSON を確かめて 5 つの区分の名前を返す。だめなら usage の文。
func stdinObject(b []byte) (map[string]string, bool, string) {
	raw, ok := lastValue(b)
	keys, vals, isObj := object(raw)
	if !ok || !isObj {
		return nil, false, "conv-route --stdin: JSON のオブジェクトを渡す"
	}
	bad := ""
	for _, k := range keys {
		if !has(stdinKeys, k) {
			bad += k + " "
		}
	}
	if bad != "" {
		return nil, false, "conv-route --stdin: 使えないキーがあります: " + bad + "(5 つの区分の名前だけ)"
	}
	m := map[string]string{}
	for _, k := range keys {
		s, ok := asString(vals[k])
		if !ok {
			return nil, false, "conv-route --stdin: 値は文字列だけ"
		}
		m[k] = s
	}
	return m, true, ""
}

type routeObj struct {
	status, reason string
	vals           map[string]json.RawMessage
}

// str は .[$f] // "" | strings を jq -r で出して $(...) で受けたもの(文字列でなければ空)。
func (r routeObj) str(f string) string {
	s, _ := asString(r.vals[f])
	return strings.TrimRight(s, "\n")
}

// routeObject は tb conv-route --json の出力か(status が routed か unregistered のオブジェクト)。
func routeObject(b []byte) (routeObj, bool) {
	raw, ok := lastValue(b)
	_, vals, isObj := object(raw)
	if !ok || !isObj {
		return routeObj{}, false
	}
	st, _ := asString(vals["status"])
	if st != "routed" && st != "unregistered" {
		return routeObj{}, false
	}
	r := routeObj{status: st, reason: "-", vals: vals}
	if v, ok := vals["reason"]; ok && string(v) != "null" && string(v) != "false" {
		if s, ok := asString(v); ok {
			r.reason = s
		} else if vs, err := decodeAll(v); err == nil && len(vs) == 1 {
			r.reason = pretty(vs[0])
		}
	}
	r.reason = strings.TrimRight(r.reason, "\n")
	return r, true
}
