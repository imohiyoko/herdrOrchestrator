// Package agents は agent の管理(taskboard/lib/agents.sh ほか)。今は設定 protect / role-models の検査だけ。
package agents

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
)

var (
	reGlob = regexp.MustCompile(`^[A-Za-z0-9*?._-]{1,64}$`)
	// ModelRE は agents.sh の MODEL_RE。
	ModelRE = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
)

// ValidateProtect は config_validate_protect。
func ValidateProtect(body string) string {
	var bad strings.Builder
	for i, line := range config.Lines(body) {
		line = config.Trim(config.StripComment(line))
		if line == "" {
			continue
		}
		k, _, _ := strings.Cut(line, "=")
		k = config.Trim(k)
		if !reGlob.MatchString(strings.TrimPrefix(k, "role:")) {
			fmt.Fprintf(&bad, "%d 行目: <名前の glob> か role:<役割の glob> の形ではありません: %s\n", i+1, k)
		}
	}
	return bad.String()
}

// ValidateRoleModels は config_validate_role_models。
func ValidateRoleModels(body string) string {
	var bad strings.Builder
	for i, line := range config.Lines(body) {
		n := i + 1
		line = config.Trim(config.StripComment(line))
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			fmt.Fprintf(&bad, "%d 行目: <役割の glob> = <モデル>(または name:<agent 名の glob> = <モデル>)の形ではありません\n", n)
			continue
		}
		k, v = config.Trim(k), config.Trim(v)
		if !reGlob.MatchString(strings.TrimPrefix(k, "name:")) {
			fmt.Fprintf(&bad, "%d 行目: 役割(または name:<agent 名>)の glob に使えない文字があります: %s\n", n, k)
		}
		if !ModelRE.MatchString(v) {
			fmt.Fprintf(&bad, "%d 行目: モデル名に使えない文字があります: %s\n", n, v)
		}
	}
	return bad.String()
}
