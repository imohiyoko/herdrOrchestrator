package tbcli

import (
	"github.com/imohiyoko/API/port"

	"fmt"
	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	appalert "github.com/imohiyoko/herdrorchestrator/internal/app/alert"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
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
	var r appalert.TextResult
	if callAppW(db, app.Human(), io.Discard, "alert.get", appalert.GetReq{ID: id}, &r) != nil {
		return nil
	}
	vs, err := jv.DecodeAll([]byte(r.Text))
	if err != nil || len(vs) != 1 {
		return nil
	}
	o, _ := vs[0].(jv.Obj)
	return o
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

func sqlText(v any) string {
	if v == nil {
		return ""
	}
	return jv.Scalar(jvValue(v))
}

// reporterCall は操作 op を、pane の中なら herdr から一度だけ引いた報告者(pane の持ち主)として呼ぶ(alert / notify ack)。
// pane の外なら Reporter は無く、呼び手は callerPrincipal(人か system)。
func reporterCall(db *store.DB, what string, stderr io.Writer, op string, in, out any) error {
	env := appEnv(db, stderr)
	var by port.Principal
	if os.Getenv("HERDR_PANE_ID") != "" {
		l, err := label(db, stderr)
		if err != nil {
			return err
		}
		env.Reporter = func(string) (string, string, string) { return l.Name, l.Pane, l.Session }
		by = app.Agent(l.Name)
	} else {
		var err error
		if by, err = callerPrincipal(what); err != nil {
			return err
		}
	}
	return callAppEnv(env, by, op, in, out)
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
	var r appalert.RaiseResult
	if err := reporterCall(db, "tb alert", stderr, "alert.raise", appalert.RaiseReq{Title: title, Body: body, Key: key}, &r); err != nil {
		return err
	}
	if json {
		id, _ := validAid(r.ID)
		if o := alertRow(db, id); o != nil {
			fmt.Fprintln(w, jv.Compact(withAid(o)))
		}
		return nil
	}
	fmt.Fprintf(w, "%s %s %s\n", r.ID, r.State, r.Status)
	return nil
}

// cmdAlertDispatch は tb alert-dispatch: 承認待ち・上限待ちのアラートを進める。
func cmdAlertDispatch(args []string, w, stderr io.Writer) error {
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	var r appalert.DispatchResult
	if err := callAppWarn(db, "tb alert-dispatch", stderr, "alert.dispatch", appalert.DispatchReq{}, &r); err != nil {
		return err
	}
	for _, a := range r.Alerts {
		fmt.Fprintln(w, a.ID+" "+a.Status)
	}
	return nil
}

// cmdAlertApprove は tb alert-approve A-<n> [--always]: 人が解析役の起動を承認する。
func cmdAlertApprove(args []string, w, stderr io.Writer) error {
	if len(args) < 1 {
		return usage("alert-approve A-<n> [--always]")
	}
	if _, err := validAid(args[0]); err != nil {
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
	var r appalert.StatusResult
	if err := callAppWarn(db, "tb alert-approve", stderr, "alert.approve", appalert.IDReq{ID: args[0], Always: always}, &r); err != nil {
		return err
	}
	if r.Pending != nil {
		return pendingExit(w, r.Pending)
	}
	if r.Always {
		fmt.Fprintln(w, "alert:analyze = allow(今後は承認なし)")
	}
	fmt.Fprintf(w, "%s %s\n", r.ID, r.Status)
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
	if sev == "" || strings.Contains(sev, " ") || !strings.Contains(" "+appalert.Severities+" ", " "+sev+" ") {
		return usage("alert-result: --severity は %s のどれか(緊急度は解析役が決める)", appalert.Severities)
	}
	if blank(summary) {
		return usage("alert-result: --summary が必要です")
	}
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if alertRow(db, id) == nil {
		return die("アラートが見つかりません: A-%d", id)
	}
	rtext := ""
	if report != "" {
		if rtext, err = readHead(report, 50000); err != nil {
			return die("--report を読めません: %s", report)
		}
	}
	var r appalert.StatusResult
	in := appalert.ResultReq{ID: args[0], Severity: sev, Summary: summary, Report: rtext, ReportPath: report}
	if err := reporterCall(db, "tb alert-result", stderr, "alert.result", in, &r); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s %s\n", r.ID, r.Status)
	return nil
}

// cmdAlertClose は tb alert-close A-<n>: 人が閉じる。
func cmdAlertClose(args []string, w, stderr io.Writer) error {
	if len(args) != 1 {
		return usage("alert-close A-<n>")
	}
	if _, err := validAid(args[0]); err != nil {
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
	var r appalert.StatusResult
	if err := callAppWarn(db, "tb alert-close", stderr, "alert.close", appalert.IDReq{ID: args[0]}, &r); err != nil {
		return err
	}
	if r.Pending != nil {
		return pendingExit(w, r.Pending)
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
	var r appalert.TextResult
	if err := callAppW(db, app.Human(), io.Discard, "alert.list", appalert.ListReq{Status: st, JSON: json}, &r); err != nil { // 主体で中身の変わらない読み取り
		return err
	}
	_, err = fmt.Fprint(w, r.Text)
	return err
}
