package tbcli

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/gate"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/identity"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/review"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

var (
	reChain   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	reGatePR  = regexp.MustCompile(`^([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+)#([0-9]+)$`)
	reLineage = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	reSha     = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	reCount4  = regexp.MustCompile(`^[0-9]{1,4}$`)
	reEvid    = regexp.MustCompile(`^[A-Za-z0-9._/@+~-]{1,200}$`)
	reGatePRn = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+#[0-9]+$`)
)

const gateUsage = "使い方: tb gate evaluate|execute <chain-id> --action merge|approve / tb gate ingest-review <task-id> --pr <o>/<r>#<n> --sha <sha> / tb gate ingest-events <task-id> --pr <o>/<r>#<n> / tb gate reviewers <o>/<r>#<n> [--task <id>]"

// cmdGate は tb gate <evaluate|execute|ingest-review|ingest-events|reviewers>(taskboard/lib/gate.sh)。
func cmdGate(args []string, w, stderr io.Writer, stdin io.Reader) error {
	db, _, err := openCmd(stderr) // tb 共通の起動処理(DB を開けなければ 1)
	if err != nil {
		return err
	}
	defer db.Close()
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	fs := gate.Fixed()
	switch sub {
	case "evaluate":
		return gateEvaluate(fs, args, w, stdin)
	case "execute":
		if _, _, _, _, _, err := gateArgs(false, args); err != nil {
			return err
		}
		fmt.Fprintln(stderr, "gate: execute は無効です(段 0。何も実行せず、何も記録しません)")
		return &exit{30, ""}
	case "ingest-review":
		return gateIngestReview(db, fs, args, w, stderr)
	case "ingest-events":
		return gateIngestEvents(db, fs, args, w, stderr)
	case "reviewers":
		return gateReviewers(fs, args, w)
	}
	return usage("%s", gateUsage)
}

// gateArgs は _gate_args: <chain-id> --action merge|approve(evaluate は --pr / --fact / --facts-stdin も)。
func gateArgs(facts bool, args []string) (chain, action, pr string, fl []string, stdin bool, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--action":
			if i+1 >= len(args) {
				return "", "", "", nil, false, usage("--action に値がありません")
			}
			action = args[i+1]
			i++
		case a == "--pr":
			if !facts || i+1 >= len(args) {
				return "", "", "", nil, false, usage("--pr は evaluate だけで、値が要ります")
			}
			pr = args[i+1]
			i++
		case a == "--fact":
			if !facts || i+1 >= len(args) {
				return "", "", "", nil, false, usage("--fact は evaluate だけで、値が要ります")
			}
			fl = append(fl, args[i+1])
			i++
		case a == "--facts-stdin":
			if !facts {
				return "", "", "", nil, false, usage("--facts-stdin は evaluate だけです")
			}
			stdin = true
		case strings.HasPrefix(a, "-"):
			return "", "", "", nil, false, usage("不明な引数: %s", a)
		default:
			if chain != "" {
				return "", "", "", nil, false, usage("chain-id は 1 つだけです")
			}
			chain = a
		}
	}
	if !reChain.MatchString(chain) {
		return "", "", "", nil, false, usage("chain-id が不正です(英数字 . _ - の 64 文字まで)")
	}
	if action != "merge" && action != "approve" {
		return "", "", "", nil, false, usage("--action は merge か approve です")
	}
	return chain, action, pr, fl, stdin, nil
}

func gateEvaluate(fs gate.Files, args []string, w io.Writer, stdin io.Reader) error {
	chain, action, pr, in, useStdin, err := gateArgs(true, args)
	if err != nil {
		return err
	}
	var facts []string
	if useStdin {
		b, _ := io.ReadAll(stdin)
		s := string(b)
		if s != "" {
			ls := strings.Split(s, "\n")
			if ls[len(ls)-1] == "" {
				ls = ls[:len(ls)-1]
			}
			facts = append(facts, ls...)
		}
	}
	facts = append(facts, in...)
	r := gate.Eval(fs, action, chain, pr, facts)
	fmt.Fprint(w, r.Format())
	if r.Exit != 0 {
		return &exit{r.Exit, ""}
	}
	return nil
}

func taskExists(db *store.DB, tid string) error {
	ok, err := tickets.Exists(db.DB, tid)
	if err != nil || !ok {
		return die("タスクが見つかりません: %s", tid)
	}
	return nil
}

// prOpts は ingest-review / ingest-events の引数(<task-id> --pr ... [--sha ...])。
func prOpts(name, use string, args []string, withSha bool) (tid, owner, repo, num, sha string, err error) {
	if len(args) < 1 {
		return "", "", "", "", "", usage("%s", use)
	}
	tid = args[0]
	if !reID.MatchString(tid) {
		return "", "", "", "", "", usage("タスク ID は T-<project>-NNN 形式: %s", tid)
	}
	pr := ""
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--pr" || (withSha && a == "--sha"):
			if i+1 >= len(args) {
				return "", "", "", "", "", usage("%s に値が必要です", a)
			}
			if a == "--pr" {
				pr = args[i+1]
			} else {
				sha = args[i+1]
			}
			i++
		default:
			return "", "", "", "", "", usage("%s: 不明な引数 %s", name, a)
		}
	}
	m := reGatePR.FindStringSubmatch(pr)
	if m == nil {
		return "", "", "", "", "", usage("--pr は <owner>/<repo>#<n>")
	}
	if withSha && !reSha.MatchString(sha) {
		return "", "", "", "", "", usage("--sha は 7〜40 桁の小文字 16 進")
	}
	return tid, m[1], m[2], m[3], sha, nil
}

func gateIngestReview(db *store.DB, fs gate.Files, args []string, w, stderr io.Writer) error {
	tid, owner, repo, num, sha, err := prOpts("gate ingest-review", "gate ingest-review <task-id> --pr <o>/<r>#<n> --sha <head の sha>", args, true)
	if err != nil {
		return err
	}
	// agent の pane からは実行できない(送信者 ingest を名乗らせない)。既知の限界: 環境変数を外せば破れる(docs/gate.md)
	if os.Getenv("HERDR_PANE_ID") != "" {
		return &exit{5, "gate ingest-review は agent の pane からは実行できません"}
	}
	if err := taskExists(db, tid); err != nil {
		return err
	}
	c, cerr := gate.LoadIngestConf(fs.IngestConf, true)
	if cerr != "" {
		return &exit{1, "gate ingest-review: " + cerr}
	}
	for _, x := range c.Warns {
		fmt.Fprintln(stderr, "warn: "+x)
	}
	base := "repos/" + owner + "/" + repo
	var parts [3][]any
	for i, p := range []string{base + "/pulls/" + num + "/reviews", base + "/pulls/" + num + "/comments", base + "/issues/" + num + "/comments"} {
		if parts[i], err = gate.Fetch(c.GH, p); err != nil {
			return &exit{12, "gate ingest-review: 取得または解析に失敗したため記録しません(記録なし = 条件を満たさない)"}
		}
	}
	res, err := c.IngestReview(parts[0], parts[1], parts[2], sha)
	if err != nil {
		return &exit{12, "gate ingest-review: 解析に失敗したため記録しません"}
	}
	switch res.Status {
	case "none":
		return &exit{10, "gate ingest-review: head(" + sha + ")に対する外部レビューがありません。記録しません(記録なし = 条件を満たさない)"}
	case "skipped":
		return &exit{11, "gate ingest-review: 外部レビューはスキップされています(上限など)。pass とは記録しません"}
	}
	for _, n := range []int{res.Blocker, res.Should, res.Unk} {
		if !reCount4.MatchString(strconv.Itoa(n)) {
			return &exit{12, "gate ingest-review: 件数が範囲外のため記録しません"}
		}
	}
	if res.Unk != 0 {
		fmt.Fprintf(stderr, "warn: 重大度の表記が見つからない指摘が %d 件あります(should として数えました。blocker には数えていません)\n", res.Unk)
	}
	ver := "pass"
	if res.Blocker != 0 {
		ver = "fail"
	}
	pr := owner + "/" + repo + "#" + num
	if err := gate.AppendRecord(fs.Reviews, tid, gate.IngestSender, pr, "coderabbit", sha, res.Blocker, res.Should, ver, "-"); err != nil {
		return die("%s", err.Error())
	}
	fmt.Fprintf(w, "recorded: %s REVIEW coderabbit %s blocker=%d should=%d verdict=%s by=%s unknown-label=%d\n", tid, short7(sha), res.Blocker, res.Should, ver, gate.IngestSender, res.Unk)
	return nil
}

func short7(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

func gateIngestEvents(db *store.DB, fs gate.Files, args []string, w, stderr io.Writer) error {
	tid, owner, repo, num, _, err := prOpts("gate ingest-events", "gate ingest-events <task-id> --pr <o>/<r>#<n>", args, false)
	if err != nil {
		return err
	}
	if os.Getenv("HERDR_PANE_ID") != "" {
		return &exit{5, "gate ingest-events は agent の pane からは実行できません"}
	}
	if err := taskExists(db, tid); err != nil {
		return err
	}
	c, cerr := gate.LoadIngestConf(fs.IngestConf, false)
	if cerr != "" {
		return &exit{1, "gate ingest-events: " + cerr}
	}
	for _, x := range c.Warns {
		fmt.Fprintln(stderr, "warn: "+x)
	}
	keys, err := gate.EventKeys(fs.Events)
	if err != nil {
		return &exit{1, "gate ingest-events: " + err.Error()}
	}
	base := "repos/" + owner + "/" + repo
	fail := &exit{12, "gate ingest-events: 取得または解析に失敗したため記録しません"}
	pr, err := gate.FetchObj(c.GH, base+"/pulls/"+num)
	if err != nil {
		return fail
	}
	var parts [3][]any
	for i, p := range []string{base + "/pulls/" + num + "/reviews", base + "/pulls/" + num + "/comments", base + "/issues/" + num + "/comments"} {
		if parts[i], err = gate.Fetch(c.GH, p); err != nil {
			return fail
		}
	}
	if !gate.PRValid(pr) {
		return &exit{12, "gate ingest-events: PR 本体の作者 id または head の sha が不正なため、何も記録しません"}
	}
	nl, skip, err := c.IngestEvents(keys, pr, parts[0], parts[1], parts[2], tid, owner, repo, num)
	if err != nil {
		e := err.(*gate.EventsError)
		return &exit{e.Code, "gate ingest-events: " + e.Msg}
	}
	if err := gate.AppendLines(fs.Events, nl); err != nil {
		return die("%s", err.Error())
	}
	fmt.Fprintf(w, "recorded: %s %s/%s#%s events: new=%d unchanged=%d\n", tid, owner, repo, num, len(nl), skip)
	return nil
}

func gateReviewers(fs gate.Files, args []string, w io.Writer) error {
	if len(args) < 1 {
		return usage("gate reviewers <o>/<r>#<n> [--task <id>]")
	}
	pr, task := args[0], ""
	for i := 1; i < len(args); i++ {
		if args[i] != "--task" {
			return usage("gate reviewers: 不明な引数 %s", args[i])
		}
		if i+1 >= len(args) {
			return usage("--task に値が必要です")
		}
		task = args[i+1]
		i++
	}
	m := reGatePR.FindStringSubmatch(pr)
	if m == nil {
		return usage("引数は <owner>/<repo>#<n>")
	}
	if task != "" && !reChain.MatchString(task) {
		return usage("--task の形が不正です")
	}
	repo, num := m[1]+"/"+m[2], m[3]
	fmt.Fprintf(w, "reviewers: %s#%s   # 表示だけ。判定には使われません(段 1c)\n", repo, num)
	fmt.Fprintln(w, "note: label-blocker / label-should は、投稿者が書いた本文の最初の実質行の語から機械的に数えた値で、検証された重大度ではありません(誰でも書ける)。判定には使われません")
	n, out, ok, err := gate.Reviewers(fs.Events, repo, num, task)
	if err != nil {
		flush(w)
		return die("gate reviewers: %s", err.Error())
	}
	if !ok {
		fmt.Fprintln(w, "events: 0 (記録がありません)")
		return nil
	}
	fmt.Fprintf(w, "events: %d   reviewers: %d\n", n, len(out))
	for _, l := range out {
		fmt.Fprintln(w, l)
	}
	return nil
}

// cmdReview は tb review <pack|submit> と、古い名前 review-pack / review-submit。
func cmdReview(args []string, w, stderr io.Writer, legacy string) error {
	sub := legacy
	if sub == "" {
		if len(args) == 0 {
			fmt.Fprint(w, reviewUsage)
			return &exit{exitUsage, ""}
		}
		sub, args = args[0], args[1:]
		switch sub {
		case "help", "-h", "--help":
			fmt.Fprint(w, reviewUsage)
			return nil
		case "pack":
			sub = "review-pack"
		case "submit":
			sub = "review-submit"
		default:
			return usage("review: 不明なサブコマンド %s(pack / submit)", sub)
		}
	}
	db, st, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if sub == "review-pack" {
		return reviewPack(db, st, args, w)
	}
	return reviewSubmit(db, args, w)
}

const reviewUsage = `tb review pack [--since ISO8601] [--mark]   会計役に渡すレビュー材料(集計と上位だけ)
tb review submit <task-id> --lineage L --sha S --blocker N --should N --verdict pass|fail [--pr o/r#n] [--evidence PATH]   レビュー結果を記録する
(古い名前 tb review-pack / tb review-submit も同じに使える)
`

func reviewPack(db *store.DB, st tickets.StatusSet, args []string, w io.Writer) error {
	since, mark := "", false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--since":
			if i+1 >= len(args) {
				return usage("--since に値が必要です")
			}
			since = args[i+1]
			i++
		case "--mark":
			mark = true
		default:
			return usage("review-pack: 不明な引数 %s", args[i])
		}
	}
	if err := review.Pack(db.DB, db.Session, since, mark, st.SQLList("active", "review", "blocked"), w); err != nil {
		if err == review.ErrSince {
			return usage("%s", err.Error())
		}
		if err == review.ErrJq {
			return &exit{code: 5}
		}
		return die("%s", err.Error())
	}
	return nil
}

// reviewSubmit は cmd_review-submit。終了コード: 0 記録した / 2 使い方 / 3 送信者を特定できない / 4 作者と同一・作者を確かめられない / 5 役割・系統が許されない / 1 その他。
func reviewSubmit(db *store.DB, args []string, w io.Writer) error {
	if len(args) < 1 {
		return usage("review-submit <task-id> --lineage L --sha S --blocker N --should N --verdict pass|fail [--pr o/r#n] [--evidence PATH]")
	}
	tid := args[0]
	if !reID.MatchString(tid) {
		return usage("タスク ID は T-<project>-NNN 形式: %s", tid)
	}
	opt := map[string]string{"--pr": "-", "--evidence": "-"}
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--lineage", "--sha", "--blocker", "--should", "--verdict", "--pr", "--evidence":
			if i+1 >= len(args) {
				return usage("%s に値が必要です", a)
			}
			opt[a] = args[i+1]
			i++
		default:
			return usage("review-submit: 不明な引数 %s", a)
		}
	}
	lin, sha, blkS, shdS, ver, pr, ev := opt["--lineage"], opt["--sha"], opt["--blocker"], opt["--should"], opt["--verdict"], opt["--pr"], opt["--evidence"]
	switch {
	case !reLineage.MatchString(lin):
		return usage("--lineage は小文字英字で始まる英小文字・数字・- の 32 文字まで(例: self, coderabbit)")
	case !reSha.MatchString(sha):
		return usage("--sha は 7〜40 桁の小文字 16 進")
	case !reCount4.MatchString(blkS):
		return usage("--blocker は 0 以上の整数")
	case !reCount4.MatchString(shdS):
		return usage("--should は 0 以上の整数")
	case ver != "pass" && ver != "fail":
		return usage("--verdict は pass か fail")
	case pr != "-" && !reGatePRn.MatchString(pr):
		return usage("--pr は <owner>/<repo>#<n>")
	case ev != "-" && !reEvid.MatchString(ev):
		return usage("--evidence は空白のない 200 文字までのパス")
	}
	blk, _ := strconv.Atoi(blkS)
	shd, _ := strconv.Atoi(shdS)
	if ver != "fail" && blk != 0 {
		return usage("verdict=pass には blocker=0 が要ります")
	}
	// 送信者(pane から決める。自己申告にしない)
	by, agent := "human", false
	if pane := os.Getenv("HERDR_PANE_ID"); pane != "" {
		n, err := identity.PaneOwnerName(pane)
		if err != nil {
			return &exit{3, "herdr agent list に失敗。送信者を特定できないため記録しません"}
		}
		if !reChain.MatchString(n) {
			return &exit{3, "pane " + pane + " の agent の名前を特定できないため記録しません"}
		}
		by, agent = n, true
	}
	if by == gate.IngestSender {
		return &exit{5, "送信者名 " + gate.IngestSender + " は取り込み口の予約名です。記録しません"}
	}
	if err := taskExists(db, tid); err != nil {
		return err
	}
	var owner string
	_ = db.QueryRow(`SELECT COALESCE(owner_agent,'') FROM tasks WHERE id=?`, tid).Scan(&owner)
	fs := gate.Fixed()
	if agent {
		// 最小の職務分掌: agent はレビュー担当の役割だけ。作者と同一は拒否。作者が分からないときも拒否
		if owner == "" {
			return &exit{4, "タスクの owner がいないため、作者と別かを確かめられません。記録しません"}
		}
		if lowerASCIIs(owner) == lowerASCIIs(by) {
			return &exit{4, "作者(タスクの owner: " + owner + ")は自分の成果物のレビュー結果を記録できません"}
		}
		role := agents.NewRolesIn(db.DB, db.Session, fs.RolesDir).First(by)
		role = strings.TrimSuffix(role[strings.LastIndexByte(role, '/')+1:], ".md")
		if role == "" || !slices.Contains(gate.ReviewRoles, role) {
			r := role
			if r == "" {
				r = "なし"
			}
			return &exit{5, "この役割(" + r + ")は review-submit できません(レビュー担当の役割だけ)"}
		}
		if slices.Contains(gate.ReservedLineages, lin) {
			return &exit{5, "系統 " + lin + " は外部の取り込み用で、agent は名乗れません"}
		}
	}
	if err := gate.AppendRecord(fs.Reviews, tid, by, pr, lin, sha, blk, shd, ver, ev); err != nil {
		return die("%s", err.Error())
	}
	fmt.Fprintf(w, "recorded: %s REVIEW %s %s blocker=%d should=%d verdict=%s by=%s\n", tid, lin, short7(sha), blk, shd, ver, by)
	return nil
}

// lowerASCIIs は tr A-Z a-z。
func lowerASCIIs(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
