package tbcli

import (
	"fmt"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/perms"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/env"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
	"github.com/imohiyoko/herdrorchestrator/internal/notify"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

const configUsage = "config [list | show <キー> | set <キー> [--shared] < 本文 | edit <キー> [--shared] | unset <キー> [--shared] | import [--dry-run] | history <キー>]"

// validators は config_validate_<キー>。検査はそのキーを持つサービスのパッケージに置く。
func validators(st tickets.StatusSet) map[string]config.Validator {
	return map[string]config.Validator{
		"messages":      config.ValidateMessages,
		"statuses":      tickets.ValidateStatuses,
		"labels":        tickets.ValidateLabels,
		"system-labels": tickets.ValidateSystemLabels,
		"notify-rules":  notify.ValidateRules,
		"nudge":         message.ValidateNudge(st),
		"protect":       agents.ValidateProtect,
		"role-models":   agents.ValidateRoleModels,
		"org":           approval.ValidateOrg,
		"policy":        perms.ValidatePolicy,
	}
}

func cmdConfig(args []string, w, stderr io.Writer) error {
	db, st, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	sub := "list"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "list":
		if len(args) != 0 {
			return usage("config list")
		}
		fmt.Fprintf(w, "セッション: %s\n", db.Session)
		for _, k := range config.Keys {
			_, src, _ := config.Lookup(db.DB, db.Session, k)
			fmt.Fprintf(w, "%-13s %s\n", k, src)
		}
		return nil
	case "show":
		if len(args) != 1 {
			return usage("config show <キー>")
		}
		k := args[0]
		if err := keyOK(k); err != nil {
			return err
		}
		body, src, ok := config.Lookup(db.DB, db.Session, k)
		if !ok {
			return die("%s の設定はありません(DB にもファイルにも無い。tb config set %s で作る)", k, k)
		}
		fmt.Fprintf(stderr, "# %s: %s\n", k, src)
		if body != "" {
			fmt.Fprintln(w, body)
		}
		return nil
	case "set", "edit", "unset":
		return configChange(db, st, sub, args, w, stderr)
	case "import":
		return configImport(db, args, w)
	case "history":
		if len(args) != 1 {
			return usage("config history <キー>")
		}
		if err := keyOK(args[0]); err != nil {
			return err
		}
		return store.Each(db.DB, `SELECT id, herdr_session, how, changed_by, changed_at, CASE WHEN body IS NULL THEN '(消した)' ELSE length(body) || ' 文字' END
    FROM main.config_changes WHERE key=? AND herdr_session IN (?, '*') ORDER BY id DESC LIMIT 50`, []any{args[0], db.Session},
			func(cols []string, vals []any) error { return emitRow(w, false, cols, vals, "") })
	}
	return usage("%s", configUsage)
}

func keyOK(k string) error {
	if _, ok := config.Info(k); !ok {
		return usage("不明な設定のキー: %s(使えるのは: %s)", k, strings.Join(config.Keys, " "))
	}
	return nil
}

func configChange(db *store.DB, st tickets.StatusSet, sub string, args []string, w, stderr io.Writer) error {
	key, shared := "", false
	for _, a := range args {
		switch {
		case a == "--shared":
			shared = true
		case strings.HasPrefix(a, "-"):
			return usage("config %s: 不明な引数 %s", sub, a)
		default:
			if key != "" {
				return usage("config %s <キー> [--shared]", sub)
			}
			key = a
		}
	}
	if key == "" {
		return usage("config %s <キー> [--shared]", sub)
	}
	if err := keyOK(key); err != nil {
		return err
	}
	if err := requireHuman("tb config " + sub); err != nil {
		return err
	}
	if key == "conv-routes" && sub != "unset" {
		return die("conv-routes は tb conv-routes-set < 新しい表 で書きます(緩める変更は --approve-looser が要ります)")
	}
	if (key == "approvals" || key == "grants") && sub != "unset" {
		return die("%s はもう読みません(ADR 0007)。権限は設定 policy に書きます(残っている古い設定は tb policy import で移します)", key)
	}
	info, _ := config.Info(key)
	envFile := os.Getenv(info.Env)
	if shared && envFile != "" {
		return die("%s でファイルを明示しているので --shared は使えません", info.Env)
	}
	source := func() string { _, s, _ := config.Lookup(db.DB, db.Session, key); return s }
	var body string
	switch sub {
	case "unset":
		if envFile != "" {
			return die("%s でファイルを明示しています(DB の行は使っていません)", info.Env)
		}
		if err := config.Unset(db.DB, db.Session, key, shared); err != nil {
			return err
		}
		fmt.Fprintf(w, "%s: %s\n", key, source())
		return nil
	case "set":
		if stdinIsTTY() {
			return usage("config set <キー>: 本文は標準入力から渡す(例: tb config set %s < 本文のファイル)", key)
		}
		var err error
		if body, err = readStdin(); err != nil {
			return err
		}
	case "edit":
		cur, _, has := config.Lookup(db.DB, db.Session, key)
		var err error
		if body, err = editBody(cur, has && cur != ""); err != nil {
			return err
		}
	}
	if v := validators(st)[key]; v != nil {
		if msg := v(body); msg != "" {
			fmt.Fprint(stderr, msg)
			return die("%s の本文に問題があるため書きませんでした", key)
		}
	}
	if envFile != "" {
		if err := writeConfigFile(envFile, body); err != nil {
			return err
		}
	} else if err := config.DBPut(db.DB, db.Session, key, body, sub, shared, ""); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s: %s\n", key, source())
	return nil
}

// writeConfigFile は config_put のファイルの書き方: 同じディレクトリの一時ファイルに書いて置き換える(今のファイルの権限を保つ)。
func writeConfigFile(f, body string) error {
	if st, err := os.Lstat(f); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return die("%s がシンボリックリンクです: %s", filepath.Base(f), f)
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f), filepath.Base(f)+".")
	if err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(f); err == nil {
		mode = st.Mode().Perm()
	}
	if body != "" {
		body += "\n"
	}
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f)
}

var (
	reRoleKey     = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$`)
	reSessionName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$`)
)

// configImport は config_import と同じ: checkout のファイルを共通の行(*)に、roles/sessions/<セッション>/ をそのセッションの行に取り込み、退かす。
func configImport(db *store.DB, args []string, w io.Writer) error {
	dry := false
	for _, a := range args {
		if a != "--dry-run" {
			return usage("config import [--dry-run]")
		}
		dry = true
	}
	if err := requireHuman("tb config import"); err != nil {
		return err
	}
	dest, n := "", 0
	mkdest := func() error {
		if dest != "" {
			return nil
		}
		dest = filepath.Join(env.StateDir(), "config-imported", time.Now().Format("20060102-150405"))
		if os.MkdirAll(dest, 0o755) != nil {
			return die("退かす先を作れません: %s", dest)
		}
		return nil
	}
	for _, k := range config.Keys {
		info, _ := config.Info(k)
		f := filepath.Join(config.Dir(), info.File)
		if !isRegularNoLink(f) {
			continue
		}
		if ok, err := config.HasRow(db.DB, db.Session, k, true); err != nil {
			return err
		} else if ok {
			fmt.Fprintf(w, "%s: 共通の行が既にあるので飛ばします(%s はそのまま)\n", k, f)
			continue
		}
		n++
		if dry {
			fmt.Fprintf(w, "%s: %s を共通の行として取り込みます\n", k, f)
			continue
		}
		if err := mkdest(); err != nil {
			return err
		}
		if err := config.DBPut(db.DB, db.Session, k, readBodyFile(f), "import", true, ""); err != nil {
			return err
		}
		if moveFile(f, dest) != nil {
			return die("%s を退かせません(DB には取り込み済み)", f)
		}
		fmt.Fprintf(w, "%s: %s を共通の行として取り込み、%s/ へ退かしました\n", k, f, dest)
	}
	// セッションごとの役割(roles/sessions/<セッション>/*.md と map.conf)は、そのセッションの行として取り込み、ディレクトリごと退かす
	base := os.Getenv("TB_ROLES")
	if base == "" {
		base = filepath.Join(os.Getenv("TB_ROOT"), "..", "roles")
	}
	dirs, _ := filepath.Glob(filepath.Join(base, "sessions", "*"))
	sort.Strings(dirs)
	for _, d := range dirs {
		if st, err := os.Lstat(d); err != nil || !st.IsDir() {
			continue
		}
		sname := filepath.Base(d)
		if !reSessionName.MatchString(sname) {
			fmt.Fprintf(w, "roles/sessions/%s: セッション名に使えない文字があるので飛ばします\n", sname)
			continue
		}
		n++
		files, _ := filepath.Glob(filepath.Join(d, "*.md"))
		sort.Strings(files)
		files = append(files, filepath.Join(d, "map.conf"))
		for _, rf := range files {
			if !isRegularNoLink(rf) {
				continue
			}
			key := "role-map"
			if filepath.Base(rf) != "map.conf" {
				name := strings.TrimSuffix(filepath.Base(rf), ".md")
				if name == "README" {
					continue
				}
				if !reRoleKey.MatchString(name) {
					fmt.Fprintf(w, "%s: 役割名に使えない文字があるので飛ばします\n", rf)
					continue
				}
				key = "role:" + name
			}
			var c int
			if err := db.QueryRow(`SELECT COUNT(*) FROM main.config WHERE herdr_session=? AND key=?`, sname, key).Scan(&c); err != nil {
				return err
			}
			if c > 0 {
				fmt.Fprintf(w, "%s(セッション %s): 行が既にあるので飛ばします\n", key, sname)
				continue
			}
			if dry {
				fmt.Fprintf(w, "%s: %s をセッション %s の行として取り込みます\n", key, rf, sname)
				continue
			}
			// 例外: ほかのセッションの行を書く(人が実行する取り込み。見張りなしの接続で書く)
			if err := putOtherSession(sname, key, readBodyFile(rf)); err != nil {
				return err
			}
			fmt.Fprintf(w, "%s: %s をセッション %s の行として取り込みました\n", key, rf, sname)
		}
		if dry {
			continue
		}
		if err := mkdest(); err != nil {
			return err
		}
		if os.MkdirAll(filepath.Join(dest, "roles-sessions"), 0o755) != nil || os.Rename(d, filepath.Join(dest, "roles-sessions", sname)) != nil {
			return die("%s/ を退かせません(DB には取り込み済み)", d)
		}
		fmt.Fprintf(w, "roles/sessions/%s/ を %s/roles-sessions/ へ退かしました\n", sname, dest)
	}
	if dry {
		fmt.Fprintln(w, "(dry-run: 何も書いていません)")
	}
	if n == 0 {
		fmt.Fprintln(w, "取り込むファイルはありません")
	}
	return nil
}

func putOtherSession(sname, key, body string) error {
	var opt store.Options
	opt.NoSession = true
	if d := os.Getenv("TB_SCHEMA_DIR"); d != "" {
		opt.Schema = os.DirFS(d)
	}
	db, err := store.Open(env.TBDB(), sname, &opt)
	if err != nil {
		return err
	}
	defer db.Close()
	return config.DBPut(db.DB, sname, key, body, "import", false, "")
}

func isRegularNoLink(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.Mode().IsRegular()
}

func readBodyFile(p string) string {
	b, _ := os.ReadFile(p)
	return strings.TrimRight(string(b), "\n")
}
