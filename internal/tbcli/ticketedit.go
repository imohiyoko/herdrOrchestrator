package tbcli

// チケットを作る・変えるコマンド(taskboard/lib/tasks.sh の add / update)と、その小道具(ラベル・外部リンク・親子の規則)。

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

var (
	ticketTypes = []string{"epic", "story", "task", "bug", "subtask"}
	priorities  = []string{"highest", "high", "medium", "low", "lowest"}
	reRefKV     = regexp.MustCompile(`^([a-z][a-z0-9_-]*)=(.*)$`)
	reRefKind   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,19}$`)
	reRefURL    = regexp.MustCompile(`^https?://[^ \t\n\v\f\r"'<>]+$`)
	rePoints    = regexp.MustCompile(`^[0-9]{1,4}$`)
)

func validType(v string) error {
	if !slices.Contains(ticketTypes, v) {
		return usage("種別は %s のどれか: %s", strings.Join(ticketTypes, " "), v)
	}
	return nil
}

func validPriority(v string) error {
	if !slices.Contains(priorities, v) {
		return usage("優先度は %s のどれか: %s", strings.Join(priorities, " "), v)
	}
	return nil
}

func validPoints(v string) error {
	if !rePoints.MatchString(v) {
		return usage("ポイントは 0〜9999 の整数: %s", v)
	}
	return nil
}

// badLabelChars は valid_label の [[:space:],\"\'](ラベルに使えない文字)。
func badLabelChars(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return config.IsSpace(r) || r == ',' || r == '"' || r == '\'' })
}

func validLabel(v string) error {
	if n := utf8.RuneCountInString(v); n < 1 || n > 40 || badLabelChars(v) {
		return usage("ラベルは 1〜40 文字で、空白・カンマ・引用符を含まない: %s", v)
	}
	return nil
}

func validRefURL(v string) error {
	if !reRefURL.MatchString(v) || utf8.RuneCountInString(v) > 500 {
		return usage("外部リンクは http(s):// で始まる 500 文字までの URL(空白・引用符・<> なし): %s", v)
	}
	return nil
}

// labelsJSON は labels_json: 引数(それぞれカンマ区切り可)→ ラベルの配列(重複なし・辞書順)。
func labelsJSON(args []string) ([]string, error) {
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
			if err := validLabel(p); err != nil {
				return nil, err
			}
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

func strArray(ss []string) string {
	a := make([]any, len(ss))
	for i, s := range ss {
		a[i] = s
	}
	return jv.Compact(a)
}

// refsJSON は refs_json: 引数(<種類>=<URL> か URL)→ [{kind,url}](url で重複なし、先に書いたものを残す)。
func refsJSON(args []string) (string, error) {
	out := []any{}
	seen := map[string]bool{}
	for _, a := range args {
		if blank(a) {
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
			return "", usage("外部リンクの種類は英小文字で始まる 20 文字までの英数字・_ -: %s", kind)
		}
		if err := validRefURL(url); err != nil {
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
func systemLabel(db *store.DB, stderr io.Writer, l string) bool {
	if l == "alert" || l == "proposal" || l == "rejected" || strings.HasPrefix(l, "role:") {
		return true
	}
	return l == systemLabelOf(db, stderr, "deadline")
}

// sysLabels は system_label_of が読んだ設定 system-labels(1 回の tb で 1 度だけ読む。警告も 1 度だけ)。
var sysLabels map[string]string

// systemLabelOf は system_label_of <用途>(team の {team} は置き換えない)。
func systemLabelOf(db *store.DB, stderr io.Writer, use string) string {
	if sysLabels == nil {
		sysLabels = map[string]string{}
		for k, v := range tickets.SystemLabelBuiltin {
			sysLabels[k] = v
		}
		if body, _, ok := config.Lookup(db.DB, db.Session, "system-labels"); ok {
			if tickets.ValidateSystemLabels(body) == "" {
				for _, line := range config.Lines(body) {
					line = config.Trim(config.StripComment(line))
					if k, v, ok := strings.Cut(line, "="); ok {
						sysLabels[config.Trim(k)] = config.Trim(v)
					}
				}
			} else {
				fmt.Fprintln(stderr, "tb: 設定 system-labels に不正な行があるので組み込みのラベルを使います(tb config edit system-labels で直す)")
			}
		}
	}
	return sysLabels[use]
}

// labelsCheckDefined は labels_check_defined: 設定 labels があれば、付けるラベルが用意したものかを確かめる。
func labelsCheckDefined(db *store.DB, stderr io.Writer, labels []string) error {
	if len(labels) == 0 {
		return nil
	}
	defs, ok := definedLabels(db)
	if !ok {
		return nil
	}
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d[0]
	}
	var bad []string
	for _, l := range labels {
		if systemLabel(db, stderr, l) || slices.Contains(names, l) {
			continue
		}
		bad = append(bad, l)
	}
	if len(bad) == 0 {
		return nil
	}
	return usage("用意されていないラベルです: %s(使えるのは: %s。一覧は tb labels --defined、足すのは人が tb config edit labels)", strings.Join(bad, " "), strings.Join(names, " "))
}

// checkHierarchy は check_hierarchy: 種別と親の組み合わせ(self は update のときの自分)。
func checkHierarchy(db *sql.DB, typ, parent, self string) error {
	if parent != "" {
		if typ == "epic" {
			return usage("epic は親を持てません")
		}
		var ptype string
		_ = db.QueryRow(`SELECT type FROM tasks WHERE id=?`, parent).Scan(&ptype)
		if ptype == "subtask" {
			return usage("subtask は親にできません: %s", parent)
		}
		if typ == "subtask" && ptype == "epic" {
			return usage("subtask の親は story / task / bug にする(epic の直下には置けない): %s", parent)
		}
	} else if typ == "subtask" {
		return usage("subtask には --parent が必要です")
	}
	count := func(q string) int {
		var n int
		_ = db.QueryRow(q, self).Scan(&n)
		return n
	}
	if self != "" && typ == "subtask" && count(`SELECT COUNT(*) FROM tasks WHERE parent_id=?`) != 0 {
		return usage("子のあるタスクは subtask にできません: %s", self)
	}
	if self != "" && typ == "epic" && count(`SELECT COUNT(*) FROM tasks WHERE parent_id=? AND type='subtask'`) != 0 {
		return usage("subtask の子があるタスクは epic にできません: %s", self)
	}
	return nil
}

// parentCycleSQL は parent_cycle_sql: 親をたどって self に着くなら真。
func parentCycleSQL(parent, self string) string {
	return fmt.Sprintf("EXISTS (WITH RECURSIVE up(id) AS (SELECT %s UNION SELECT t.parent_id FROM tasks t JOIN up ON t.id=up.id WHERE t.parent_id IS NOT NULL) SELECT 1 FROM up WHERE id=%s)", store.Quote(parent), store.Quote(self))
}

// hierarchyOKSQL は hierarchy_ok_sql: check_hierarchy と同じ規則を書き込みの条件にする。
func hierarchyOKSQL(typ, parent, self string) string {
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
			c += " AND NOT " + parentCycleSQL(parent, self)
		}
	}
	return "(" + c + ")"
}

// cmdAdd は tb add -p <project> -t <title> [...] → ID。
func cmdAdd(args []string, w, stderr io.Writer) error {
	db, st, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	var project, title, parent, owner, session, account, desc, due, sprint, points string
	typ, priority := "task", "medium"
	var labs, refs []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-p", "--project", "-t", "--title", "--type", "--priority", "-l", "--label", "--desc", "--description",
			"--ref", "--due", "--sprint", "--points", "--parent", "--owner", "--session", "--account":
		default:
			return usage("add: 不明な引数 %s", a)
		}
		v, err := argv(args, i)
		if err != nil {
			return err
		}
		i++
		switch a {
		case "-p", "--project":
			project = v
		case "-t", "--title":
			title = v
		case "--type":
			if err := validType(v); err != nil {
				return err
			}
			typ = v
		case "--priority":
			if err := validPriority(v); err != nil {
				return err
			}
			priority = v
		case "-l", "--label":
			labs = append(labs, v)
		case "--desc", "--description":
			desc = v
		case "--ref":
			refs = append(refs, v)
		case "--due":
			if v != "" && !validDue(v) {
				return usage("期日は YYYY-MM-DD の日付: %s", v)
			}
			due = v
		case "--sprint":
			sprint = v
		case "--points":
			if err := validPoints(v); err != nil {
				return err
			}
			points = v
		case "--parent":
			parent = v
		case "--owner":
			owner = v
		case "--session":
			session = v
		case "--account":
			account = v
		}
	}
	if project == "" {
		return usage("add: -p <project> が必要です")
	}
	if err := needProject(project); err != nil {
		return err
	}
	if blank(title) {
		return usage("add: -t <title> が必要です")
	}
	if parent != "" {
		if err := needID(parent); err != nil {
			return err
		}
		if err := requireTask(db, parent); err != nil {
			return err
		}
	}
	if err := checkHierarchy(db.DB, typ, parent, ""); err != nil {
		return err
	}
	sid := ""
	if sprint != "" {
		if sid, err = sprintForTask(db.DB, sprint, project); err != nil {
			return err
		}
	}
	lj, err := labelsJSON(labs)
	if err != nil {
		return err
	}
	rj, err := refsJSON(refs)
	if err != nil {
		return err
	}
	if err := labelsCheckDefined(db, stderr, lj); err != nil {
		return err
	}
	if err := eventActorResolve(db); err != nil {
		return err
	}
	// 採番と挿入を 1 文で行い、同時 add でも重複しないようにする。親の種別の規則も同じ文で確かめる(HAVING)
	// ID は DB 全体で一意(採番は全セッションの行を見る。ここだけ herdr_session で絞らない)
	cond := hierarchyOKSQL(typ, parent, "")
	if sid != "" {
		cond += " AND EXISTS (SELECT 1 FROM sprints WHERE id=" + sid + " AND state!='closed')"
	}
	n := now()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	db.RaceHook()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	commit := false
	defer func() {
		if !commit {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()
	r, err := conn.ExecContext(ctx, `INSERT INTO main.tasks (id, project, title, status, owner_agent, session_id, account, parent_id, type, priority, labels, description, refs, due, sprint_id, points, created_at, updated_at, herdr_session)
		SELECT 'T-'||?1||'-'||printf('%03d', COALESCE(MAX(CAST(substr(id, length(?1)+4) AS INTEGER)),0)+1),
		       ?1, ?2, ?3, NULLIF(?4,''), NULLIF(?5,''), NULLIF(?6,''), NULLIF(?7,''), ?8, ?9, ?10, NULLIF(?11,''), ?12, NULLIF(?13,''),
		       CAST(NULLIF(?14,'') AS INTEGER), CAST(NULLIF(?15,'') AS INTEGER), ?16, ?16, ?17
		FROM main.tasks WHERE project=?1 HAVING `+cond,
		project, title, st.Main("todo"), owner, session, account, parent, typ, priority, strArray(lj), desc, rj, due, sid, points, n, db.Session)
	if err != nil {
		return err
	}
	var id string
	if c, _ := r.RowsAffected(); c == 1 {
		if rowid, err := r.LastInsertId(); err == nil {
			_ = conn.QueryRowContext(ctx, `SELECT id FROM main.tasks WHERE rowid=?`, rowid).Scan(&id)
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	commit = true
	conn.Close()
	if id == "" {
		x := parent
		if x == "" {
			x = sprint
		}
		return usage("add: 親の種別かスプリントが規則に合いません(並行して変わりました): %s", x)
	}
	fmt.Fprintln(w, id)
	rulesHook(db, st)
	return nil
}

// statusEnterSets は status_enter_sets: 分類に入ったときだけ完了報告の列を合わせる。
func statusEnterSets(st tickets.StatusSet, to, cur string) string {
	if st.Is(to, st.Cat(cur)) {
		return ""
	}
	if st.Is(to, "done") {
		return "reported_at=NULL"
	}
	if st.Is(to, "cancelled") {
		return "reported_at=" + store.Quote(now())
	}
	return ""
}

// statusEnteredHook は status_entered_hook: done の分類に入ったら done の通知。
func statusEnteredHook(db *store.DB, st tickets.StatusSet, id, to, cur string) {
	if st.Is(to, "done") && !st.Is(cur, "done") {
		taskDoneHook(db, id)
	}
}

const updateUsage = "update <id> [--status S] [--title T] [--owner N] [--session S] [--account A] [--parent ID] [--type T] [--priority P] [--desc D] [--due YYYY-MM-DD|''] [--label L ...] [--add-label L] [--remove-label L]"

// cmdUpdate は tb update <id> [...]。
func cmdUpdate(args []string, w, stderr io.Writer) error {
	db, st, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(args) < 1 {
		return usage(updateUsage)
	}
	id := args[0]
	if err := needID(id); err != nil {
		return err
	}
	q := store.Quote
	var sets, setlab, addlab, rmlab, addref, rmref []string
	var ntype, nparent, nstatus string
	relabel, nsprint, hasSprint := false, "", false
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--status", "--title", "--owner", "--session", "--account", "--parent", "--type", "--priority", "--desc", "--description",
			"--due", "-l", "--label", "--add-label", "--remove-label", "--add-ref", "--sprint", "--points", "--remove-ref":
		default:
			return usage("update: 不明な引数 %s", a)
		}
		v, err := argv(args, i)
		if err != nil {
			return err
		}
		i++
		switch a {
		case "--status":
			if err := oneOf(st.Names, v, "status"); err != nil {
				return err
			}
			nstatus = v
			sets = append(sets, "status="+q(v))
		case "--title":
			if blank(v) {
				return usage("update: title が空です")
			}
			sets = append(sets, "title="+q(v))
		case "--owner":
			sets = append(sets, "owner_agent=NULLIF("+q(v)+",'')")
		case "--session":
			sets = append(sets, "session_id=NULLIF("+q(v)+",'')")
		case "--account":
			sets = append(sets, "account=NULLIF("+q(v)+",'')")
		case "--parent":
			if err := needID(v); err != nil {
				return err
			}
			if err := requireTask(db, v); err != nil {
				return err
			}
			if v == id {
				return usage("update: 自分自身を parent にできません")
			}
			nparent = v
			sets = append(sets, "parent_id="+q(v))
		case "--type":
			if err := validType(v); err != nil {
				return err
			}
			ntype = v
			sets = append(sets, "type="+q(v))
		case "--priority":
			if err := validPriority(v); err != nil {
				return err
			}
			sets = append(sets, "priority="+q(v))
		case "--desc", "--description":
			sets = append(sets, "description=NULLIF("+q(v)+",'')")
		case "--due":
			if v != "" && !validDue(v) {
				return usage("期日は YYYY-MM-DD の日付: %s", v)
			}
			sets = append(sets, "due=NULLIF("+q(v)+",'')")
		case "-l", "--label":
			relabel = true
			setlab = append(setlab, v)
		case "--add-label":
			addlab = append(addlab, v)
		case "--remove-label":
			rmlab = append(rmlab, v)
		case "--add-ref":
			addref = append(addref, v)
		case "--sprint":
			nsprint, hasSprint = v, v != "-" // bash と同じく - は指定なし
		case "--points":
			if v != "" {
				if err := validPoints(v); err != nil {
					return err
				}
			}
			sets = append(sets, "points=CAST(NULLIF("+q(v)+",'') AS INTEGER)")
		case "--remove-ref":
			if err := validRefURL(v); err != nil {
				return err
			}
			rmref = append(rmref, v)
		}
	}
	if len(sets) == 0 && !relabel && len(addlab) == 0 && len(rmlab) == 0 && len(addref) == 0 && len(rmref) == 0 && !hasSprint {
		return usage("update: 変更する項目がありません")
	}
	var cur, project, rtype string
	var rparent sql.NullString
	err = db.QueryRow(`SELECT status, project, type, parent_id FROM tasks WHERE id=?`, id).Scan(&cur, &project, &rtype, &rparent)
	if err == sql.ErrNoRows {
		return die("タスクが見つかりません: %s", id)
	}
	if err != nil {
		return err
	}
	if nstatus != "" {
		if err := specialTicketGuard(db.DB, id, "tb update --status"); err != nil {
			return err
		}
		if es := statusEnterSets(st, nstatus, cur); es != "" {
			sets = append(sets, es)
		}
	}
	sguard := ""
	if hasSprint {
		if nsprint == "" {
			sets = append(sets, "sprint_id=NULL")
		} else {
			sn, err := sprintForTask(db.DB, nsprint, project)
			if err != nil {
				return err
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
		if err := checkHierarchy(db.DB, ftype, fparent, id); err != nil {
			return err
		}
	}
	if relabel || len(addlab) > 0 || len(rmlab) > 0 {
		// 付け外しは書く時点の labels に当てる(並行する --add-label を互いに消さない)
		base := "labels"
		if relabel {
			sj, err := labelsJSON(setlab)
			if err != nil {
				return err
			}
			if err := labelsCheckDefined(db, stderr, sj); err != nil {
				return err
			}
			base = q(strArray(sj))
		}
		add, err := labelsJSON(addlab)
		if err != nil {
			return err
		}
		rm, err := labelsJSON(rmlab)
		if err != nil {
			return err
		}
		if err := labelsCheckDefined(db, stderr, add); err != nil {
			return err
		}
		sets = append(sets, "labels=(SELECT json_group_array(value) FROM (SELECT value FROM (SELECT value FROM json_each("+base+") UNION SELECT value FROM json_each("+q(strArray(add))+")) WHERE value NOT IN (SELECT value FROM json_each("+q(strArray(rm))+")) ORDER BY value))")
	}
	if len(addref) > 0 || len(rmref) > 0 {
		// 外部リンクも書く時点の refs に当てる。同じ URL を足すと種類を置き換える
		ra, err := refsJSON(addref)
		if err != nil {
			return err
		}
		rr := strArray(rmref)
		sets = append(sets, "refs=(SELECT json_group_array(json(v)) FROM ( SELECT value AS v FROM json_each(refs) WHERE json_extract(value,'$.url') NOT IN (SELECT value FROM json_each("+q(rr)+")) AND json_extract(value,'$.url') NOT IN (SELECT json_extract(value,'$.url') FROM json_each("+q(ra)+")) UNION ALL SELECT value FROM json_each("+q(ra)+")))")
	}
	// 種別・親を変えるときは、規則(循環を含む)の検査と書き込みを 1 つの書き込みトランザクションで行う
	guard := ""
	if ntype != "" || nparent != "" {
		// 指定しなかった種別・親は読んだときの値で検査したので、書く時点でも変わっていないことを条件にする
		guard = " AND " + hierarchyOKSQL(ftype, fparent, id) + " AND type=" + q(rtype) + " AND parent_id IS NULLIF(" + q(rparent.String) + ",'')"
	}
	if err := eventActorResolve(db); err != nil {
		return err
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	db.RaceHook()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	commit := false
	defer func() {
		if !commit {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()
	r, err := conn.ExecContext(ctx, "UPDATE main.tasks SET "+strings.Join(sets, ",")+", updated_at=? WHERE herdr_session=? AND id=?"+guard+sguard, now(), db.Session, id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	commit = true
	conn.Close()
	if n != 1 {
		if sguard != "" {
			var one int
			if db.QueryRow(`SELECT 1 FROM sprints WHERE id=`+strings.TrimPrefix(nsprint, "S-")+` AND state!='closed'`).Scan(&one) != nil {
				return usage("閉じたスプリントには入れられません: %s", nsprint)
			}
		}
		if fparent != "" {
			var one int
			if db.QueryRow(`SELECT 1 WHERE `+parentCycleSQL(fparent, id)).Scan(&one) == nil {
				return usage("親子が循環します: %s → %s", id, fparent)
			}
		}
		return usage("update: 種別・親が規則に合いません(並行して変わりました): %s", id)
	}
	if nstatus != "" {
		statusEnteredHook(db, st, id, nstatus, cur)
	}
	rulesHook(db, st)
	fmt.Fprintf(w, "%s updated\n", id)
	return nil
}
