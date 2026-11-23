package tbcli

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/env"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/transport"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
	"github.com/imohiyoko/herdrorchestrator/internal/limits"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
	"github.com/imohiyoko/herdrorchestrator/internal/notify"
)

// アラートと解析役(taskboard/lib/alerts.sh の alert / alerts / alert-*)。
// アラートはチケット(project alert)と alert_details に置き、ここでは互換ビュー alerts を読み書きする(020。ADR 0003)。
// 閉じる(closed)のは人だけ(tb alert-close)。

const alertSeverities = "critical high medium low"

var reAid = regexp.MustCompile(`^A-[0-9]+$`)

func validAid(s string) (int64, error) {
	if !reAid.MatchString(s) {
		return 0, usage("アラート ID は A-<数字>: %s", s)
	}
	n, err := strconv.ParseInt(s[2:], 10, 64)
	if err != nil {
		n = -1 // 大きすぎる番号は無い番号
	}
	return n, nil
}

// needVal は need_arg: フラグの次に値が無ければ使い方の誤り。
func needVal(args []string, i int) (string, error) {
	if i+1 >= len(args) {
		return "", usage("%s に値が必要です", args[i])
	}
	return args[i+1], nil
}

// alertRow は alert_row(session_row alerts id n)。無ければ nil。
func alertRow(db *store.DB, id int64) jv.Obj {
	rows, err := sqlObjs(db.DB, nil, `SELECT * FROM alerts WHERE id=?`, id)
	if err != nil || len(rows) == 0 {
		return nil
	}
	return rows[0]
}

func alertStatus(db *store.DB, id int64) string {
	var s sql.NullString
	_ = db.QueryRow(`SELECT status FROM alerts WHERE id=?`, id).Scan(&s)
	return s.String
}

func alertSetStatus(db *store.DB, id int64, st string) {
	_, _ = db.Exec(`UPDATE main.alerts SET status=? WHERE herdr_session=? AND id=?`, st, db.Session, id)
}

func alertQueued(db *store.DB, id int64, note string) {
	_, _ = db.Exec(`UPDATE main.alerts SET status='queued', note=? WHERE herdr_session=? AND id=?`, note, db.Session, id)
}

// jqStr は jq の文字列埋め込み \(.x) の値(null は "null")。
func jqStr(v any) string {
	if v == nil {
		return "null"
	}
	return jv.Scalar(v)
}

// objStr は .x // "" の値。
func objStr(o jv.Obj, k string) string {
	v := o.Get(k)
	if v == nil || v == false {
		return ""
	}
	return jv.Scalar(v)
}

func withAid(o jv.Obj) jv.Obj { return o.Set("id", "A-"+jv.Scalar(o.Get("id"))) }

func alertNotify(db *store.DB, typ string, id int64) {
	o := alertRow(db, id)
	if o == nil {
		return
	}
	e := notify.Event{Type: typ, Ref: "A-" + strconv.FormatInt(id, 10)}
	if typ == "alert_raised" {
		e.A, e.B, e.C = objStr(o, "title"), objStr(o, "source"), objStr(o, "body")
	} else {
		e.Sev, e.A, e.B, e.C = objStr(o, "severity"), objStr(o, "title"), objStr(o, "result_summary"), objStr(o, "result_path")
	}
	notify.New(db.DB, db.Session).Emit(e)
}

// cmdAlert は tb alert: 見張り役が登録する(緊急度は解析役が決める)。同じ指紋の未解決があれば件数を増やすだけ。
func cmdAlert(args []string, w, stderr io.Writer) error {
	var title, body, bfile, key string
	json := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--severity":
			return usage("alert: 緊急度は解析役が tb alert-result --severity で決めます(見張り役は指定しない)")
		case "--title", "--body", "--body-file", "--key":
			v, err := needVal(args, i)
			if err != nil {
				return err
			}
			i++
			switch a {
			case "--title":
				title = v
			case "--body":
				body = v
			case "--body-file":
				bfile = v
			default:
				key = v
			}
		case "--json":
			json = true
		case "--from", "--source", "--name", "--pane":
			return usage("alert: 報告者は herdr から自動で決まります。%s は指定できません", a)
		default:
			return usage("alert: 不明な引数 %s", a)
		}
	}
	if blank(title) {
		return usage("alert: --title が必要です")
	}
	if bfile != "" {
		b, err := readHead(bfile, 20000)
		if err != nil {
			return die("alert: --body-file を読めません: %s", bfile)
		}
		body = b
	}
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	src, spane := "cli", ""
	if os.Getenv("HERDR_PANE_ID") != "" {
		l, err := label(db, stderr)
		if err != nil {
			return err
		}
		src, spane = l.Name, l.Pane
	}
	sum := sha256.Sum256([]byte(or(key, title)))
	fp := hex.EncodeToString(sum[:])[:16]
	ts := nowTS()
	var dup sql.NullInt64
	_ = db.QueryRow(`SELECT id FROM alerts WHERE fingerprint=? AND status NOT IN ('analyzed','closed') ORDER BY id DESC LIMIT 1`, fp).Scan(&dup)
	var id int64
	state := "new"
	if dup.Valid {
		if _, err := db.Exec(`UPDATE main.alerts SET count=count+1, last_seen=? WHERE herdr_session=? AND id=?`, ts, db.Session, dup.Int64); err != nil {
			return err
		}
		id, state = dup.Int64, "duplicate"
	} else {
		// ビュー経由では last_insert_rowid() が使えないので、同じトランザクションで振った番号を読む
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO main.alerts (herdr_session, fingerprint, title, body, source, source_pane, status, count, first_seen, last_seen)
			VALUES (?, ?, ?, ?, ?, NULLIF(?,''), 'new', 1, ?, ?)`, db.Session, fp, title, body, src, spane, ts, ts)
		if err == nil {
			err = tx.QueryRow(`SELECT MAX(alert_no) FROM main.alert_details`).Scan(&id)
		}
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	if state == "new" {
		alertNotify(db, "alert_raised", id)
		_ = alertDispatchOne(db, id)
	}
	if json {
		if o := alertRow(db, id); o != nil {
			fmt.Fprintln(w, jv.Compact(withAid(o)))
		}
		return nil
	}
	fmt.Fprintf(w, "A-%d %s %s\n", id, state, alertStatus(db, id))
	return nil
}

// alertDispatchOne は alert_dispatch_one: 解析役を起動する。新規で policy の alert:analyze が allow でなければ awaiting-approval。上限なら queued。
func alertDispatchOne(db *store.DB, id int64) int {
	st := alertStatus(db, id)
	if st != "new" && st != "queued" && st != "approved" {
		return 0
	}
	if st == "new" {
		if v, _ := approval.NewJudge(db.DB, db.Session).Of("alert:analyze", ""); v != "allow" {
			alertSetStatus(db, id, "awaiting-approval")
			return 0
		}
	}
	var busy int64
	_ = db.QueryRow(`SELECT COUNT(*) FROM alerts WHERE status='analyzing'`).Scan(&busy)
	amax := envOr("TB_ANALYST_MAX", "2")
	ls := limits.Load(db.DB, db.Session)
	if v, _, ok := ls.Raw("analyst_max"); ok { // limits で設定されていれば TB_ANALYST_MAX より優先
		amax = v
	}
	if n, err := strconv.ParseInt(strings.TrimSpace(amax), 10, 64); err == nil && busy >= n {
		alertSetStatus(db, id, "queued")
		return 0
	}
	// 動作中の agent の総数(max_running_agents)。上限なら作らず queued に戻す
	if limitsRefuseRunning(db, 1, io.Discard) != nil {
		alertQueued(db, id, "max_running_agents に達しているため待機")
		return 0
	}
	return startAnalyst(db, id)
}

// startAnalyst は start_analyst: analyst-A<n> を起動して依頼を送る。
func startAnalyst(db *store.DB, id int64) int {
	ids := strconv.FormatInt(id, 10)
	name := "analyst-A" + ids
	d := envOr("TB_ANALYST_CWD", os.Getenv("TB_ROOT")+"/..")
	if d == "" || !isDir(d) {
		alertQueued(db, id, "TB_ANALYST_CWD が無い")
		return 11
	}
	cwd := physDir(d)
	model := os.Getenv("TB_ANALYST_MODEL")
	var extra []string
	if model != "" {
		extra = []string{"--model", model}
	}
	retry := envOr("TB_ANALYST_RETRY_SEC", "3")
	old, had := os.LookupEnv("TB_SPAWN_RETRY_SEC")
	_ = os.Setenv("TB_SPAWN_RETRY_SEC", retry)
	sp, ok := transport.Spawn(env.TBDB(), kindDefault, name, cwd, extra)
	if had {
		_ = os.Setenv("TB_SPAWN_RETRY_SEC", old)
	} else {
		_ = os.Unsetenv("TB_SPAWN_RETRY_SEC")
	}
	if !ok {
		alertQueued(db, id, sp.Err)
		return 11
	}
	_ = agentRegister(db, name, cwd, model, "", "tb", "unknown", "", kindDefault)
	_, _ = db.Exec(`UPDATE main.alerts SET status='analyzing', analyst=?, analysis_started_at=?, note=NULL WHERE herdr_session=? AND id=?`, name, nowTS(), db.Session, id)
	prompt := "[from: tb / alert / A-" + ids + " / task] " + config.Msg(db.DB, db.Session, "alert.analyze",
		"あなたはアラート A-{id} を解析する agent です。まず tb alerts A-{id} で全文を読み、原因の仮説・根拠・影響範囲・推奨対応をまとめ、緊急度を決めて、tb alert-result A-{id} --severity critical|high|medium|low --summary '<要約>' --report <報告ファイル> で返してください。", "id="+ids)
	// 起動直後は idle になるまで少し待つ(送るのは idle/done のときだけ)
	for i := 1; i <= 5; i++ {
		if transport.Send(db.DB, db.Session, name, prompt, "A-"+ids, "alert", io.Discard) == 0 {
			return 0
		}
		time.Sleep(time.Duration(secEnv("TB_ANALYST_RETRY_SEC", 3) * float64(time.Second)))
	}
	_, _ = db.Exec(`UPDATE main.alerts SET note='解析役に依頼を送れていない(tb alert-dispatch で再送)' WHERE herdr_session=? AND id=?`, db.Session, id)
	return 10
}

// cmdAlertDispatch は tb alert-dispatch: 承認待ち・上限待ちのアラートを進める。
func cmdAlertDispatch(args []string, w, stderr io.Writer) error {
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	alertDispatchAll(db)
	return qrows(db.DB, `SELECT 'A-'||id, status FROM alerts WHERE status IN ('awaiting-approval','queued','analyzing') ORDER BY id`, nil, func(_ []string, vals []any) error {
		fmt.Fprintln(w, sqlText(vals[0])+" "+sqlText(vals[1]))
		return nil
	})
}

func sqlText(v any) string {
	if v == nil {
		return ""
	}
	return jv.Scalar(jvValue(v))
}

func alertDispatchAll(db *store.DB) {
	var ids []int64
	_ = qrows(db.DB, `SELECT id FROM alerts WHERE status IN ('new','queued','approved') ORDER BY id`, nil, func(_ []string, vals []any) error {
		if n, ok := vals[0].(int64); ok {
			ids = append(ids, n)
		}
		return nil
	})
	for _, id := range ids {
		_ = alertDispatchOne(db, id)
	}
}

// cmdAlertApprove は tb alert-approve A-<n> [--always]: 人が解析役の起動を承認する。
func cmdAlertApprove(args []string, w, stderr io.Writer) error {
	if len(args) < 1 {
		return usage("alert-approve A-<n> [--always]")
	}
	id, err := validAid(args[0])
	if err != nil {
		return err
	}
	always := len(args) > 1 && args[1] == "--always"
	if err := requireHuman("tb alert-approve"); err != nil {
		return err
	}
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if alertStatus(db, id) != "awaiting-approval" {
		return die("A-%d は承認待ちではありません", id)
	}
	alertSetStatus(db, id, "approved")
	if always && setApproval(db, "allow", "alert:analyze", io.Discard, io.Discard) == nil {
		fmt.Fprintln(w, "alert:analyze = allow(今後は承認なし)")
	}
	_ = alertDispatchOne(db, id)
	fmt.Fprintf(w, "A-%d %s\n", id, alertStatus(db, id))
	return nil
}

// cmdAlertResult は tb alert-result: 解析役が緊急度と結果を返す(担当の解析役か人だけ)。
func cmdAlertResult(args []string, w, stderr io.Writer) error {
	if len(args) < 1 {
		return usage("alert-result A-<n> --severity critical|high|medium|low --summary S [--report <ファイル>]")
	}
	id, err := validAid(args[0])
	if err != nil {
		return err
	}
	var summary, report, sev string
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--summary", "--report", "--severity":
			v, err := needVal(args, i)
			if err != nil {
				return err
			}
			switch args[i] {
			case "--summary":
				summary = v
			case "--report":
				report = v
			default:
				sev = v
			}
			i++
		default:
			return usage("alert-result: 不明な引数 %s", args[i])
		}
	}
	if sev == "" || strings.Contains(sev, " ") || !strings.Contains(" "+alertSeverities+" ", " "+sev+" ") {
		return usage("alert-result: --severity は %s のどれか(緊急度は解析役が決める)", alertSeverities)
	}
	if blank(summary) {
		return usage("alert-result: --summary が必要です")
	}
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	row := alertRow(db, id)
	if row == nil {
		return die("アラートが見つかりません: A-%d", id)
	}
	by := "human"
	if os.Getenv("HERDR_PANE_ID") != "" {
		l, err := label(db, stderr)
		if err != nil {
			return err
		}
		if l.Name != objStr(row, "analyst") {
			return die("A-%d の解析役ではありません(%s)", id, l.Name)
		}
		by = l.Name
	}
	if objStr(row, "status") == "closed" {
		return die("A-%d は閉じています", id)
	}
	rtext := ""
	if report != "" {
		if rtext, err = readHead(report, 50000); err != nil {
			return die("--report を読めません: %s", report)
		}
	}
	ts := nowTS()
	if _, err := db.Exec(`UPDATE main.alerts SET status='analyzed', severity=?, result_summary=?, result_report=NULLIF(?,''),
		result_path=NULLIF(?,''), result_by=?, analyzed_at=? WHERE herdr_session=? AND id=?`, sev, summary, rtext, report, by, ts, db.Session, id); err != nil {
		return err
	}
	alertNotify(db, "alert_result", id)
	alertDeliver(db, id)
	// 解析役は結果を返したら用済み: 止めてよい印を付ける(停止は tb agent-stop)
	if by != "human" {
		_, _ = db.Exec(`UPDATE main.agents SET killable='yes', killable_reason=?, killable_by='tb', killable_at=?,
			killable_session=session_id WHERE herdr_session=? AND name=?`, fmt.Sprintf("A-%d の解析結果を返した", id), nowTS(), db.Session, by)
	}
	alertDispatchAll(db) // 枠が空いたので待ちを進める
	fmt.Fprintf(w, "A-%d analyzed\n", id)
	return nil
}

// alertDeliver は alert_deliver: 解析結果を指揮官へ(使用中なら tb digest が回収する)。
func alertDeliver(db *store.DB, id int64) {
	o := alertRow(db, id)
	if o == nil || objStr(o, "result_delivered_at") != "" {
		return
	}
	ids := strconv.FormatInt(id, 10)
	target := message.TargetFor(db.DB, db.Session, "*")
	msg := "[alert A-" + ids + " analyzed / " + jqStr(o.Get("severity")) + "] " + util.Oneline(jqStr(o.Get("title")), 80) + " — " +
		util.Oneline(jqStr(o.Get("result_summary")), 300) + " (全文: tb alerts A-" + ids + ")"
	if transport.Send(db.DB, db.Session, target, msg, "A-"+ids, "alert", io.Discard) != 0 {
		return
	}
	_, _ = db.Exec(`UPDATE main.alerts SET result_delivered_at=? WHERE herdr_session=? AND id=?`, nowTS(), db.Session, id)
}

// cmdAlertClose は tb alert-close A-<n>: 人が閉じる。
func cmdAlertClose(args []string, w, stderr io.Writer) error {
	if len(args) != 1 {
		return usage("alert-close A-<n>")
	}
	id, err := validAid(args[0])
	if err != nil {
		return err
	}
	if err := requireHuman("tb alert-close"); err != nil {
		return err
	}
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE main.alerts SET status='closed', closed_at=? WHERE herdr_session=? AND id=?`, nowTS(), db.Session, id); err != nil {
		return err
	}
	fmt.Fprintln(w, args[0]+" closed")
	return nil
}

// cmdAlerts は tb alerts [A-<n>] [--status S] [--json]: 一覧 / 全文と解析結果。
func cmdAlerts(args []string, w, stderr io.Writer) error {
	if len(args) > 0 && strings.HasPrefix(args[0], "A-") {
		id, err := validAid(args[0])
		if err != nil {
			return err
		}
		db, _, err := openCmd(stderr)
		if err != nil {
			return err
		}
		defer db.Close()
		o := alertRow(db, id)
		if o == nil {
			return die("アラートが見つかりません: %s", args[0])
		}
		if len(args) > 1 && args[1] == "--json" {
			fmt.Fprintln(w, jv.Compact(withAid(o)))
			return nil
		}
		g := func(k, def string) string {
			if v := o.Get(k); v != nil && v != false {
				return jv.Scalar(v)
			}
			return def
		}
		s := fmt.Sprintf("A-%s [%s] %s ×%s  %s\n報告: %s / 初回 %s / 最終 %s\n解析役: %s  チケット: %s\n\n%s\n",
			jqStr(o.Get("id")), g("severity", "未解析"), jqStr(o.Get("status")), jqStr(o.Get("count")), jqStr(o.Get("title")),
			jqStr(o.Get("source")), jqStr(o.Get("first_seen")), jqStr(o.Get("last_seen")),
			g("analyst", "-"), g("task_id", "-"), g("body", ""))
		if v := o.Get("result_summary"); v != nil && v != false {
			s += fmt.Sprintf("---- 解析結果(%s, %s)\n%s\n\n%s", jqStr(o.Get("result_by")), jqStr(o.Get("analyzed_at")), jqStr(v), g("result_report", ""))
		}
		fmt.Fprintln(w, s)
		return nil
	}
	json, st := false, ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			json = true
		case "--status":
			v, err := needVal(args, i)
			if err != nil {
				return err
			}
			st = v
			i++
		default:
			return usage("alerts: 不明な引数 %s", args[i])
		}
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	where, qa := "1=1", []any{}
	if st != "" {
		where, qa = "status=?", []any{st}
	}
	if json {
		rows, err := sqlObjs(db.DB, nil, `SELECT * FROM alerts WHERE (`+where+`) ORDER BY id DESC LIMIT 100`, qa...)
		if err != nil {
			return err
		}
		for _, o := range rows {
			fmt.Fprintln(w, jv.Compact(withAid(o)))
		}
		return nil
	}
	return qrows(db.DB, `SELECT 'A-'||id, COALESCE(severity,'未解析'), status, count, COALESCE(analyst,'-'), last_seen, title FROM alerts WHERE (`+where+`) ORDER BY id DESC LIMIT 100`, qa, func(_ []string, vals []any) error {
		parts := make([]string, len(vals))
		for i, v := range vals {
			if v == nil {
				parts[i] = "-"
			} else {
				parts[i] = sqlText(v)
			}
		}
		fmt.Fprintln(w, strings.Join(parts, "\t"))
		return nil
	})
}
