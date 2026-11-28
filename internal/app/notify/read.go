package notify

// 通知の読むだけの操作(tb notify status / dry-run / events / rules test)。Text は tb の出力そのまま。

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/rowfmt"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/capv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
	"github.com/imohiyoko/herdrorchestrator/internal/notify"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// StatusReq は notify.status の入力。
type StatusReq struct {
	JSON bool `api:"json,optional"`
}

// DryRunReq は notify.dry-run の入力(無し)。
type DryRunReq struct{}

// EventsReq は notify.events の入力。Status は slack_status で絞る。Limit は 1〜9999(0 なら 50)。
type EventsReq struct {
	Status string `api:"status,optional"`
	Limit  int    `api:"limit,optional"`
	JSON   bool   `api:"json,optional"`
}

// RulesTestReq は notify.rules.test の入力(E-<n>)。
type RulesTestReq struct {
	ID string `api:"id,path"`
}

// TextResult は読むだけの操作の結果。Text は tb が標準出力に出す文字列。
type TextResult struct {
	Text string `api:"text" json:"text"`
}

var (
	reStatusW  = regexp.MustCompile(`^[a-z-]+$`)
	reEventRef = regexp.MustCompile(`^E-([0-9]{1,18})$`)
)

func readOps(env *app.Env) []app.Op {
	return []app.Op{
		readOp(env, "notify.status", "cmd:notify:status", "GET /v1/notify/status", "通知の設定と状態(tb notify status)", statusText),
		readOp(env, "notify.dryrun", "cmd:notify:dry-run", "GET /v1/notify/dry-run", "送るとしたときの内容(tb notify dry-run。何も送らない)", dryRun),
		readOp(env, "notify.events", "cmd:notify:events", "GET /v1/notify/events", "通知のイベントの一覧(tb notify events。新しい順)", eventsText),
		readOp(env, "notify.rules.test", "cmd:notify:rules", "GET /v1/notify/rules/test/{id}", "タスクの履歴 1 件がどの規則に当たるか(tb notify rules test。何も積まない)", rulesTest),
	}
}

// readOp は読むだけの操作(門は通すだけ。入力は f の中で確かめる)。
func readOp[In any](env *app.Env, name, authz, route, doc string, f func(*store.DB, *notify.Q, In) (string, error)) app.Op {
	return app.Query(env, name,
		func(context.Context, *app.Env, string, any) app.Decision { return app.Decision{} },
		func(_ context.Context, in In) (TextResult, error) {
			return withDB(env, func(db *store.DB) (TextResult, error) {
				q := queue(db)
				q.ActiveSQL = tickets.LoadStatuses(db.DB, db.Session).SQLList("active", "review")
				s, err := f(db, q, in)
				if err != nil {
					if _, ok := err.(*define.Error); !ok {
						err = define.Errorf(define.CodeInternal, "%s", err.Error())
					}
					return TextResult{}, err
				}
				return TextResult{Text: s}, nil
			})
		},
		define.Doc(doc),
		define.Authz(authz),
		define.Expose(define.Internal, define.REST),
		define.HTTP(route),
	)
}

func webhookState(q *notify.Q) string {
	f := q.C.Get("webhook_file")
	if f == "" {
		return "未設定"
	}
	if st, err := os.Stat(f); err != nil || !st.Mode().IsRegular() {
		return "ファイルが無い"
	}
	if st, err := os.Lstat(f); err == nil && st.Mode()&(os.ModePerm|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0o600 && st.Mode().IsRegular() {
		return "ok(0600)"
	}
	return "権限が 0600 ではない"
}

func setOrUnset(v, set, unset string) string {
	if v != "" {
		return set
	}
	return unset
}

func or(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// capJSON は上限の設定を JSON の値にする(上限なしは null)。
func capJSON(c *notify.Cfg, k string) string {
	if capv.IsUnlimited(c.Get(k)) {
		return "null"
	}
	return strconv.Itoa(c.Int(k))
}

func statusText(db *store.DB, q *notify.Q, in StatusReq) (string, error) {
	c := q.C
	var b strings.Builder
	var counts []string
	_ = store.Each(db.DB, "SELECT slack_status, COUNT(*) FROM notify_events GROUP BY slack_status ORDER BY 1", nil, func(_ []string, vals []any) error {
		counts = append(counts, tickets.Text(vals[0])+" "+tickets.Text(vals[1]))
		return nil
	})
	cs := strings.Join(counts, ",")
	if in.JSON {
		fmt.Fprintf(&b, `{"enabled":%s,"slack_via":%s,"slack_target":%s,"notifier_agent":%s,"webhook_file":%s,"default_sinks":%s,"rate_per_min":%s,"rate_per_hour":%s,"max_len":%s,"slack_status_counts":%s}`+"\n",
			util.JQString(c.Get("enabled")), util.JQString(c.Get("slack_via")), util.JQString(setOrUnset(c.Get("slack_target"), "set", "unset")),
			util.JQString(c.Get("notifier_agent")), util.JQString(webhookState(q)), util.JQString(c.Get("sinks_default")),
			capJSON(c, "rate_per_min"), capJSON(c, "rate_per_hour"), capJSON(c, "max_len"), util.JQString(cs))
		return b.String(), nil
	}
	fmt.Fprintf(&b, "enabled: %s (%s)\n", c.Get("enabled"), c.Source("enabled"))
	fmt.Fprintf(&b, "default_sinks = %s\n", c.Get("sinks_default"))
	for _, t := range notify.Types {
		if s := c.SinksOf(t); s != c.Get("sinks_default") && t != "digest" {
			fmt.Fprintf(&b, "  sinks[%s]: %s\n", t, s)
		}
	}
	fmt.Fprintf(&b, "slack_via: %s\n", c.Get("slack_via"))
	fmt.Fprintf(&b, "slack_target: %s(値は表示しません)\n", setOrUnset(c.Get("slack_target"), "設定あり", "未設定"))
	fmt.Fprintf(&b, "notifier_agent: %s\n", c.Get("notifier_agent"))
	fmt.Fprintf(&b, "webhook_file: %s\n", webhookState(q))
	fmt.Fprintf(&b, "rate: %s/min, %s/hour(超えた分は 1 通にまとめる)  max_len: %s  dedupe_sec: %s\n", c.Get("rate_per_min"), c.Get("rate_per_hour"), c.Get("max_len"), c.Get("dedupe_sec"))
	fmt.Fprintf(&b, "hash_check: %s  ack_timeout_min: %s  ack_retries: %s\n", c.Get("hash_check"), c.Get("ack_timeout_min"), c.Get("ack_retries"))
	fmt.Fprintf(&b, "mask: email=%s ip=%s repo=%s account=%s host=%s  strip_raw_logs: %s\n", c.Get("mask_email"), c.Get("mask_ip"), c.Get("mask_repo"), c.Get("mask_account"), c.Get("mask_host"), c.Get("strip_raw_logs"))
	fmt.Fprintln(&b, "secret filter: 常に有効(秘密らしい値を含むイベントは外へ出さず、file にだけ伏せて残す)")
	fmt.Fprintf(&b, "events: %s\n", or(cs, "なし"))
	return b.String(), nil
}

// tabRest は IFS=$'\t' read の最後の変数と同じく、前後のタブを落とす(本文はタブを含まないので念のため)。
func tabRest(s string) string { return strings.Trim(s, "\t") }

func dryRun(db *store.DB, q *notify.Q, _ DryRunReq) (string, error) {
	c := q.C
	var b strings.Builder
	if !c.On() {
		fmt.Fprintln(&b, "(enabled=off: 今は何も積まれず、何も送られません。下は、積まれているものを送るとしたときの内容)")
	}
	budget := q.Budget()
	fmt.Fprintf(&b, "# dry-run: 何も送りません。経路 slack_via=%s / 宛先 %s / 今回の枠 %d 通\n", c.Get("slack_via"), setOrUnset(c.Get("slack_target"), "設定あり", "未設定"), budget)
	cnt := 0
	err := store.Each(db.DB, "SELECT id, hash, text FROM notify_events WHERE slack_status='pending' AND blocked=0 ORDER BY id", nil, func(_ []string, v []any) error {
		cnt++
		fmt.Fprintf(&b, "would send N-%s [%s] %s\n", tickets.Text(v[0]), tickets.Text(v[1]), tabRest(tickets.Text(v[2])))
		return nil
	})
	if err != nil {
		return "", err
	}
	if cnt > budget {
		fmt.Fprintf(&b, "# 枠を超える分は 1 通にまとめます(個別に送るのは最大 %d 件)\n", max(budget-1, 0))
	}
	err = store.Each(db.DB, "SELECT id, text FROM notify_events WHERE blocked=1 ORDER BY id DESC LIMIT 20", nil, func(_ []string, v []any) error {
		fmt.Fprintf(&b, "blocked N-%s(外へは送りません) %s\n", tickets.Text(v[0]), tabRest(tickets.Text(v[1])))
		return nil
	})
	if err != nil {
		return "", err
	}
	if cnt == 0 {
		fmt.Fprintln(&b, "(送るものはありません)")
	}
	return b.String(), nil
}

func eventsText(db *store.DB, _ *notify.Q, in EventsReq) (string, error) {
	lim := in.Limit
	if lim == 0 {
		lim = 50
	}
	if lim < 0 || lim > 9999 {
		return "", define.Errorf(define.CodeInvalidArgument, "events: limit は 0〜9999(0 は既定の 50)")
	}
	where, qa := "1=1", []any{}
	if in.Status != "" {
		if !reStatusW.MatchString(in.Status) {
			return "", define.Errorf(define.CodeInvalidArgument, "events: --status が不正")
		}
		where, qa = "slack_status=?", []any{in.Status}
	}
	var b strings.Builder
	l := strconv.Itoa(lim)
	if in.JSON {
		err := store.Each(db.DB, "SELECT * FROM notify_events WHERE ("+where+") ORDER BY id DESC LIMIT "+l, qa, func(cols []string, vals []any) error {
			for i, c := range cols {
				if c == "id" {
					vals[i] = "N-" + tickets.Text(vals[i])
				}
			}
			return rowfmt.EmitRow(&b, true, cols, vals, "")
		})
		return b.String(), err
	}
	err := store.Each(db.DB, "SELECT 'N-'||id, type, slack_status, CASE WHEN blocked=1 THEN 'blocked' ELSE '-' END, created_at, text FROM notify_events WHERE ("+where+") ORDER BY id DESC LIMIT "+l, qa, func(cols []string, vals []any) error {
		return rowfmt.EmitRow(&b, false, cols, vals, "-")
	})
	return b.String(), err
}

func rulesTest(db *store.DB, q *notify.Q, in RulesTestReq) (string, error) {
	m := reEventRef.FindStringSubmatch(in.ID)
	if m == nil {
		return "", define.Errorf(define.CodeInvalidArgument, "notify rules test E-<n>")
	}
	id, _ := strconv.ParseInt(m[1], 10, 64)
	var one int
	if db.QueryRow("SELECT 1 FROM main.task_events WHERE herdr_session=? AND id=?", db.Session, id).Scan(&one) != nil {
		return "", define.Errorf(define.CodeNotFound, "イベントが見つかりません: E-%s", m[1])
	}
	hits, err := q.Apply(q.LoadRules(), id-1, id, true)
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return fmt.Sprintf("E-%s: どの規則にも当たりません(送りません)\n", m[1]), nil
	}
	return fmt.Sprintf("E-%s: 届け先 %s(当たった行: %s)。試すだけで、何も積みません\n", m[1], hits[0].Sinks, hits[0].Lines), nil
}
