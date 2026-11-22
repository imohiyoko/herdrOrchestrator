// Package limits は制限の値を読む(taskboard/lib/limits.sh の limits_load / limit_norm / limit_raw / limit_get)。
// 書くのは agent サービスを移すときに足す。spawn_max_per_parent / spawn_max_total の spawn-policy への落とし方も、そのときに足す。
package limits

import (
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

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
	reInt  = regexp.MustCompile(`^[0-9]{1,6}$`)
	reName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
)

func trim(s string) string { return strings.Trim(s, " \t\n\v\f\r") }

// Norm は limit_norm: 値を検査して正規化する。通らなければ理由を err に入れる。
func Norm(k, v string) (string, error) {
	switch k {
	case "max_running_agents":
		if !reInt.MatchString(v) {
			return "", fmt.Errorf("%s は 1 以上の整数: %s", k, v)
		}
		n, _ := strconv.Atoi(v)
		if n < 1 {
			return "", fmt.Errorf("%s は 1 以上の整数: %s", k, v)
		}
		return strconv.Itoa(n), nil
	case "max_working_agents", "incident_max_open", "analyst_max", "spawn_max_per_parent", "spawn_max_total":
		if !reInt.MatchString(v) {
			return "", fmt.Errorf("%s は 0 以上の整数: %s", k, v)
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
}

// Load は limits_load: 設定 limits を読む(既知のキーで、値が通るものだけ)。
func Load(db *sql.DB, session string) Set {
	s := Set{Mode: "normal", conf: map[string]string{}}
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

// Get は limit_get(spawn_* の spawn-policy への落とし方はまだ。そのキーは既定値を返す)。
func (s Set) Get(k string) string {
	if v, _, ok := s.Raw(k); ok {
		return v
	}
	if k == "analyst_max" {
		if v := os.Getenv("TB_ANALYST_MAX"); v != "" {
			return v
		}
	}
	return Default(k)
}
