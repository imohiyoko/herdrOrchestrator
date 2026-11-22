package agents

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
)

// 役割(taskboard/lib/roles.sh の role_lookup / role_files_for / role_name_of / name_matches)。
// 役割の本文は DB の config 表(キー role:<役割名> と role-map)> ROLES_DIR のファイル。

var (
	// NameRE は agents.sh の AGENT_NAME_RE(herdr agent start の名前の規則)。
	NameRE      = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	reRoleKey   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$`)
	reRoleAgent = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	reRoleItem  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	reMapVal    = regexp.MustCompile(`^[A-Za-z0-9._, -]+$`)
	reSessDir   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)
)

// RolesDir は roles.sh の ROLES_DIR: roles/sessions/<セッション>/ があればそこだけ、無ければ roles/。
func RolesDir(session string) string {
	base := os.Getenv("TB_ROLES")
	if base == "" {
		base = filepath.Join(os.Getenv("TB_ROOT"), "..", "roles")
	}
	if reSessDir.MatchString(session) {
		d := filepath.Join(base, "sessions", session)
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
	}
	return base
}

type roleBody struct {
	body string
	ok   bool
}

type roleNames struct {
	names []string
	ok    bool // false = 台帳を読めず確かめられない
}

// Roles は 1 回の判定の中で役割を引く(引いた結果は覚えておく。bash の role_lookup の覚え方と role_pin の固定にあたる)。
type Roles struct {
	DB      *sql.DB
	Session string
	Dir     string
	bodies  map[string]roleBody
	names   map[string]roleNames
}

// NewRoles は今のセッションの役割の引き方。
func NewRoles(db *sql.DB, session string) *Roles {
	return &Roles{DB: db, Session: session, Dir: RolesDir(session), bodies: map[string]roleBody{}, names: map[string]roleNames{}}
}

// NewRolesIn は役割のファイルの置き場を固定した引き方(review-submit は env の TB_ROLES やセッションの置き場を見ない)。DB の役割の行は見る。
func NewRolesIn(db *sql.DB, session, dir string) *Roles {
	return &Roles{DB: db, Session: session, Dir: dir, bodies: map[string]roleBody{}, names: map[string]roleNames{}}
}

// Lookup は role_lookup(@map は役割の対応)。
func (r *Roles) Lookup(name string) (string, bool) {
	if b, ok := r.bodies[name]; ok {
		return b.body, b.ok
	}
	key, file := "", ""
	if name == "@map" {
		key, file = "role-map", "map.conf"
	} else {
		if !reRoleKey.MatchString(name) || name == "README" {
			return "", false
		}
		key, file = "role:"+name, name+".md"
	}
	var b roleBody
	if body, _, ok := config.DBRow(r.DB, r.Session, key); ok {
		b = roleBody{body, true}
	} else if p := filepath.Join(r.Dir, file); isRegular(p) {
		data, _ := os.ReadFile(p)
		b = roleBody{strings.TrimRight(string(data), "\n"), true}
	}
	r.bodies[name] = b
	return b.body, b.ok
}

// Exists は role_exists。
func (r *Roles) Exists(name string) bool { _, ok := r.Lookup(name); return ok }

func isRegular(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// list は _role_list: カンマ区切りのうち、ある役割だけ。
func (r *Roles) list(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		x = config.Trim(x)
		if reRoleItem.MatchString(x) && x != "README" && r.Exists(x) {
			out = append(out, x)
		}
	}
	return out
}

// FilesFor は role_files_for(ROLE_STRICT=1): agent の役割名。台帳を読めなければ ok=false。
func (r *Roles) FilesFor(name string) ([]string, bool) {
	if !reRoleAgent.MatchString(name) || strings.HasPrefix(name, "_") || name == "README" {
		return nil, true
	}
	if r.Exists(name) {
		return []string{name}, true
	}
	var reg sql.NullString
	err := r.DB.QueryRow(`SELECT COALESCE(role,'') FROM agents WHERE name=?`, name).Scan(&reg)
	if err != nil && err != sql.ErrNoRows {
		return nil, false
	}
	if reg.String != "" {
		if found := r.list(reg.String); len(found) > 0 {
			return found, true
		}
	}
	body, ok := r.Lookup("@map")
	if !ok {
		return nil, true
	}
	for _, line := range strings.Split(body, "\n") {
		k, v, _ := strings.Cut(line, "=")
		v = util.ReadRest(v, "=")
		pat := config.Trim(cutHash(k))
		v = config.Trim(cutHash(v))
		if pat == "" || v == "" || !reMapVal.MatchString(v) {
			continue
		}
		if util.Glob(pat, name) {
			return r.list(v), true
		}
	}
	return nil, true
}

func cutHash(s string) string {
	if i := strings.IndexByte(s, '#'); i >= 0 {
		return s[:i]
	}
	return s
}

// NameOf は role_name_of(役割名のカンマ区切り)。台帳を読めなければ ok=false。
func (r *Roles) NameOf(name string) (string, bool) {
	if n, ok := r.names[name]; ok {
		return strings.Join(n.names, ","), n.ok
	}
	fs, ok := r.FilesFor(name)
	r.names[name] = roleNames{fs, ok}
	return strings.Join(fs, ","), ok
}

// First は role_file_for(先頭の役割名。無ければ空)。
func (r *Roles) First(name string) string {
	s, _ := r.NameOf(name)
	f, _, _ := strings.Cut(s, ",")
	return f
}

// Match は name_matches: 0 一致 / 1 不一致 / 2 台帳を読めず役割を確かめられない。
//
//	@<役割の glob>  その役割を持つ。@a+b は全部を持つ。それ以外は名前の glob
func (r *Roles) Match(name, pat string) int {
	if !strings.HasPrefix(pat, "@") {
		if util.Glob(pat, name) {
			return 0
		}
		return 1
	}
	roles, ok := r.NameOf(name)
	if !ok {
		return 2
	}
	if roles == "" {
		return 1
	}
	have := strings.Split(roles, ",")
	wants := strings.Split(pat[1:], "+")
	if wants[len(wants)-1] == "" { // read -ra は末尾の空の欄を数えない(@ だけなら 1 つも無い = 一致)
		wants = wants[:len(wants)-1]
	}
	for _, want := range wants {
		hit := false
		for _, h := range have {
			if util.Glob(want, h) {
				hit = true
				break
			}
		}
		if !hit {
			return 1
		}
	}
	return 0
}
