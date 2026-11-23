package agents

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
	body, src string // src は db:<セッション> / db:* / file:<パス>
	ok        bool
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
	b, _, ok := r.LookupSrc(name)
	return b, ok
}

// LookupSrc は role_lookup の本文と出どころ(RB_SRC)。
func (r *Roles) LookupSrc(name string) (body, src string, ok bool) {
	if b, ok := r.bodies[name]; ok {
		return b.body, b.src, b.ok
	}
	key, file := "", ""
	if name == "@map" {
		key, file = "role-map", "map.conf"
	} else {
		if !reRoleKey.MatchString(name) || name == "README" {
			return "", "none", false
		}
		key, file = "role:"+name, name+".md"
	}
	b := roleBody{src: "none"}
	if body, shared, ok := config.DBRow(r.DB, r.Session, key); ok {
		b = roleBody{body, "db:" + r.Session, true}
		if shared {
			b.src = "db:*"
		}
	} else if p := filepath.Join(r.Dir, file); isRegular(p) {
		data, _ := os.ReadFile(p)
		b = roleBody{strings.TrimRight(string(data), "\n"), "file:" + p, true}
	}
	r.bodies[name] = b
	return b.body, b.src, b.ok
}

// Forget は role_forget(書いたあとに覚えた本文を捨てる)。
func (r *Roles) Forget(name string) {
	delete(r.bodies, name)
	r.names = map[string]roleNames{}
}

// Hint は ROLE_HINT_NAME / ROLE_HINT: まだ台帳に無い agent(作成の承認判定)は、名前から推定せず要求された役割(カンマ区切り)で判定する。
func (r *Roles) Hint(name, roles string) {
	r.names[name] = roleNames{strings.Split(roles, ","), true}
}

// KeyOf は role_key_of: 役割名(か @map)の DB のキー。使えない名前なら ok=false。
func KeyOf(name string) (string, bool) {
	if name == "@map" {
		return "role-map", true
	}
	if !reRoleKey.MatchString(name) || name == "README" {
		return "", false
	}
	return "role:" + name, true
}

// List は role_list: 役割名の一覧(DB の行とファイル。_ で始まるもの・README を除く。バイト順で重複なし)。
func (r *Roles) List() []string {
	var all []string
	if rows, err := r.DB.Query(`SELECT substr(key, 6) FROM main.config WHERE key LIKE 'role:%' AND herdr_session IN (?, '*')`, r.Session); err == nil {
		for rows.Next() {
			var k string
			if rows.Scan(&k) == nil {
				all = append(all, k)
			}
		}
		rows.Close()
	}
	if es, err := os.ReadDir(r.Dir); err == nil {
		for _, e := range es {
			n := e.Name()
			if !strings.HasPrefix(n, ".") && strings.HasSuffix(n, ".md") && isRegular(filepath.Join(r.Dir, n)) {
				all = append(all, strings.TrimSuffix(n, ".md"))
			}
		}
	}
	sort.Strings(all)
	var out []string
	for i, n := range all {
		if strings.HasPrefix(n, "_") || n == "README" || (i > 0 && n == all[i-1]) {
			continue
		}
		out = append(out, n)
	}
	return out
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
	return r.filesFor(name, nil, true)
}

// FilesForLoose は ROLE_STRICT なしの role_files_for(台帳を読めなければ未登録とみなして map で決める。load_role など)。
func (r *Roles) FilesForLoose(name string) []string {
	fs, _ := r.filesFor(name, nil, false)
	return fs
}

// FilesForReg は role_files_for <名前> <台帳の role>(台帳を読まずに渡した値で決める)。
func (r *Roles) FilesForReg(name, reg string) []string {
	fs, _ := r.filesFor(name, &reg, false)
	return fs
}

func (r *Roles) filesFor(name string, given *string, strict bool) ([]string, bool) {
	if !reRoleAgent.MatchString(name) || strings.HasPrefix(name, "_") || name == "README" {
		return nil, true
	}
	if r.Exists(name) {
		return []string{name}, true
	}
	reg := ""
	if given != nil {
		reg = *given
	} else {
		var v sql.NullString
		err := r.DB.QueryRow(`SELECT COALESCE(role,'') FROM agents WHERE name=?`, name).Scan(&v)
		if err != nil && err != sql.ErrNoRows {
			if strict {
				return nil, false
			}
		}
		reg = v.String
	}
	if reg != "" {
		if found := r.list(reg); len(found) > 0 {
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

// ProtectReason は protect_reason(設定 protect で保護されていれば理由と true)。role:<glob> の行は a の役割と比べる。
func (r *Roles) ProtectReason(a string) (string, bool) {
	body, _, ok := config.Lookup(r.DB, r.Session, "protect")
	if !ok {
		return "", false
	}
	var have []string
	loaded := false
	for _, line := range config.Lines(body) {
		line = config.Trim(config.StripComment(line))
		if line == "" {
			continue
		}
		k, v, has := strings.Cut(line, "=")
		k = config.Trim(k)
		if has {
			v = config.Trim(v)
		}
		why := v
		if why == "" {
			why = "protect: " + k
		}
		if strings.HasPrefix(k, "role:") {
			if !loaded {
				loaded = true
				if s, _ := r.NameOf(a); s != "" {
					have = strings.Split(s, ",")
				}
			}
			for _, x := range have {
				if util.Glob(k[len("role:"):], x) {
					return why, true
				}
			}
		} else if util.Glob(k, a) {
			return why, true
		}
	}
	return "", false
}
