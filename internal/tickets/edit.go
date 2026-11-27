package tickets

// チケットを作る・変える中身(tb add / update)と、その小道具(ラベル・外部リンク・親子の規則)。

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

var (
	reRefKV   = regexp.MustCompile(`^([a-z][a-z0-9_-]*)=(.*)$`)
	reRefKind = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,19}$`)
	reRefURL  = regexp.MustCompile(`^https?://[^ \t\n\v\f\r"'<>]+$`)
	rePoints  = regexp.MustCompile(`^[0-9]{1,4}$`)
)

func ValidType(v string) error {
	if !slices.Contains(Types, v) {
		return invalid("種別は %s のどれか: %s", strings.Join(Types, " "), v)
	}
	return nil
}

func ValidPriority(v string) error {
	if !slices.Contains(Priorities, v) {
		return invalid("優先度は %s のどれか: %s", strings.Join(Priorities, " "), v)
	}
	return nil
}

func ValidPoints(v string) error {
	if !rePoints.MatchString(v) {
		return invalid("ポイントは 0〜9999 の整数: %s", v)
	}
	return nil
}

// badLabelChars は valid_label の [[:space:],\"\'](ラベルに使えない文字)。
func badLabelChars(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return config.IsSpace(r) || r == ',' || r == '"' || r == '\'' })
}

func ValidLabel(v string) error {
	if n := utf8.RuneCountInString(v); n < 1 || n > 40 || badLabelChars(v) {
		return invalid("ラベルは 1〜40 文字で、空白・カンマ・引用符を含まない: %s", v)
	}
	return nil
}

func ValidRefURL(v string) error {
	if !reRefURL.MatchString(v) || utf8.RuneCountInString(v) > 500 {
		return invalid("外部リンクは http(s):// で始まる 500 文字までの URL(空白・引用符・<> なし): %s", v)
	}
	return nil
}

// LabelsJSON は labels_json: 引数(それぞれカンマ区切り可)→ ラベルの配列(重複なし・辞書順)。
func LabelsJSON(args []string) ([]string, error) {
	var out []string
	for _, a := range args {
		// bash は read -ra で読むので、改行があれば先頭の行だけ
		if i := strings.IndexByte(a, '\n'); i >= 0 {
			a = a[:i]
		}
		for _, p := range strings.Split(a, ",") {
			p = config.Trim(p)
			if p == "" {
				continue
			}
			if err := ValidLabel(p); err != nil {
				return nil, err
			}
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// RefsJSON は refs_json: 引数(<種類>=<URL> か URL)→ [{kind,url}](url で重複なし、先に書いたものを残す)。
func RefsJSON(args []string) (string, error) {
	out := []any{}
	seen := map[string]bool{}
	for _, a := range args {
		if strings.TrimFunc(a, config.IsSpace) == "" {
			continue
		}
		var kind, url string
		if m := reRefKV.FindStringSubmatch(a); m != nil {
			kind, url = m[1], m[2]
		} else {
			url = a
			switch {
			case strings.HasPrefix(url, "https://") && strings.Contains(url[len("https://"):], "slack.com/"):
				kind = "slack"
			case strings.HasPrefix(url, "https://github.com/"):
				kind = "github"
			default:
				kind = "link"
			}
		}
		if !reRefKind.MatchString(kind) {
			return "", invalid("外部リンクの種類は英小文字で始まる 20 文字までの英数字・_ -: %s", kind)
		}
		if err := ValidRefURL(url); err != nil {
			return "", err
		}
		if !seen[url] {
			seen[url] = true
			out = append(out, jv.Obj{{K: "kind", V: kind}, {K: "url", V: url}})
		}
	}
	return jv.Compact(out), nil
}

// systemLabels は system_label: tb 自身が付けるラベル(設定 labels に書かなくても付けられる)。
func SystemLabel(db *store.DB, warn io.Writer, l string) bool {
	if l == "alert" || l == "proposal" || l == "rejected" || strings.HasPrefix(l, "role:") {
		return true
	}
	return l == SystemLabelOf(db, warn, "deadline")
}

// sysLabels は system_label_of が読んだ設定 system-labels(1 つのプロセスで 1 度だけ読む。警告も 1 度だけ)。
// tb は 1 回ごとにプロセスが変わる。Web UI のように常駐する入口では、設定を変えたら再起動まで前の値になる(tb と同じく 1 度だけ読む)。
var sysLabels map[string]string

// SystemLabelOf は system_label_of <用途>(team の {team} は置き換えない)。
func SystemLabelOf(db *store.DB, warn io.Writer, use string) string {
	if sysLabels == nil {
		sysLabels = map[string]string{}
		for k, v := range SystemLabelBuiltin {
			sysLabels[k] = v
		}
		if body, _, ok := config.Lookup(db.DB, db.Session, "system-labels"); ok {
			if ValidateSystemLabels(body) == "" {
				for _, line := range config.Lines(body) {
					line = config.Trim(config.StripComment(line))
					if k, v, ok := strings.Cut(line, "="); ok {
						sysLabels[config.Trim(k)] = config.Trim(v)
					}
				}
			} else {
				fmt.Fprintln(warn, "tb: 設定 system-labels に不正な行があるので組み込みのラベルを使います(tb config edit system-labels で直す)")
			}
		}
	}
	return sysLabels[use]
}

// LabelsCheckDefined は labels_check_defined: 設定 labels があれば、付けるラベルが用意したものかを確かめる。
func LabelsCheckDefined(db *store.DB, warn io.Writer, labels []string) error {
	if len(labels) == 0 {
		return nil
	}
	defs, ok := DefinedLabels(db)
	if !ok {
		return nil
	}
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d[0]
	}
	var bad []string
	for _, l := range labels {
		if SystemLabel(db, warn, l) || slices.Contains(names, l) {
			continue
		}
		bad = append(bad, l)
	}
	if len(bad) == 0 {
		return nil
	}
	return invalid("用意されていないラベルです: %s(使えるのは: %s。一覧は tb labels --defined、足すのは人が tb config edit labels)", strings.Join(bad, " "), strings.Join(names, " "))
}

// CheckHierarchy は check_hierarchy: 種別と親の組み合わせ(self は update のときの自分)。
func CheckHierarchy(db *sql.DB, typ, parent, self string) error {
	if parent != "" {
		if typ == "epic" {
			return invalid("epic は親を持てません")
		}
		var ptype string
		_ = db.QueryRow(`SELECT type FROM tasks WHERE id=?`, parent).Scan(&ptype)
		if ptype == "subtask" {
			return invalid("subtask は親にできません: %s", parent)
		}
		if typ == "subtask" && ptype == "epic" {
			return invalid("subtask の親は story / task / bug にする(epic の直下には置けない): %s", parent)
		}
	} else if typ == "subtask" {
		return invalid("subtask には --parent が必要です")
	}
	count := func(q string) int {
		var n int
		_ = db.QueryRow(q, self).Scan(&n)
		return n
	}
	if self != "" && typ == "subtask" && count(`SELECT COUNT(*) FROM tasks WHERE parent_id=?`) != 0 {
		return invalid("子のあるタスクは subtask にできません: %s", self)
	}
	if self != "" && typ == "epic" && count(`SELECT COUNT(*) FROM tasks WHERE parent_id=? AND type='subtask'`) != 0 {
		return invalid("subtask の子があるタスクは epic にできません: %s", self)
	}
	return nil
}

// ParentCycleSQL は parent_cycle_sql: 親をたどって self に着くなら真。
func ParentCycleSQL(parent, self string) string {
	return fmt.Sprintf("EXISTS (WITH RECURSIVE up(id) AS (SELECT %s UNION SELECT t.parent_id FROM tasks t JOIN up ON t.id=up.id WHERE t.parent_id IS NOT NULL) SELECT 1 FROM up WHERE id=%s)", store.Quote(parent), store.Quote(self))
}

// HierarchyOKSQL は hierarchy_ok_sql: check_hierarchy と同じ規則を書き込みの条件にする。
func HierarchyOKSQL(typ, parent, self string) string {
	c := "1"
	if parent != "" {
		c += fmt.Sprintf(" AND NOT EXISTS (SELECT 1 FROM tasks WHERE id=%s AND (type='subtask' OR (%s='subtask' AND type='epic')))", store.Quote(parent), store.Quote(typ))
	}
	if self != "" {
		switch typ {
		case "subtask":
			c += fmt.Sprintf(" AND NOT EXISTS (SELECT 1 FROM tasks WHERE parent_id=%s)", store.Quote(self))
		case "epic":
			c += fmt.Sprintf(" AND NOT EXISTS (SELECT 1 FROM tasks WHERE parent_id=%s AND type='subtask')", store.Quote(self))
		}
		if parent != "" {
			c += " AND NOT " + ParentCycleSQL(parent, self)
		}
	}
	return "(" + c + ")"
}

// DefinedLabels は設定 labels の (名前, 説明)。設定が無ければ false。
func DefinedLabels(db *store.DB) ([][2]string, bool) {
	body, _, ok := config.Lookup(db.DB, db.Session, "labels")
	if !ok {
		return nil, false
	}
	var out [][2]string
	for _, line := range strings.Split(body, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = config.Trim(line)
		if line == "" {
			continue
		}
		k, v, has := strings.Cut(line, "=")
		k = config.Trim(k)
		if has {
			v = config.Trim(v)
		}
		out = append(out, [2]string{k, v})
	}
	return out, true
}

var reDue = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)

// ValidDue は YYYY-MM-DD の日付か。
func ValidDue(s string) bool {
	if !reDue.MatchString(s) {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// NeedDue は --due などの値の検査(空はよい)。
func NeedDue(v string) error {
	if v != "" && !ValidDue(v) {
		return invalid("期日は YYYY-MM-DD の日付: %s", v)
	}
	return nil
}

// AddSpec は新しいチケット(tb add の引数)。Type と Priority は空なら task / medium。
type AddSpec struct {
	Project, Title, Type, Priority, Desc, Due, Sprint, Points, Parent, Owner, Session, Account string
	Labels, Refs                                                                               []string
}

// Add はチケットを作り、ID を返す。warn は設定 system-labels が不正なときの警告の書き込み先。
// 採番と挿入を 1 文で行い、同時 add でも重複しないようにする。親の種別の規則も同じ文で確かめる(HAVING)。
func Add(db *store.DB, st StatusSet, sp AddSpec, warn io.Writer, now string) (string, error) {
	if sp.Type == "" {
		sp.Type = "task"
	}
	if sp.Priority == "" {
		sp.Priority = "medium"
	}
	if err := ValidType(sp.Type); err != nil {
		return "", err
	}
	if err := ValidPriority(sp.Priority); err != nil {
		return "", err
	}
	if err := NeedDue(sp.Due); err != nil {
		return "", err
	}
	if sp.Points != "" {
		if err := ValidPoints(sp.Points); err != nil {
			return "", err
		}
	}
	if sp.Project == "" {
		return "", invalid("add: -p <project> が必要です")
	}
	if err := NeedProject(sp.Project); err != nil {
		return "", err
	}
	if strings.TrimFunc(sp.Title, config.IsSpace) == "" {
		return "", invalid("add: -t <title> が必要です")
	}
	if sp.Parent != "" {
		if err := NeedID(sp.Parent); err != nil {
			return "", err
		}
		if err := requireTask(db, sp.Parent); err != nil {
			return "", err
		}
	}
	if err := CheckHierarchy(db.DB, sp.Type, sp.Parent, ""); err != nil {
		return "", err
	}
	sid := ""
	if sp.Sprint != "" {
		var err error
		if sid, err = SprintForTask(db.DB, sp.Sprint, sp.Project); err != nil {
			return "", err
		}
	}
	lj, err := LabelsJSON(sp.Labels)
	if err != nil {
		return "", err
	}
	rj, err := RefsJSON(sp.Refs)
	if err != nil {
		return "", err
	}
	if err := LabelsCheckDefined(db, warn, lj); err != nil {
		return "", err
	}
	// ID は DB 全体で一意(採番は全セッションの行を見る。ここだけ herdr_session で絞らない)
	cond := HierarchyOKSQL(sp.Type, sp.Parent, "")
	if sid != "" {
		cond += " AND EXISTS (SELECT 1 FROM sprints WHERE id=" + sid + " AND state!='closed')"
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	db.RaceHook()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return "", err
	}
	commit := false
	defer func() {
		if !commit {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()
	r, err := conn.ExecContext(ctx, `INSERT INTO main.tasks (id, project, title, status, owner_agent, session_id, account, parent_id, type, priority, labels, description, refs, due, sprint_id, points, created_at, updated_at, herdr_session)
		SELECT 'T-'||?1||'-'||printf('%03d', MAX(COALESCE(MAX(CAST(substr(id, length(?1)+4) AS INTEGER)),0),
		         COALESCE((SELECT MAX(CAST(value AS INTEGER)) FROM main.tb_flags WHERE key='ticket-rm-max:'||?1),0))+1),
		       ?1, ?2, ?3, NULLIF(?4,''), NULLIF(?5,''), NULLIF(?6,''), NULLIF(?7,''), ?8, ?9, ?10, NULLIF(?11,''), ?12, NULLIF(?13,''),
		       CAST(NULLIF(?14,'') AS INTEGER), CAST(NULLIF(?15,'') AS INTEGER), ?16, ?16, ?17
		FROM main.tasks WHERE project=?1 HAVING `+cond,
		sp.Project, sp.Title, st.Main("todo"), sp.Owner, sp.Session, sp.Account, sp.Parent, sp.Type, sp.Priority, StrArray(lj), sp.Desc, rj, sp.Due, sid, sp.Points, now, db.Session)
	if err != nil {
		return "", err
	}
	var id string
	if c, _ := r.RowsAffected(); c == 1 {
		if rowid, err := r.LastInsertId(); err == nil {
			_ = conn.QueryRowContext(ctx, `SELECT id FROM main.tasks WHERE rowid=?`, rowid).Scan(&id)
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return "", err
	}
	commit = true
	if id == "" {
		x := sp.Parent
		if x == "" {
			x = sp.Sprint
		}
		return "", invalid("add: 親の種別かスプリントが規則に合いません(並行して変わりました): %s", x)
	}
	return id, nil
}

// Change は tb update の 1 つの指定(Field は引数の名前から - を除いたもの: status title owner session account parent
// type priority desc due label add-label remove-label add-ref remove-ref sprint points)。指定の順に確かめる。
type Change struct {
	Field string `api:"field" json:"field"`
	Value string `api:"value" json:"value"`
}

// UpdateFields は Change.Field に使える名前。
var UpdateFields = []string{"status", "title", "owner", "session", "account", "parent", "type", "priority", "desc", "due",
	"label", "add-label", "remove-label", "add-ref", "remove-ref", "sprint", "points"}

// Updated は Update の結果: 前の状態と、状態を変えたならその状態。
type Updated struct {
	From, Status string
}

// Update はチケット id を changes のとおりに変える(tb update)。
func Update(db *store.DB, st StatusSet, id string, changes []Change, warn io.Writer, now string) (Updated, error) {
	if err := NeedID(id); err != nil {
		return Updated{}, err
	}
	q := store.Quote
	var sets, setlab, addlab, rmlab, addref, rmref []string
	var ntype, nparent, nstatus string
	relabel, nsprint, hasSprint := false, "", false
	for _, c := range changes {
		v := c.Value
		switch c.Field {
		case "status":
			if !slices.Contains(st.Names, v) {
				return Updated{}, invalid("status は %s のどれか: %s", strings.Join(st.Names, " "), v)
			}
			nstatus = v
			sets = append(sets, "status="+q(v))
		case "title":
			if strings.TrimFunc(v, config.IsSpace) == "" {
				return Updated{}, invalid("update: title が空です")
			}
			sets = append(sets, "title="+q(v))
		case "owner":
			sets = append(sets, "owner_agent=NULLIF("+q(v)+",'')")
		case "session":
			sets = append(sets, "session_id=NULLIF("+q(v)+",'')")
		case "account":
			sets = append(sets, "account=NULLIF("+q(v)+",'')")
		case "parent":
			if err := NeedID(v); err != nil {
				return Updated{}, err
			}
			if err := requireTask(db, v); err != nil {
				return Updated{}, err
			}
			if v == id {
				return Updated{}, invalid("update: 自分自身を parent にできません")
			}
			nparent = v
			sets = append(sets, "parent_id="+q(v))
		case "type":
			if err := ValidType(v); err != nil {
				return Updated{}, err
			}
			ntype = v
			sets = append(sets, "type="+q(v))
		case "priority":
			if err := ValidPriority(v); err != nil {
				return Updated{}, err
			}
			sets = append(sets, "priority="+q(v))
		case "desc":
			sets = append(sets, "description=NULLIF("+q(v)+",'')")
		case "due":
			if err := NeedDue(v); err != nil {
				return Updated{}, err
			}
			sets = append(sets, "due=NULLIF("+q(v)+",'')")
		case "label":
			relabel = true
			setlab = append(setlab, v)
		case "add-label":
			addlab = append(addlab, v)
		case "remove-label":
			rmlab = append(rmlab, v)
		case "add-ref":
			addref = append(addref, v)
		case "sprint":
			nsprint, hasSprint = v, v != "-" // bash と同じく - は指定なし
		case "points":
			if v != "" {
				if err := ValidPoints(v); err != nil {
					return Updated{}, err
				}
			}
			sets = append(sets, "points=CAST(NULLIF("+q(v)+",'') AS INTEGER)")
		case "remove-ref":
			if err := ValidRefURL(v); err != nil {
				return Updated{}, err
			}
			rmref = append(rmref, v)
		default:
			return Updated{}, invalid("update: 不明な引数 --%s", c.Field)
		}
	}
	if len(sets) == 0 && !relabel && len(addlab) == 0 && len(rmlab) == 0 && len(addref) == 0 && len(rmref) == 0 && !hasSprint {
		return Updated{}, invalid("update: 変更する項目がありません")
	}
	var cur, project, rtype string
	var rparent sql.NullString
	err := db.QueryRow(`SELECT status, project, type, parent_id FROM tasks WHERE id=?`, id).Scan(&cur, &project, &rtype, &rparent)
	if err == sql.ErrNoRows {
		return Updated{}, notFound(id)
	}
	if err != nil {
		return Updated{}, err
	}
	if nstatus != "" {
		if err := SpecialGuard(db.DB, id, "tb update --status"); err != nil {
			return Updated{}, err
		}
		if es := EnterSets(st, nstatus, cur, now); es != "" {
			sets = append(sets, es)
		}
	}
	sguard := ""
	if hasSprint {
		if nsprint == "" {
			sets = append(sets, "sprint_id=NULL")
		} else {
			sn, err := SprintForTask(db.DB, nsprint, project)
			if err != nil {
				return Updated{}, err
			}
			sets = append(sets, "sprint_id="+sn)
			sguard = " AND EXISTS (SELECT 1 FROM sprints WHERE id=" + sn + " AND state!='closed')" // 書く時点でも閉じていない
		}
	}
	ftype, fparent := rtype, rparent.String
	if ntype != "" {
		ftype = ntype
	}
	if nparent != "" {
		fparent = nparent
	}
	if ntype != "" || nparent != "" {
		if err := CheckHierarchy(db.DB, ftype, fparent, id); err != nil {
			return Updated{}, err
		}
	}
	if relabel || len(addlab) > 0 || len(rmlab) > 0 {
		// 付け外しは書く時点の labels に当てる(並行する --add-label を互いに消さない)
		base := "labels"
		if relabel {
			sj, err := LabelsJSON(setlab)
			if err != nil {
				return Updated{}, err
			}
			if err := LabelsCheckDefined(db, warn, sj); err != nil {
				return Updated{}, err
			}
			base = q(StrArray(sj))
		}
		add, err := LabelsJSON(addlab)
		if err != nil {
			return Updated{}, err
		}
		rm, err := LabelsJSON(rmlab)
		if err != nil {
			return Updated{}, err
		}
		if err := LabelsCheckDefined(db, warn, add); err != nil {
			return Updated{}, err
		}
		sets = append(sets, "labels=(SELECT json_group_array(value) FROM (SELECT value FROM (SELECT value FROM json_each("+base+") UNION SELECT value FROM json_each("+q(StrArray(add))+")) WHERE value NOT IN (SELECT value FROM json_each("+q(StrArray(rm))+")) ORDER BY value))")
	}
	if len(addref) > 0 || len(rmref) > 0 {
		// 外部リンクも書く時点の refs に当てる。同じ URL を足すと種類を置き換える
		ra, err := RefsJSON(addref)
		if err != nil {
			return Updated{}, err
		}
		rr := StrArray(rmref)
		sets = append(sets, "refs=(SELECT json_group_array(json(v)) FROM ( SELECT value AS v FROM json_each(refs) WHERE json_extract(value,'$.url') NOT IN (SELECT value FROM json_each("+q(rr)+")) AND json_extract(value,'$.url') NOT IN (SELECT json_extract(value,'$.url') FROM json_each("+q(ra)+")) UNION ALL SELECT value FROM json_each("+q(ra)+")))")
	}
	// 種別・親を変えるときは、規則(循環を含む)の検査と書き込みを 1 つの書き込みトランザクションで行う
	guard := ""
	if ntype != "" || nparent != "" {
		// 指定しなかった種別・親は読んだときの値で検査したので、書く時点でも変わっていないことを条件にする
		guard = " AND " + HierarchyOKSQL(ftype, fparent, id) + " AND type=" + q(rtype) + " AND parent_id IS NULLIF(" + q(rparent.String) + ",'')"
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return Updated{}, err
	}
	defer conn.Close()
	db.RaceHook()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return Updated{}, err
	}
	commit := false
	defer func() {
		if !commit {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()
	r, err := conn.ExecContext(ctx, "UPDATE main.tasks SET "+strings.Join(sets, ",")+", updated_at=? WHERE herdr_session=? AND id=?"+guard+sguard, now, db.Session, id)
	if err != nil {
		return Updated{}, err
	}
	n, _ := r.RowsAffected()
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return Updated{}, err
	}
	commit = true
	conn.Close()
	if n != 1 {
		if sguard != "" {
			var one int
			if db.QueryRow(`SELECT 1 FROM sprints WHERE id=`+strings.TrimPrefix(nsprint, "S-")+` AND state!='closed'`).Scan(&one) != nil {
				return Updated{}, invalid("閉じたスプリントには入れられません: %s", nsprint)
			}
		}
		if fparent != "" {
			var one int
			if db.QueryRow(`SELECT 1 WHERE `+ParentCycleSQL(fparent, id)).Scan(&one) == nil {
				return Updated{}, invalid("親子が循環します: %s → %s", id, fparent)
			}
		}
		return Updated{}, invalid("update: 種別・親が規則に合いません(並行して変わりました): %s", id)
	}
	return Updated{From: cur, Status: nstatus}, nil
}
