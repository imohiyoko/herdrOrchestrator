package tbcli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/imohiyoko/API/port"

	"github.com/imohiyoko/herdrorchestrator/internal/agentset"
	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/server"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// tb agentset <動詞>: agent セット(internal/agentset)。定義は人だけが書き(add / rm)、
// up / down は API(agent.new・agent.revive・agent.stop)を人として呼ぶ。定義の置き場は TB_AGENTSETS か、DB と同じ所の agentsets/。

var agentsetVerbs = map[string]cmdFunc{
	"add": cmdAgentsetAdd, "rm": cmdAgentsetRm, "list": cmdAgentsetList, "show": cmdAgentsetShow,
	"up": cmdAgentsetUp, "down": cmdAgentsetDown, "status": cmdAgentsetStatus, "scale": cmdAgentsetScale,
	"run": cmdAgentsetRun, "reap": cmdAgentsetReap,
}

var agentsetService = service{name: "agentset", one: agentsetVerbs, help: agentsetUsageText}

func cmdAgentset(args []string, w, stderr io.Writer) error {
	return runService(agentsetService, args, w, stderr)
}

const agentsetUsageText = `tb agentset add <セット> [--file F]   定義を書く(人だけ。本文は省くと標準入力)
tb agentset rm <セット>   定義を消す(人だけ。動いているメンバーは止めない)
tb agentset list   定義のあるセット
tb agentset show <セット>   定義の本文
tb agentset up <セット>   1 つの workspace に pane を分けて立ち上げる(動いているメンバーはそのまま、止まっているメンバーは戻す)
tb agentset down <セット> [--force]   メンバーを子から順に止める(止めてよいと判定されていないメンバーは --force)
tb agentset status <セット> [--json]   メンバーごとの状態・pane・親
tb agentset scale <セット> <メンバー> <N|+N|-N>   数を変えられるメンバーの動いている数を変える(同じ workspace に足す・手の空いたものから止める)
tb agentset run <セット> <メンバー> --task T [--brief B]   job 型(mode = job)のメンバーをタスク T のために 1 体立てる(人だけ)
tb agentset reap <セット>   タスクが終わった job を止め、親が全員止まったメンバーを on_parent_exit で片付ける
`

// appCaller は agentset.Caller(同じプロセスで API を呼ぶ)。
type appCaller struct{ c *server.Client }

func (a appCaller) Call(ctx context.Context, by port.Principal, op string, in, out any) error {
	return a.c.Call(ctx, by, op, in, out)
}

func agentsetCaller(db *store.DB, stderr io.Writer) (agentset.Caller, error) {
	c, err := server.New(&app.Env{DB: app.Shared(db), Now: now, PendingQ: pendingQ, Warn: stderr})
	if err != nil {
		return nil, err
	}
	return appCaller{c}, nil
}

func agentsetName(args []string, verb string) (string, error) {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return "", usage("agentset %s <セット>", verb)
	}
	if !agentset.ReName.MatchString(args[0]) {
		return "", usage("セットの名前は英小文字で始まり、英小文字・数字・_ の 16 字以内: %s", args[0])
	}
	return args[0], nil
}

func cmdAgentsetAdd(args []string, w, stderr io.Writer) error {
	name, err := agentsetName(args, "add")
	if err != nil {
		return err
	}
	file := ""
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--file":
			if i+1 >= len(args) {
				return usage("--file に値が必要です")
			}
			file = args[i+1]
			i++
		default:
			return usage("agentset add: 不明な引数 %s", args[i])
		}
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := requireHuman("tb agentset add"); err != nil {
		return err
	}
	var body string
	if file != "" {
		st, err := os.Stat(file)
		b, rerr := os.ReadFile(file)
		if err != nil || !st.Mode().IsRegular() || rerr != nil {
			return die("agentset add: ファイルを読めません: %s", file)
		}
		body = string(b)
	} else if body, err = readStdin(); err != nil {
		return err
	}
	if err := agentset.Save(agentset.Dir(db.Path), name, body); err != nil {
		return usage("agentset add: %s(登録しませんでした)", err)
	}
	fmt.Fprintf(w, "agentset %s を登録しました\n", name)
	return nil
}

func cmdAgentsetRm(args []string, w, stderr io.Writer) error {
	name, err := agentsetName(args, "rm")
	if err != nil {
		return err
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := requireHuman("tb agentset rm"); err != nil {
		return err
	}
	if err := agentset.Remove(agentset.Dir(db.Path), name); err != nil {
		return die("agentset rm: %s", err)
	}
	fmt.Fprintf(w, "agentset %s を消しました\n", name)
	return nil
}

func cmdAgentsetList(args []string, w, stderr io.Writer) error {
	if len(args) > 0 {
		return usage("agentset list: 不明な引数 %s", args[0])
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	for _, n := range agentset.Names(agentset.Dir(db.Path)) {
		fmt.Fprintln(w, n)
	}
	return nil
}

func cmdAgentsetShow(args []string, w, stderr io.Writer) error {
	name, err := agentsetName(args, "show")
	if err != nil {
		return err
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	b, err := os.ReadFile(agentset.Dir(db.Path) + "/" + name + ".conf")
	if err != nil {
		return die("セット %s の定義がありません", name)
	}
	_, _ = w.Write(b)
	return nil
}

// agentsetLoad は定義を読み、API の呼び出しを用意する(呼び出し元を主体にする)。
func agentsetLoad(args []string, verb string, stderr io.Writer) (*store.DB, agentset.Def, agentset.Caller, port.Principal, error) {
	var by port.Principal
	name, err := agentsetName(args, verb)
	if err != nil {
		return nil, agentset.Def{}, nil, by, err
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return nil, agentset.Def{}, nil, by, err
	}
	d, err := agentset.Load(agentset.Dir(db.Path), name)
	if err != nil {
		db.Close()
		return nil, d, nil, by, die("agentset %s: %s", verb, err)
	}
	if by, err = callerPrincipal("tb agentset " + verb); err != nil {
		db.Close()
		return nil, d, nil, by, err
	}
	c, err := agentsetCaller(db, stderr)
	if err != nil {
		db.Close()
		return nil, d, nil, by, err
	}
	return db, d, c, by, nil
}

// agentsetLines は 1 体ごとの結果を出す(失敗は標準エラー)。最後の失敗を終了コードにする。
func agentsetLines(lines []agentset.Line, err error, w, stderr io.Writer) error {
	for _, l := range lines {
		if l.Err != nil {
			e := appErr(l.Err)
			if x, ok := e.(*exit); ok {
				fmt.Fprintf(stderr, "tb: %s: %s\n", l.Name, x.msg)
			} else {
				fmt.Fprintf(stderr, "tb: %s: %s\n", l.Name, e)
			}
			continue
		}
		fmt.Fprintf(w, "%s: %s\n", l.Name, l.Out)
	}
	if err == nil {
		return nil
	}
	e := appErr(err)
	if x, ok := e.(*exit); ok && len(lines) > 0 {
		return &exit{x.code, ""} // 理由は上の行で出した
	}
	return e
}

func cmdAgentsetUp(args []string, w, stderr io.Writer) error {
	if len(args) > 1 {
		return usage("agentset up: 不明な引数 %s", args[1])
	}
	db, d, c, by, err := agentsetLoad(args, "up", stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	lines, err := agentset.Up(context.Background(), c, by, d)
	return agentsetLines(lines, err, w, stderr)
}

func cmdAgentsetDown(args []string, w, stderr io.Writer) error {
	force := false
	for _, a := range args[min(1, len(args)):] {
		if a != "--force" {
			return usage("agentset down: 不明な引数 %s", a)
		}
		force = true
	}
	db, d, c, by, err := agentsetLoad(args, "down", stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	lines, err := agentset.Down(context.Background(), c, by, d, force)
	return agentsetLines(lines, err, w, stderr)
}

func cmdAgentsetStatus(args []string, w, stderr io.Writer) error {
	asJSON := false
	for _, a := range args[min(1, len(args)):] {
		if a != "--json" {
			return usage("agentset status: 不明な引数 %s", a)
		}
		asJSON = true
	}
	db, d, c, by, err := agentsetLoad(args, "status", stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := agentset.Status(context.Background(), c, by, d)
	if err != nil {
		return appErr(err)
	}
	if asJSON {
		b, _ := json.Marshal(rows)
		fmt.Fprintln(w, string(b))
		return nil
	}
	dash := func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	}
	for _, r := range rows {
		diff := ""
		if r.Diff {
			diff = "\t定義の親は " + dash(r.Want)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s%s\n", r.Name, r.Member, r.State, dash(r.Pane), dash(r.Parent), diff)
	}
	return nil
}

func cmdAgentsetScale(args []string, w, stderr io.Writer) error {
	if len(args) != 3 {
		return usage("agentset scale <セット> <メンバー> <N|+N|-N>")
	}
	db, d, c, by, err := agentsetLoad(args[:1], "scale", stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	lines, err := agentset.Scale(context.Background(), c, by, d, args[1], args[2])
	return agentsetLines(lines, err, w, stderr)
}

func cmdAgentsetRun(args []string, w, stderr io.Writer) error {
	if len(args) < 2 || strings.HasPrefix(args[1], "-") {
		return usage("agentset run <セット> <メンバー> --task T [--brief B]")
	}
	task, brief := "", ""
	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "--task", "--brief":
			if i+1 >= len(args) {
				return usage("%s に値が必要です", args[i])
			}
			if args[i] == "--task" {
				task = args[i+1]
			} else {
				brief = args[i+1]
			}
			i++
		default:
			return usage("agentset run: 不明な引数 %s", args[i])
		}
	}
	if task == "" {
		return usage("agentset run: --task が必要です")
	}
	db, d, c, by, err := agentsetLoad(args[:1], "run", stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	lines, err := agentset.Run(context.Background(), c, by, d, args[1], task, brief)
	return agentsetLines(lines, err, w, stderr)
}

func cmdAgentsetReap(args []string, w, stderr io.Writer) error {
	if len(args) > 1 {
		return usage("agentset reap: 不明な引数 %s", args[1])
	}
	db, d, c, by, err := agentsetLoad(args, "reap", stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	lines, err := agentset.Reap(context.Background(), c, by, d)
	if err == nil && len(lines) == 0 {
		fmt.Fprintln(w, "片付けるものはありません")
	}
	return agentsetLines(lines, err, w, stderr)
}
