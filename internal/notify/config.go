package notify

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/capv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
)

// Types は通知の種別(NOTIFY_TYPES と同じ順)。
var Types = []string{"alert_raised", "alert_result", "pr_opened", "pr_approved", "pr_changes", "pr_merged", "question", "blocked", "done", "incident_opened", "incident_closed", "approvals_pending", "summary", "test", "warning", "digest", "ticket"}

var (
	boolKeys = []string{"enabled", "hash_check", "mask_ip", "mask_repo", "mask_account", "mask_host", "mask_email", "strip_raw_logs"}
	intKeys  = []string{"rate_per_min", "rate_per_hour", "max_len", "dedupe_sec", "ack_timeout_min", "ack_retries", "summary_interval_min"}
	capKeys  = []string{"rate_per_min", "rate_per_hour", "max_len"} // unlimited(上限なし)も書ける
	strKeys  = []string{"slack_via", "slack_target", "notifier_agent", "webhook_file"}
	reInt    = regexp.MustCompile(`^[0-9]{1,6}$`)
	reTarget = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	reAgent  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
)

// Default は notify_default(知らないキーは空)。
func Default(k string) string {
	switch k {
	case "enabled":
		return "off"
	case "hash_check", "mask_email", "strip_raw_logs":
		return "on"
	case "mask_ip", "mask_repo", "mask_account", "mask_host":
		return "off" // 人の決定: リポジトリ名・IP・アカウント ID・ホスト名は伏せない
	case "rate_per_min":
		return "6"
	case "rate_per_hour":
		return "60"
	case "max_len":
		return "600"
	case "dedupe_sec":
		return "600"
	case "ack_timeout_min":
		return "5"
	case "ack_retries":
		return "3"
	case "summary_interval_min":
		return "30"
	case "slack_via":
		return "agent"
	case "notifier_agent":
		return "notifier"
	case "sinks_default":
		return "file stdout slack"
	case "sinks_warning":
		return "file stdout"
	}
	return ""
}

// KeyOK は notify_key_ok(sinks_<種別> も許す)。
func KeyOK(k string) bool {
	if slices.Contains(boolKeys, k) || slices.Contains(intKeys, k) || slices.Contains(strKeys, k) || k == "sinks_default" {
		return true
	}
	t, ok := strings.CutPrefix(k, "sinks_")
	return ok && slices.Contains(Types, t)
}

// Norm は notify_norm: 値の検査と正規化。だめなら理由を err に。
func Norm(k, v string) (norm, errMsg string, ok bool) {
	switch {
	case slices.Contains(boolKeys, k):
		switch v {
		case "on", "true", "yes", "1":
			return "on", "", true
		case "off", "false", "no", "0":
			return "off", "", true
		}
		return "", k + " は on|off: " + v, false
	case slices.Contains(capKeys, k) && capv.IsUnlimited(v):
		return capv.Unlimited, "", true
	case slices.Contains(intKeys, k):
		if !reInt.MatchString(v) {
			if slices.Contains(capKeys, k) {
				return "", k + " は 0 以上の整数か unlimited: " + v, false
			}
			return "", k + " は 0 以上の整数: " + v, false
		}
		n, _ := strconv.Atoi(v)
		if k == "max_len" && n < 40 {
			return "", "max_len は 40 以上", false
		}
		return strconv.Itoa(n), "", true
	case k == "slack_via":
		if v != "agent" && v != "webhook" {
			return "", "slack_via は agent|webhook: " + v, false
		}
		return v, "", true
	case strings.HasPrefix(k, "sinks_"):
		var out []string
		for _, t := range ifsFields(strings.ReplaceAll(v, ",", " ")) {
			if t != "file" && t != "stdout" && t != "slack" {
				return "", k + " に使えない sink: " + t + "(file stdout slack)", false
			}
			if !slices.Contains(out, t) {
				out = append(out, t)
			}
		}
		return strings.Join(out, " "), "", true
	case k == "slack_target":
		if v != "" && !reTarget.MatchString(v) {
			return "", "slack_target は英数字と _ . - の 64 字まで", false
		}
		return v, "", true
	case k == "notifier_agent":
		if !reAgent.MatchString(v) {
			return "", "notifier_agent は agent 名", false
		}
		return v, "", true
	case k == "webhook_file":
		if strings.Contains(v, "\n") || utf8.RuneCountInString(v) > 512 {
			return "", "webhook_file が不正", false
		}
		return v, "", true
	}
	return "", "不明なキー: " + k, false
}

// Cfg は notify の設定(環境変数 TB_NOTIFY_<キー大文字> > 設定 notify > 既定)。
type Cfg struct {
	conf map[string]string
	db   *sql.DB
	sess string
}

// Load は notify_load: 設定 notify を読む(読めない行・知らないキー・不正な値は飛ばす)。
func Load(db *sql.DB, session string) *Cfg {
	c := &Cfg{conf: map[string]string{}, db: db, sess: session}
	body, _, ok := config.Lookup(db, session, "notify")
	if !ok {
		return c
	}
	for _, line := range config.Lines(body) {
		line = config.Trim(config.StripComment(line))
		if !strings.Contains(line, "=") {
			continue
		}
		k, v, _ := strings.Cut(line, "=")
		k, v = config.Trim(k), config.Trim(v)
		if !KeyOK(k) {
			continue
		}
		if n, _, ok := Norm(k, v); ok {
			c.conf[k] = n
		}
	}
	return c
}

func envName(k string) string { return "TB_NOTIFY_" + strings.ToUpper(k) }

// Get は ncfg。
func (c *Cfg) Get(k string) string {
	if v, ok := os.LookupEnv(envName(k)); ok {
		if n, _, ok := Norm(k, v); ok {
			return n
		}
	}
	if v, ok := c.conf[k]; ok {
		return v
	}
	return Default(k)
}

// Int は数の設定(不正なら 0。unlimited は capv.NoLimit)。
func (c *Cfg) Int(k string) int { return capv.Int(c.Get(k), 0) }

// Source は notify_source: env / conf / default。
func (c *Cfg) Source(k string) string {
	if v, ok := os.LookupEnv(envName(k)); ok {
		if _, _, ok := Norm(k, v); ok {
			return "env"
		}
	}
	if _, ok := c.conf[k]; ok {
		return "conf"
	}
	return "default"
}

// On は notify_on。
func (c *Cfg) On() bool { return c.Get("enabled") == "on" }

// Flag は nflag。
func (c *Cfg) Flag(k string) bool { return c.Get(k) == "on" }

// SinksOf は notify_sinks_of。
func (c *Cfg) SinksOf(t string) string {
	s := c.Get("sinks_" + t)
	if s == "" {
		s = c.Get("sinks_default")
	}
	if t == "warning" && c.Get("sinks_warning") == "" {
		s = "file stdout"
	}
	return s
}

// root は bin/tb が渡す TB_ROOT(checkout の taskboard)。
func root() string { return os.Getenv("TB_ROOT") }

// Dir は NOTIFY_DIR(TB_NOTIFY_DIR。既定は taskboard/data/notify)。
func Dir() string {
	if d := os.Getenv("TB_NOTIFY_DIR"); d != "" {
		return d
	}
	return filepath.Join(root(), "data", "notify")
}

// ReposFile は許可リスト(TB_REPOS_FILE。既定は taskboard/config/repos.md)。
func ReposFile() string {
	if f := os.Getenv("TB_REPOS_FILE"); f != "" {
		return f
	}
	return filepath.Join(root(), "config", "repos.md")
}

var reRepo = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*/[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// Repos は notify_repos: 許可リストの <org>/<repo>。
func Repos() []string {
	b, err := os.ReadFile(ReposFile())
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		line = config.Trim(line)
		rest, ok := strings.CutPrefix(line, "-")
		if !ok {
			continue
		}
		if rest = config.Trim(rest); reRepo.MatchString(rest) {
			out = append(out, rest)
		}
	}
	return out
}

// ifsFields は bash の既定の IFS(空白・タブ・改行)で分ける(for t in $v)。
func ifsFields(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' })
}
