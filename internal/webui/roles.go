package webui

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	appconfig "github.com/imohiyoko/herdrorchestrator/internal/app/config"
)

// roles.go はロールの解決(tb の role_files_for / name_matches と同じ)。

var (
	reName    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	reRole    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	reSession = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)
	reMapVal  = regexp.MustCompile(`^[A-Za-z0-9._, -]+$`)
)

func pyStrip(s string) string { return strings.TrimFunc(s, pyIsSpace) }

// rolesDir は役割ファイルの場所(このセッションの roles/sessions/<セッション>/ があればそこだけ)。
func (v *sv) rolesDir() string {
	base := os.Getenv("TB_ROLES")
	if base == "" {
		base = filepath.Join(HerdrRoot(), "roles")
	}
	if reSession.MatchString(v.sess) {
		d := filepath.Join(base, "sessions", v.sess)
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
	}
	return base
}

// roleDB は DB の役割の定義(config の role:<名前> / role-map)。このセッションの行が共通の行より勝つ。
func (v *sv) roleDB() map[string]string {
	return cached(v.sess, "role-db", func() map[string]string {
		rows := appconfig.RoleRows(v.qChecked, v.sess)
		sort.SliceStable(rows, func(i, j int) bool { return rows[i]["s"] == "*" && rows[j]["s"] != "*" })
		out := map[string]string{}
		for _, r := range rows {
			out[pyStr(r["key"])] = pyStr(r["body"])
		}
		return out
	})
}

func (v *sv) roleExists(x, rdir string) bool {
	if rdir == "" {
		rdir = v.rolesDir()
	}
	if _, ok := v.roleDB()["role:"+x]; ok {
		return true
	}
	return isFile(filepath.Join(rdir, x+".md"))
}

func (v *sv) mapLines(rdir string) []string {
	if m, ok := v.roleDB()["role-map"]; ok {
		return pySplitlines(m)
	}
	b, err := os.ReadFile(filepath.Join(rdir, "map.conf"))
	if err != nil {
		return nil
	}
	return pySplitlines(strings.ToValidUTF8(string(b), "�"))
}

func (v *sv) roleList(text, rdir string) []string {
	var out []string
	for _, x := range strings.Split(text, ",") {
		x = pyStrip(x)
		if x != "" && reRole.MatchString(x) && x != "README" && v.roleExists(x, rdir) {
			out = append(out, x)
		}
	}
	return out
}

// rolesOf は役割名のリスト(roles/<名前>.md > 台帳の role > map.conf の順)。
func (v *sv) rolesOf(name, regRole string) []string {
	rdir := v.rolesDir()
	if name == "" || !reName.MatchString(name) || strings.HasPrefix(name, "_") || name == "README" {
		return nil
	}
	if v.roleExists(name, rdir) {
		return []string{name}
	}
	if found := v.roleList(regRole, rdir); len(found) > 0 {
		return found
	}
	for _, line := range v.mapLines(rdir) {
		k, val, sep := strings.Cut(line, "=")
		pat, _, _ := strings.Cut(k, "#")
		val, _, _ = strings.Cut(val, "#")
		pat, val = pyStrip(pat), pyStrip(val)
		if !sep || pat == "" || val == "" || !reMapVal.MatchString(val) {
			continue
		}
		if fnmatchcase(name, pat) {
			return v.roleList(val, rdir)
		}
	}
	return nil
}

// nameMatches は tb の name_matches(@a は a を持つ、@a+b は両方を持つ)。
func (v *sv) nameMatches(name, pat, regRole string) bool {
	if !strings.HasPrefix(pat, "@") {
		return fnmatchcase(name, pat)
	}
	have := v.rolesOf(name, regRole)
	if len(have) == 0 {
		return false
	}
	for _, w := range strings.Split(pat[1:], "+") {
		hit := false
		for _, r := range have {
			if fnmatchcase(r, w) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}
