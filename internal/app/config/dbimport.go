package config

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// config.db.import(tb db-import。もとは taskboard/lib/dbimport.sh)。旧形式の DB(1 セッション = 1 DB、スキーマ v11 まで)を、共有の DB の 1 セッションとして取り込む。
// - 取り込むのは写し。元の DB は読むだけ
// - 写しを最新のスキーマまで移行し(012 で行の herdr_session が取り込み先のセッションになる)、1 つのトランザクションで共有の DB に足す
// - 整数の ID は共有の DB の最大値の後ろへずらし、ずらした番号を指す列と ref も直す
// - タスク ID がぶつかれば次の空き番号に付け替え、参照も直す(tb ticket rm で消した番号もぶつかるものとして扱う)
// - 名前やキーが主キーの表は共有の DB の行を優先する(agents は空の値だけ旧 DB で埋める)
// - 同じファイルを 2 回取り込まない(tb_flags の db-import:<実体のパス>)

// importIDTables は整数の ID を持つ表(<表>[.<列>]:<接頭辞>)。
var importIDTables = strings.Fields(`comments:C outbox:O alert_details.alert_no:A agent_ops:L notify_events:N digests:D proposal_details.proposal_no:P pr_queue:PQ
  proxy_requests: messages: reviews: read_log: role_changes: role_briefings: approval_changes: notify_sends:`)

// importTaskRefs はタスク ID を持つ列(表.列)。
var importTaskRefs = strings.Fields(`tasks.parent_id comments.task_id proxy_requests.task_id outbox.task_id pr_queue.task_id agents.spawn_task observations.task_id
  alert_details.task_id proposal_details.task_id`)

// importIDSplit は import_id_split: 表と番号の列と接頭辞。
func importIDSplit(t string) (tbl, idc, p string) {
	tbl, p, _ = strings.Cut(t, ":")
	idc = "id"
	if a, b, ok := strings.Cut(tbl, "."); ok {
		tbl, idc = a, b
	}
	return
}

// ReSessName は取り込み先の herdr セッション名。
var ReSessName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$`)

// physPath は cd "$(dirname p)" && pwd -P と basename。
func physPath(p string) (string, error) {
	d, err := filepath.Abs(filepath.Dir(p))
	if err != nil {
		return "", err
	}
	if d, err = filepath.EvalSymlinks(d); err != nil {
		return "", err
	}
	return filepath.Join(d, filepath.Base(p)), nil
}

// DBImportReq は config.db.import の入力。Path は絶対パス。Session が空なら呼び手の今のセッション。
type DBImportReq struct {
	Path    string `api:"path"`
	Session string `api:"session,optional"`
	DryRun  bool   `api:"dry_run,optional"`
}

// DBImportResult は取り込んだ結果。Out は tb が標準出力に出す文字列(表ごとの行数と、終わりの 1 行)。
type DBImportResult struct {
	Path    string `api:"path" json:"path"`
	Session string `api:"session" json:"session"`
	Out     string `api:"out" json:"out"`
}

func dbImportOp(env *app.Env) app.Op {
	return app.Command(env, "config.db.import", app.Cmd("cmd:config:db-import", "tb db-import"),
		func(ctx context.Context, in DBImportReq) (DBImportResult, error) {
			if in.Path == "" {
				return DBImportResult{}, define.Errorf(define.CodeInvalidArgument, "db-import <旧 DB のパス> [--session <名前>] [--dry-run]")
			}
			if !filepath.IsAbs(in.Path) { // 呼び手と API の作業ディレクトリは違いうる(tb は絶対パスにして渡す)
				return DBImportResult{}, define.Errorf(define.CodeInvalidArgument, "db-import: 旧 DB のパスは絶対パスで: %s", in.Path)
			}
			p, _ := define.PrincipalFrom(ctx)
			who := app.Who(p)
			db, done, err := env.DB()
			if err != nil {
				return DBImportResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
			}
			defer done()
			return dbImport(db, in, who, env.NowString())
		},
		define.Doc("旧形式の DB(スキーマ v11 まで)を共有の DB の 1 セッションとして取り込む(tb db-import。既定は人だけ)"),
		define.Authz("cmd:config:db-import"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/config/db-import"),
	)
}

// alreadyImported は、書き込みの鍵を取ったあとで印が見つかった(同時に取り込んだ)。
type alreadyImported struct{ seen string }

func (e *alreadyImported) Error() string { return "already imported: " + e.seen }

func dbImport(db *store.DB, in DBImportReq, who, now string) (DBImportResult, error) {
	src, session, dry := in.Path, in.Session, in.DryRun
	if session == "" {
		session = db.Session
	}
	if !ReSessName.MatchString(session) {
		return DBImportResult{}, define.Errorf(define.CodeInvalidArgument, "db-import: セッション名は英数字で始まり、英数字と . _ - の 64 字以内: %s", session)
	}
	fail := func(format string, a ...any) (DBImportResult, error) {
		return DBImportResult{}, define.Errorf(define.CodeFailedPrecondition, format, a...)
	}
	if st, err := os.Stat(src); err != nil || !st.Mode().IsRegular() {
		return fail("旧 DB がありません: %s", src)
	}
	real, err := physPath(src)
	if err != nil {
		return fail("旧 DB がありません: %s", src)
	}
	dreal, err := physPath(db.Path)
	if err != nil {
		dreal = db.Path
	}
	if real == dreal {
		return fail("取り込み先と同じ DB です: %s", src)
	}
	ro, err := store.OpenRO(real, 0)
	if err != nil {
		return fail("旧 DB を開けません: %s", src)
	}
	defer ro.Close()
	var sv int
	if ro.QueryRow(`PRAGMA user_version`).Scan(&sv) != nil {
		return fail("旧 DB を開けません: %s", src)
	}
	if sv >= 12 {
		return fail("取り込めるのは旧形式(セッションごとに分ける前、スキーマ v11 まで)の DB だけです(この DB は v%d)", sv)
	}
	mark := "db-import:" + real
	var seen sql.NullString
	if db.QueryRow(`SELECT herdr_session||' / '||value FROM main.tb_flags WHERE key=? LIMIT 1`, mark).Scan(&seen) == nil {
		return fail("この DB は取り込み済みです: %s(%s)", real, seen.String)
	}
	// 写しを作り、取り込み先のセッションとして最新まで移行する(元の DB には触らない)
	tmp, err := os.MkdirTemp("", "tb-import-")
	if err != nil {
		return fail("旧 DB の写しを作れません: %s", src)
	}
	defer os.RemoveAll(tmp)
	copyPath := filepath.Join(tmp, "src.db")
	if _, err := ro.Exec(`VACUUM INTO ` + store.Quote(copyPath)); err != nil {
		return fail("旧 DB の写しを作れません: %s", src)
	}
	ro.Close()
	var opt *store.Options
	if d := os.Getenv("TB_SCHEMA_DIR"); d != "" {
		opt = &store.Options{Schema: os.DirFS(d), NoSession: true}
	} else {
		opt = &store.Options{NoSession: true}
	}
	cp, err := store.Open(copyPath, session, opt)
	if err != nil {
		return fail("旧 DB の写しを移行できません: %v", err)
	}
	defer cp.Close()
	out, err := runImport(cp, db.Path, session, mark, who, now, dry)
	var ai *alreadyImported
	if errors.As(err, &ai) {
		return fail("この DB は取り込み済みです: %s(%s)", real, ai.seen)
	}
	if err != nil {
		return DBImportResult{}, define.Errorf(define.CodeInternal, "取り込みに失敗しました(何も変えていません): %v", err)
	}
	if dry {
		out = "(dry-run: 何も書いていません)\n" + out
	} else {
		out += fmt.Sprintf("%s を herdr セッション '%s' の行として取り込みました。旧 DB はそのまま残しています(不要なら消す)\n", real, session)
	}
	return DBImportResult{Path: real, Session: session, Out: out}, nil
}

// runImport は import_sql を写しの接続で流す(写しを main、共有の DB を dst)。出力は SELECT の行。
func runImport(cp *store.DB, dstPath, session, mark, who, ts string, dry bool) (string, error) {
	conn := cp.DB
	if _, err := conn.Exec(`ATTACH ` + store.Quote(dstPath) + ` AS dst`); err != nil {
		return "", err
	}
	defer conn.Exec(`DETACH dst`) //nolint:errcheck
	if _, err := conn.Exec(`BEGIN IMMEDIATE`); err != nil {
		return "", err
	}
	done := false
	defer func() {
		if !done {
			_, _ = conn.Exec(`ROLLBACK`)
		}
	}()
	// 同じ DB を同時に 2 度取り込まない(書き込みの鍵を取ってから印を見直す)
	var seen string
	if conn.QueryRow(`SELECT herdr_session||' / '||value FROM dst.tb_flags WHERE key=? LIMIT 1`, mark).Scan(&seen) == nil {
		return "", &alreadyImported{seen}
	}
	var out strings.Builder
	ex := func(q string) error { _, err := conn.Exec(q); return err }
	sel := func(q string) error {
		rows, err := conn.Query(q)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v any
			if err := rows.Scan(&v); err != nil {
				return err
			}
			out.WriteString(cellText(v) + "\n")
		}
		return rows.Err()
	}
	var stmts []string
	// 1. 整数の ID をずらす(負の値を経由して、途中で主キーがぶつからないようにする)
	stmts = append(stmts, `CREATE TEMP TABLE _off (t TEXT PRIMARY KEY, n INTEGER NOT NULL)`)
	for _, t := range importIDTables {
		tbl, idc, _ := importIDSplit(t)
		stmts = append(stmts,
			`INSERT INTO _off VALUES ('`+tbl+`', COALESCE((SELECT MAX(`+idc+`) FROM dst.`+tbl+`), 0))`,
			`UPDATE main.`+tbl+` SET `+idc+` = -(`+idc+` + (SELECT n FROM _off WHERE t='`+tbl+`'))`,
			`UPDATE main.`+tbl+` SET `+idc+` = -`+idc)
	}
	stmts = append(stmts,
		`UPDATE main.notify_sends SET event_id = event_id + (SELECT n FROM _off WHERE t='notify_events')`,
		`UPDATE main.notify_events SET batch_id = batch_id + (SELECT n FROM _off WHERE t='notify_events') WHERE batch_id IS NOT NULL`,
		`UPDATE main.observations SET digest_id = digest_id + (SELECT n FROM _off WHERE t='digests') WHERE digest_id IS NOT NULL`,
		`UPDATE main.pr_queue SET depends_on = depends_on + (SELECT n FROM _off WHERE t='pr_queue') WHERE depends_on IS NOT NULL`)
	// ref の "<接頭辞>-<数字>" だけを直す(owner/repo#12 などはそのまま)
	for _, t := range importIDTables {
		tbl, _, p := importIDSplit(t)
		if p == "" {
			continue
		}
		off := strconv.Itoa(len(p) + 2)
		for _, col := range []string{"messages.ref", "notify_events.ref"} {
			ct, cc, _ := strings.Cut(col, ".")
			stmts = append(stmts, `UPDATE main.`+ct+` SET `+cc+` = '`+p+`-' || (CAST(substr(`+cc+`, `+off+`) AS INTEGER) + (SELECT n FROM _off WHERE t='`+tbl+`'))
           WHERE `+cc+` LIKE '`+p+`-%' AND substr(`+cc+`, `+off+`) != '' AND substr(`+cc+`, `+off+`) NOT GLOB '*[^0-9]*'`)
		}
	}
	// 2. タスク ID がぶつかるものを付け替える(同じ project の、両方の DB を通した次の番号)。参照も直す
	stmts = append(stmts,
		`CREATE TEMP TABLE _ren (old TEXT PRIMARY KEY, new TEXT)`,
		`CREATE TEMP TABLE _next (project TEXT PRIMARY KEY, n INTEGER)`,
		// 消したチケットの番号(tb ticket rm が残す ticket-rm-max:<PJ>)も使い直さない: その番号以下は、ぶつかるものとして付け替える
		`INSERT INTO _next SELECT project, MAX(n)
         FROM (SELECT project, CAST(substr(id, length(project) + 4) AS INTEGER) AS n FROM (SELECT id, project FROM dst.tasks UNION ALL SELECT id, project FROM main.tasks)
               UNION ALL SELECT substr(key, 15), CAST(value AS INTEGER) FROM dst.tb_flags WHERE key LIKE 'ticket-rm-max:%') GROUP BY project`,
		`CREATE TEMP TABLE _clash AS SELECT m.id, m.project, ROW_NUMBER() OVER (PARTITION BY m.project ORDER BY m.id) AS k
         FROM main.tasks m WHERE EXISTS (SELECT 1 FROM dst.tasks d WHERE d.id = m.id)
           OR CAST(substr(m.id, length(m.project) + 4) AS INTEGER) <= COALESCE((SELECT MAX(CAST(value AS INTEGER)) FROM dst.tb_flags WHERE key = 'ticket-rm-max:' || m.project), 0)`,
		`INSERT INTO _ren SELECT c.id, 'T-' || c.project || '-' || printf('%03d', (SELECT n FROM _next x WHERE x.project = c.project) + c.k) FROM _clash c`,
		`UPDATE main.tasks SET id = (SELECT new FROM _ren WHERE old = id) WHERE id IN (SELECT old FROM _ren)`)
	for _, col := range importTaskRefs {
		ct, cc, _ := strings.Cut(col, ".")
		stmts = append(stmts, `UPDATE main.`+ct+` SET `+cc+` = (SELECT new FROM _ren WHERE old = `+cc+`) WHERE `+cc+` IN (SELECT old FROM _ren)`)
	}
	stmts = append(stmts,
		`UPDATE main.messages SET ref = (SELECT new FROM _ren WHERE old = ref) WHERE ref IN (SELECT old FROM _ren)`,
		`UPDATE main.notify_events SET ref = (SELECT new FROM _ren WHERE old = ref) WHERE ref IN (SELECT old FROM _ren)`)
	for _, q := range stmts {
		if err := ex(q); err != nil {
			return "", err
		}
	}
	// 3. 足す。行のセッションは写しの移行で取り込み先のものになっている
	cols := func(tbl string) (string, error) {
		var c sql.NullString
		err := conn.QueryRow(`SELECT group_concat(name, ', ') FROM pragma_table_info(?)`, tbl).Scan(&c)
		return c.String, err
	}
	tables := append([]string{"tasks"}, importIDTables...)
	for _, t := range tables {
		tbl, _, _ := importIDSplit(t)
		c, err := cols(tbl)
		if err != nil {
			return "", err
		}
		if err := ex(`INSERT INTO dst.` + tbl + ` (` + c + `) SELECT ` + c + ` FROM main.` + tbl); err != nil {
			return "", err
		}
	}
	for _, tbl := range []string{"agent_activity", "observations", "tb_flags", "notify_seen", "agent_parents"} {
		c, err := cols(tbl)
		if err != nil {
			return "", err
		}
		if err := ex(`INSERT OR IGNORE INTO dst.` + tbl + ` (` + c + `) SELECT ` + c + ` FROM main.` + tbl); err != nil {
			return "", err
		}
	}
	c, err := cols("agents")
	if err != nil {
		return "", err
	}
	if err := ex(`INSERT INTO dst.agents (` + c + `) SELECT ` + c + ` FROM main.agents WHERE true
       ON CONFLICT (herdr_session, name) DO UPDATE SET role = COALESCE(role, excluded.role), model = COALESCE(model, excluded.model),
         cwd = COALESCE(cwd, excluded.cwd), session_id = COALESCE(session_id, excluded.session_id),
         spawned_by = COALESCE(spawned_by, excluded.spawned_by), spawn_task = COALESCE(spawn_task, excluded.spawn_task)`); err != nil {
		return "", err
	}
	if _, err := conn.Exec(`INSERT INTO dst.tb_flags (herdr_session, key, value, set_by, set_at) VALUES (?, ?, ?, ?, ?)`, session, mark, ts, who, ts); err != nil {
		return "", err
	}
	// 4. 件数(ずらした番号の範囲と、付け替えたタスク)
	sels := []string{
		`SELECT 'tasks: ' || (SELECT COUNT(*) FROM main.tasks) || ' 件(付け替え ' || (SELECT COUNT(*) FROM _ren) || ' 件)'`,
		`SELECT '  ' || old || ' -> ' || new FROM _ren ORDER BY old`,
	}
	for _, t := range importIDTables {
		tbl, _, _ := importIDSplit(t)
		sels = append(sels, `SELECT '`+tbl+`: ' || COUNT(*) || ' 件' || CASE WHEN COUNT(*) > 0 AND (SELECT n FROM _off WHERE t='`+tbl+`') > 0
           THEN '(番号は +' || (SELECT n FROM _off WHERE t='`+tbl+`') || ')' ELSE '' END FROM main.`+tbl+` HAVING COUNT(*) > 0`)
	}
	sels = append(sels, `SELECT 'agents: ' || (SELECT COUNT(*) FROM main.agents) || ' 件'`)
	for _, q := range sels {
		if err := sel(q); err != nil {
			return "", err
		}
	}
	if dry {
		if _, err := conn.Exec(`ROLLBACK`); err != nil {
			return "", err
		}
	} else if _, err := conn.Exec(`COMMIT`); err != nil {
		return "", err
	}
	done = true
	return out.String(), nil
}

// cellText は sqlite3 の既定の出力と同じ 1 つの値の文字列(NULL は空。tb の sqlText と同じ)。
func cellText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case int64, float64:
		return jv.Scalar(jv.Number(tickets.Text(x)))
	default:
		return jv.Scalar(tickets.Text(v))
	}
}
