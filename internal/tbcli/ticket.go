package tbcli

// tb ticket <サブコマンド>(taskboard/lib/tasks.sh の cmd_ticket / ticket_move / ticket_status)。
// 状態そのものの中身は設定 statuses。別の置き場は作らない。

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/app/ticket"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

const ticketUsage = `tb ticket move <id> <状態> [-m メモ] [-d 成果物 ...]   どの状態からどの状態へも(閉じた状態から戻すのも)。
      -m は閉じた分類(done / cancelled)なら要約、それ以外ならメモ(note)に入る。-d は done の分類へ動かすときだけ
tb ticket status [list] [--json]                      状態・分類・説明・チケットの件数
tb ticket status add <状態> <分類> [-m 説明] [--after <状態>]   状態を足す(設定 statuses に書き足す。人だけ)
tb ticket status rm <状態> [--to <状態>]              状態を外す(その状態のチケットが残っていれば --to で移すまで外さない。人だけ)
tb ticket rm <id> [<id> ...]                         チケットを消す(コメント・履歴・リンクも。子があれば子も一緒に指定する。agent からは ask)
tb ticket add|list|show|update|claim|done|block|cancel ...   従来の tb <それ> と同じ
`

// cmdTicket は tb ticket <サブコマンド>。
func cmdTicket(args []string, w, stderr io.Writer) error {
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "move":
		return ticketMove(args, w, stderr)
	case "status", "statuses":
		return ticketStatus(args, w, stderr)
	case "add":
		return cmdAdd(args, w, stderr)
	case "list":
		return cmdList(args, w, stderr)
	case "show":
		return cmdShow(args, w, stderr)
	case "update":
		return cmdUpdate(args, w, stderr)
	case "claim":
		return cmdClaim(args, w, stderr)
	case "done":
		return cmdDone(args, w, stderr)
	case "block":
		return cmdBlock(args, w, stderr)
	case "cancel":
		return cmdCancel(args, w, stderr)
	case "report":
		return cmdReport(args, w, stderr)
	case "history":
		return cmdHistory(args, w, stderr)
	case "tree":
		return cmdTree(args, w, stderr)
	case "link":
		return cmdLink(args, w, stderr)
	case "unlink":
		return cmdUnlink(args, w, stderr)
	case "rm":
		return ticketRm(args, w, stderr)
	case "", "-h", "--help", "help":
		fmt.Fprint(w, ticketUsage)
		if sub == "" {
			return &exit{exitUsage, ""}
		}
		return nil
	}
	return usage("ticket: 不明なサブコマンド %s(move / status / rm / add / list / show / update / claim / done / block / cancel)", sub)
}

// ticketMove は ticket_move。中身はアプリケーション API の ticket.move(ADR 0008)。
// ここは引数を読み、呼び出し元を主体にして呼び、結果を今の文言にする。
func ticketMove(args []string, w, stderr io.Writer) error {
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(args) < 2 {
		return usage("ticket move <id> <状態> [-m メモ] [-d 成果物 ...]")
	}
	in := ticket.MoveReq{ID: args[0], Status: args[1], Deliverables: []string{}}
	if err := needID(in.ID); err != nil {
		return err
	}
	for i := 2; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-m", "--message", "--summary", "-d", "--deliverable":
		default:
			return usage("ticket move: 不明な引数 %s", a)
		}
		v, err := argv(args, i)
		if err != nil {
			return err
		}
		i++
		if a == "-d" || a == "--deliverable" {
			if !blank(v) {
				in.Deliverables = append(in.Deliverables, v)
			}
		} else {
			in.Message, in.SetMessage = v, true
		}
	}
	if err := eventActorResolve(db); err != nil {
		return err
	}
	var res ticket.MoveResult
	if err := callApp(db, "tb ticket move", "ticket.move", in, &res); err != nil {
		return err
	}
	if res.Pending != nil {
		return pendingExit(w, res.Pending)
	}
	if !res.Changed {
		fmt.Fprintf(w, "%s はすでに %s です\n", res.ID, res.To)
		return nil
	}
	fmt.Fprintf(w, "%s %s → %s\n", res.ID, res.From, res.To)
	return nil
}

// moveDirect は ticket status rm が、外す状態のチケットを移し先へ動かす(人だけのコマンドの中。門はコマンドの入口で済んでいる)。
func moveDirect(db *store.DB, st tickets.StatusSet, id, to string) error {
	cur, changed, err := tickets.Move(db, st, id, to, "", false, nil, now())
	if err != nil || !changed {
		return ticketErr(err)
	}
	statusEnteredHook(db, st, id, to, cur)
	rulesHook(db, st)
	return nil
}

// ticketStatus は ticket_status。
func ticketStatus(args []string, w, stderr io.Writer) error {
	sub := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "list":
		return ticketStatusList(args, w, stderr)
	case "add":
		return ticketStatusAdd(args, w, stderr)
	case "rm", "remove":
		return ticketStatusRm(args, w, stderr)
	}
	return usage("ticket status [list [--json] | add <状態> <分類> [-m 説明] [--after <状態>] | rm <状態> [--to <状態>]]")
}

// statusDescs は statuses_parse の説明(<状態> <分類> = 説明)。本文が不正なら空(組み込みに戻るので説明も無い)。
func statusDescs(body string) map[string]string {
	out := map[string]string{}
	if _, _, ok := tickets.ParseStatuses(body); !ok {
		return out
	}
	for _, line := range config.Lines(body) {
		line = config.Trim(config.StripComment(line))
		k, d, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		name, _ := config.FirstWord(config.Trim(k))
		out[name] = config.Trim(d)
	}
	return out
}

// statusBody は ticket_status_body: 今の本文(設定が無ければ組み込みの 6 つを書式どおりに)。末尾の改行は付けない。
func statusBody(db *store.DB) string {
	if body, _, ok := config.Lookup(db.DB, db.Session, "statuses"); ok {
		return strings.TrimRight(body, "\n")
	}
	var b strings.Builder
	for i, k := range tickets.Statuses {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%-12s %s", k, tickets.BuiltinCat(k))
	}
	return b.String()
}

func statusSource(db *store.DB) string {
	_, src, _ := config.Lookup(db.DB, db.Session, "statuses")
	return src
}

// statusWrite は ticket_status_write: 今読んでいる行(このセッション / 共通 / 明示したファイル)へ書く。設定が無ければ共通の行。
func statusWrite(db *store.DB, st tickets.StatusSet, body, how string, stderr io.Writer) error {
	if msg := tickets.ValidateStatuses(body); msg != "" {
		fmt.Fprint(stderr, msg)
		return die("statuses の本文に問題があるため書きませんでした")
	}
	info, _ := config.Info("statuses")
	if f := os.Getenv(info.Env); f != "" {
		return writeConfigFile(f, body)
	}
	return config.DBPut(db.DB, db.Session, "statuses", body, how, statusSource(db) != "db:"+db.Session, "")
}

func ticketStatusList(args []string, w, stderr io.Writer) error {
	db, st, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	js := false
	for _, a := range args {
		if a != "--json" {
			return usage("ticket status list [--json]")
		}
		js = true
	}
	counts := map[string]int64{}
	var order []string
	if err := qrows(db.DB, "SELECT status, COUNT(*) FROM tasks GROUP BY status", nil, func(_ []string, v []any) error {
		k := tickets.Text(v[0])
		n, _ := v[1].(int64)
		counts[k] = n
		order = append(order, k)
		return nil
	}); err != nil {
		return err
	}
	body, src, _ := config.Lookup(db.DB, db.Session, "statuses")
	descs := statusDescs(body)
	all := boardCols(st)
	for _, k := range order {
		if !slices.Contains(all, k) {
			all = append(all, k)
		}
	}
	for _, k := range all {
		c, n, d := st.Cat(k), counts[k], descs[k]
		configured := slices.Contains(st.Names, k)
		if js {
			fmt.Fprintln(w, jv.Compact(jv.Obj{{K: "status", V: k}, {K: "category", V: c}, {K: "description", V: d}, {K: "tickets", V: jv.Number(fmt.Sprint(n))}, {K: "configured", V: configured}}))
			continue
		}
		if c == "" {
			c = "-"
		}
		if d == "" && !configured {
			d = "(設定から外した状態)"
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", k, c, n, d)
	}
	if !js {
		fmt.Fprintf(stderr, "# 設定 statuses: %s(状態 分類 件数 説明)\n", src)
	}
	return nil
}

// statusLineName は awk の split($0 から # 以降を除いたもの, f, " ") の f[1]。
func statusLineName(line string) string {
	f := strings.Fields(config.StripComment(line))
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

func ticketStatusAdd(args []string, w, stderr io.Writer) error {
	db, st, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(args) < 2 {
		return usage("ticket status add <状態> <分類> [-m 説明] [--after <状態>]")
	}
	name, cat := args[0], args[1]
	desc, after := "", ""
	for i := 2; i < len(args); i++ {
		a := args[i]
		var p *string
		switch a {
		case "-m", "--desc", "--description":
			p = &desc
		case "--after":
			p = &after
		default:
			return usage("ticket status add: 不明な引数 %s", a)
		}
		v, err := argv(args, i)
		if err != nil {
			return err
		}
		*p = v
		i++
	}
	if err := requireHuman("tb ticket status add"); err != nil {
		return err
	}
	if !slices.Contains(tickets.Categories, cat) {
		return usage("ticket status add: 分類は %s のどれか: %s", strings.Join(tickets.Categories, " "), cat)
	}
	if strings.ContainsAny(desc, "\n#") {
		return usage("ticket status add: 説明に改行と # は使えません")
	}
	if slices.Contains(st.Names, name) {
		return die("状態 %s はすでにあります(分類 %s)", name, st.Cat(name))
	}
	if after != "" && !slices.Contains(st.Names, after) {
		return usage("ticket status add: --after の状態がありません: %s", after)
	}
	line := name + " " + cat
	if desc != "" {
		line += " = " + desc
	}
	cur := statusBody(db)
	var body string
	if after != "" { // その状態の行の直後に入れる(書いた順が選択肢と列の順)
		var out []string
		done := false
		for _, l := range strings.Split(cur, "\n") {
			out = append(out, l)
			if !done && statusLineName(l) == after {
				out = append(out, line)
				done = true
			}
		}
		body = strings.TrimRight(strings.Join(out, "\n"), "\n")
	} else {
		body = cur + "\n" + line
	}
	if err := statusWrite(db, st, body, "ticket status add "+name, stderr); err != nil {
		return err
	}
	fmt.Fprintf(w, "状態 %s を足しました(分類 %s。設定 statuses: %s)\n", name, cat, statusSource(db))
	return nil
}

func ticketStatusRm(args []string, w, stderr io.Writer) error {
	db, st, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(args) < 1 {
		return usage("ticket status rm <状態> [--to <状態>]")
	}
	name, to := args[0], ""
	for i := 1; i < len(args); i++ {
		if args[i] != "--to" {
			return usage("ticket status rm: 不明な引数 %s", args[i])
		}
		v, err := argv(args, i)
		if err != nil {
			return err
		}
		to = v
		i++
	}
	if err := requireHuman("tb ticket status rm"); err != nil {
		return err
	}
	if !slices.Contains(st.Names, name) {
		return die("状態 %s は設定にありません(tb ticket status で一覧)", name)
	}
	if to != "" && to == name {
		return usage("ticket status rm: --to に外す状態そのものは指定できません")
	}
	if to != "" {
		if err := oneOf(st.Names, to, "status"); err != nil {
			return err
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE status=?`, name).Scan(&n); err != nil {
		return err
	}
	// アラート・提案のチケットは DB のトリガーが組み込みの名前で書くので、組み込みの名前を外してもチケットは移さない
	if n > 0 && to == "" {
		ex := st.Main(st.Cat(name))
		if ex == name {
			ex = st.Main("todo")
		}
		return die("状態 %s のチケットが %d 件あります。--to <状態> で移してから外します(例: tb ticket status rm %s --to %s)", name, n, name, ex)
	}
	var keep []string
	for _, l := range strings.Split(statusBody(db), "\n") {
		if statusLineName(l) != name {
			keep = append(keep, l)
		}
	}
	body := strings.TrimRight(strings.Join(keep, "\n"), "\n")
	if msg := tickets.ValidateStatuses(body); msg != "" {
		// bash は set -e で statuses_parse の失敗のところで終わるので、続く die の文は出ない(同じにする。bash を外したら文を出す)
		fmt.Fprint(stderr, msg)
		return &exit{exitErr, ""}
	}
	if n > 0 {
		var ids []string
		if err := qrows(db.DB, `SELECT id FROM tasks WHERE status=? ORDER BY id`, []any{name}, func(_ []string, v []any) error {
			ids = append(ids, tickets.Text(v[0]))
			return nil
		}); err != nil {
			return err
		}
		moved := 0
		for _, id := range ids {
			var x int
			if db.QueryRow(`SELECT 1 FROM alert_details WHERE task_id=?1 UNION ALL SELECT 1 FROM proposal_details WHERE task_id=?1 LIMIT 1`, id).Scan(&x) == nil {
				continue
			}
			if err := moveDirect(db, st, id, to); err != nil {
				return err
			}
			moved++
		}
		fmt.Fprintf(w, "状態 %s のチケット %d 件を %s へ移しました\n", name, moved, to)
	}
	if err := statusWrite(db, st, body, "ticket status rm "+name, stderr); err != nil {
		return err
	}
	fmt.Fprintf(w, "状態 %s を外しました(設定 statuses: %s)\n", name, statusSource(db))
	return nil
}
