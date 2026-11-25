// Package limits は制限の値を読む(taskboard/lib/limits.sh の limits_load / limit_norm / limit_raw / limit_get)。
// 書く側(limits_edit)は Edit。
package limits

import (
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/capv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
)

// Keys は LIMIT_KEYS。
var Keys = []string{"max_running_agents", "max_working_agents", "incident_max_open", "incident_roles", "analyst_max", "spawn_max_per_parent", "spawn_max_total", "watchers", "human_handoff"}

// IncidentRolesAll は INCIDENT_ROLES_ALL。
var IncidentRolesAll = []string{"investigator", "containment", "fixer", "lateral", "verifier"}

// Default は limit_default。
func Default(k string) string {
	switch k {
	case "max_running_agents":
		return "30"
	case "max_working_agents":
		return "0"
	case "incident_max_open", "analyst_max", "spawn_max_per_parent":
		return "2"
	case "incident_roles":
		return strings.Join(IncidentRolesAll, " ")
	case "spawn_max_total":
		return "4"
	case "watchers":
		return "watch"
	case "human_handoff":
		return "off"
	}
	return ""
}

var (
	reDigits = regexp.MustCompile(`^[0-9]+$`)
	reInt    = regexp.MustCompile(`^[0-9]{1,6}$`)
	reName   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
)

func trim(s string) string { return strings.Trim(s, " \t\n\v\f\r") }

// Unlimited は数の上限のキーで「上限なし」を表す値(capv.Unlimited)。
const Unlimited = capv.Unlimited

// IsUnlimited / Exceeds / Show は capv のもの。
var (
	IsUnlimited = capv.IsUnlimited
	Exceeds     = capv.Exceeds
	Show        = capv.Show
)

// Cap は数の上限の値を読む。limited=false なら上限なし(unlimited)。
// 数でない値は 0(上限 0)とみなす。max_working_agents の 0 = 無制限は呼ぶ側(Set.Max)が決める。
func Cap(v string) (n int, limited bool) {
	n, limited, _ = capv.Parse(v)
	return n, limited
}

// Norm は limit_norm: 値を検査して正規化する。通らなければ理由を err に入れる。
// 数の上限のキーは unlimited(上限なし)も受け付ける。
func Norm(k, v string) (string, error) {
	switch k {
	case "max_running_agents":
		if IsUnlimited(v) {
			return Unlimited, nil
		}
		if !reInt.MatchString(v) {
			return "", fmt.Errorf("%s は 1 以上の整数か unlimited: %s", k, v)
		}
		n, _ := strconv.Atoi(v)
		if n < 1 {
			return "", fmt.Errorf("%s は 1 以上の整数か unlimited: %s", k, v)
		}
		return strconv.Itoa(n), nil
	case "max_working_agents", "incident_max_open", "analyst_max", "spawn_max_per_parent", "spawn_max_total":
		if IsUnlimited(v) {
			return Unlimited, nil
		}
		if !reInt.MatchString(v) {
			return "", fmt.Errorf("%s は 0 以上の整数か unlimited: %s", k, v)
		}
		n, _ := strconv.Atoi(v)
		return strconv.Itoa(n), nil
	case "incident_roles", "watchers":
		var out []string
		for _, t := range strings.Fields(strings.ReplaceAll(v, ",", " ")) {
			if k == "incident_roles" && !slices.Contains(IncidentRolesAll, t) {
				return "", fmt.Errorf("incident_roles に使えない役: %s(使えるのは %s)", t, strings.Join(IncidentRolesAll, " "))
			}
			if k == "watchers" && !reName.MatchString(t) {
				return "", fmt.Errorf("watchers に使えない名前: %s", t)
			}
			if !slices.Contains(out, t) {
				out = append(out, t)
			}
		}
		if k == "incident_roles" && len(out) == 0 {
			return "", fmt.Errorf("incident_roles は 1 つ以上の役が要ります")
		}
		return strings.Join(out, " "), nil
	case "human_handoff":
		if v != "on" && v != "off" {
			return "", fmt.Errorf("human_handoff は on|off: %s", v)
		}
		return v, nil
	}
	return "", fmt.Errorf("不明なキー: %s(使えるキー: %s)", k, strings.Join(Keys, " "))
}

// Set は読んだ limits の設定。
type Set struct {
	Mode string
	conf map[string]string
	// spawn_max_per_parent / spawn_max_total を設定していないときの値(spawn.sh の load_spawn_policy の SP_PER / SP_TOTAL)
	spPer, spTotal string
}

// Load は limits_load: 設定 limits を読む(既知のキーで、値が通るものだけ)。
func Load(db *sql.DB, session string) Set {
	s := Set{Mode: "normal", conf: map[string]string{}, spPer: "2", spTotal: "6"}
	if body, _, ok := config.Lookup(db, session, "spawn-policy"); ok {
		for _, line := range strings.Split(body, "\n") {
			line, _, _ = strings.Cut(line, "#")
			k, v, ok := strings.Cut(trim(line), "=")
			if !ok {
				continue
			}
			k, v = trim(k), trim(v)
			if IsUnlimited(v) {
				v = Unlimited
			} else if !reDigits.MatchString(v) {
				continue
			}
			switch k {
			case "max_children_per_parent":
				s.spPer = v
			case "max_children_total":
				s.spTotal = v
			}
		}
	}
	body, _, ok := config.Lookup(db, session, "limits")
	if !ok {
		return s
	}
	for _, line := range strings.Split(body, "\n") {
		line, _, _ = strings.Cut(line, "#")
		line = trim(line)
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = trim(k), trim(v)
		if k == "mode" {
			s.Mode = v
			continue
		}
		if !slices.Contains(Keys, k) {
			continue
		}
		if n, err := Norm(k, v); err == nil {
			s.conf[k] = n
		}
	}
	return s
}

// Raw は limit_raw: 環境変数(TB_LIMIT_<キー>)か設定で決まった値。src は env か conf。
func (s Set) Raw(k string) (v, src string, ok bool) {
	if ev, set := os.LookupEnv("TB_LIMIT_" + strings.ToUpper(k)); set {
		if n, err := Norm(k, ev); err == nil {
			return n, "env", true
		}
	}
	if v, ok := s.conf[k]; ok {
		return v, "conf", true
	}
	return "", "default", false
}

// Get は limit_get: 設定が無ければ、spawn_* は spawn-policy の値、analyst_max は TB_ANALYST_MAX、ほかは既定。
func (s Set) Get(k string) string {
	if v, _, ok := s.Raw(k); ok {
		return v
	}
	switch k {
	case "spawn_max_per_parent":
		return s.spPer
	case "spawn_max_total":
		return s.spTotal
	}
	if k == "analyst_max" {
		if v := os.Getenv("TB_ANALYST_MAX"); v != "" {
			if IsUnlimited(v) {
				return Unlimited
			}
			return v
		}
	}
	return Default(k)
}

// Max は数の上限のキーの値。limited=false なら上限なし。
// max_working_agents は 0 も上限なし(前からの約束)。
func (s Set) Max(k string) (n int, limited bool) {
	n, limited = Cap(s.Get(k))
	if k == "max_working_agents" && n <= 0 {
		return 0, false
	}
	return n, limited
}

// ModeKeys は LIMIT_MODE_KEYS: tb limits mode が触るキー。
var ModeKeys = []string{"max_working_agents", "incident_max_open", "incident_roles", "analyst_max", "spawn_max_total", "watchers"}

// Unset は Edit の値で「その行を消す(既定に戻す)」。
const Unset = "@unset"

// ModeTable は limit_mode_table: そのモードで ModeKeys に入れる値(表に無いキーは Unset)。知らないモードなら ok=false。
func ModeTable(mode string) (map[string]string, bool) {
	t := map[string]string{}
	switch mode {
	case "normal":
	case "lean":
		t = map[string]string{"max_working_agents": "10", "incident_max_open": "1", "incident_roles": "investigator fixer verifier", "analyst_max": "1", "spawn_max_total": "2"}
	case "minimal":
		t = map[string]string{"max_working_agents": "5", "incident_max_open": "1", "incident_roles": "investigator", "analyst_max": "1", "spawn_max_total": "0", "watchers": ""}
	default:
		return nil, false
	}
	for _, k := range ModeKeys {
		if _, ok := t[k]; !ok {
			t[k] = Unset
		}
	}
	return t, true
}

// KV は Edit に渡すキーと値の組。
type KV struct{ K, V string }

// EditBody は limits_edit の本文: 今の本文(例のファイルなら使わない)の同じキーの行を書き換え、無いキーは末尾に足す。
// 値が Unset ならその行を消す。同じキーの 2 行目からは消す。cur が空で has=false なら見出しの 1 行から始める。
func EditBody(cur string, has bool, kvs []KV) string {
	done := make([]bool, len(kvs))
	var out []string
	if has {
		lines := strings.Split(strings.TrimRight(cur, "\n"), "\n")
		if cur == "" {
			lines = nil
		}
		for _, line := range lines {
			hit := -1
			if !strings.HasPrefix(line, "#") && strings.Contains(line, "=") {
				k, _, _ := strings.Cut(line, "#")
				k, _, _ = strings.Cut(trim(k), "=")
				k = trim(k)
				for i, kv := range kvs {
					if kv.K == k {
						hit = i
					}
				}
			}
			if hit < 0 {
				out = append(out, line)
				continue
			}
			if !done[hit] && kvs[hit].V != Unset {
				out = append(out, kvs[hit].K+" = "+kvs[hit].V)
			}
			done[hit] = true
		}
	} else {
		out = append(out, "# tb limits が書いた制限の設定。編集するのは人だけ(項目は limits.conf.example)。環境変数 TB_LIMIT_<キー大文字> が優先。")
	}
	for i, kv := range kvs {
		if !done[i] && kv.V != Unset {
			out = append(out, kv.K+" = "+kv.V)
		}
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}
