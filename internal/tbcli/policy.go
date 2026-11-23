package tbcli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/env"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/perms"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
)

// tb policy(サービス policy。認可の表 policy の変換と確かめ。ADR 0007。bash の cmd_policy)

const policyUsageText = `tb policy import [--apply]   古い設定 approvals と grants を設定 policy の行に変換する(人だけ)。
                             --apply が無ければ、足す行・変換しない行・確かめてほしい行を出すだけ。
                             --apply で policy に足し、古い設定を消す(checkout のファイルは退かす)
`

func cmdPolicy(args []string, w, stderr io.Writer) error {
	sub := "help"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "import":
		return cmdPolicyImport(args[1:], w, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(w, policyUsageText)
		return nil
	}
	fmt.Fprint(stderr, policyUsageText)
	return &exit{2, ""}
}

// piOverlap: 2 つのパターン(glob か @<役割> か関係)が同じものに当たりうるか(分からなければ当たりうるとみなす)
func piOverlap(a, b string) bool {
	if a == b {
		return true
	}
	return strings.ContainsAny(a, "*?@~") || strings.ContainsAny(b, "*?@~") || a == "self" || b == "self"
}

var rePolicyOp = regexp.MustCompile(`^[A-Za-z0-9._:*?@-]{1,128}$`)

// policyConvert は bash の policy_convert: 足す行・変換しない行・確かめてほしい行を返す。
func policyConvert(ab, gb string) (rows, skip, warn string) {
	var R, S, W strings.Builder
	defallow := perms.OpsAllowDefaults()
	type ent struct {
		k, op, t, v string
		n           int
		emit        bool
	}
	var es []ent
	for n, line := range splitBody(ab) {
		n++
		l := config.Trim(cutHash(line))
		if l == "" {
			continue
		}
		if !strings.Contains(l, "=") {
			fmt.Fprintf(&S, "approvals %d 行目: <操作> = <値> の形ではない: %s\n", n, l)
			continue
		}
		lhs, rhs, _ := strings.Cut(l, "=")
		k, v := config.Trim(lhs), config.Trim(rhs)
		if strings.HasPrefix(k, "cmd:") {
			fmt.Fprintf(&S, "approvals %d 行目: cmd: の行は前から効いていない(コマンドの権限は policy に <主体> %s * = <効果> と書く): %s\n", n, k, l)
			continue
		}
		if v != "ask" && v != "allow" {
			fmt.Fprintf(&S, "approvals %d 行目: 値は ask か allow だけ: %s\n", n, l)
			continue
		}
		if !rePolicyOp.MatchString(k) {
			fmt.Fprintf(&S, "approvals %d 行目: 操作名に使えない文字がある: %s\n", n, l)
			continue
		}
		op, t := perms.SplitAct(k)
		if t == "" {
			t = "*"
		}
		es = append(es, ent{k: k, op: op, t: t, v: v, n: n})
	}
	for i := range es {
		if es[i].v == "allow" {
			es[i].emit = true
			continue
		}
		need := false
		// (a) 既定が allow の操作を締める ask(上の行で全部が決まっていなければ)
		for _, d := range defallow {
			if !util.Glob(es[i].op, d) {
				continue
			}
			cover := false
			for j := 0; j < i; j++ {
				if es[j].t == "*" && util.Glob(es[j].op, d) {
					cover = true
					break
				}
			}
			if !cover {
				need = true
			}
		}
		// (b) 下に、同じものに当たりうる allow の行がある
		for j := i + 1; j < len(es) && !need; j++ {
			if es[j].v == "allow" && piOverlap(es[i].op, es[j].op) && piOverlap(es[i].t, es[j].t) {
				need = true
			}
		}
		es[i].emit = need
	}
	for i, e := range es {
		if !e.emit {
			fmt.Fprintf(&S, "approvals %d 行目: 既定(perms/ops.tbl)と同じなので書かない: %s = %s\n", e.n, e.k, e.v)
			continue
		}
		v := e.v
		if v == "ask" {
			v = "ask-any"
		}
		ops := perms.OpsOf(e.op)
		if len(ops) == 0 {
			fmt.Fprintf(&S, "approvals %d 行目: コマンドの中の操作に当たらない(前から効いていない): %s = %s\n", e.n, e.k, e.v)
			continue
		}
		for _, d := range ops {
			fmt.Fprintf(&R, "%s %s %s = %s   # approvals %d 行目: %s = %s\n", perms.SubjectOf(d), d, e.t, v, e.n, e.k, e.v)
		}
		if e.v != "ask" {
			continue
		}
		for j := 0; j < i; j++ { // 上の allow の行が、下の ask の行に締められる
			if es[j].v == "allow" && piOverlap(e.op, es[j].op) && piOverlap(e.t, es[j].t) {
				fmt.Fprintf(&W, "approvals %d 行目 %s = allow は、%d 行目 %s = ask に当たる相手では ask-any になる(厳しい方が勝つ)\n", es[j].n, es[j].k, e.n, e.k)
			}
		}
	}
	for n, line := range splitBody(gb) {
		n++
		l := config.Trim(cutHash(line))
		if l == "" {
			continue
		}
		f := strings.Fields(l)
		if len(f) != 3 {
			fmt.Fprintf(&S, "grants %d 行目: <主体> <操作> <対象> の形ではない: %s\n", n, l)
			continue
		}
		s, o, t := f[0], f[1], f[2]
		fmt.Fprintf(&R, "%s %s %s = allow   # grants %d 行目\n", s, o, t, n)
		for _, e := range es { // grants は approvals より先に見ていた。締める行(ask-any)に当たる相手では許可にならない
			if e.emit && e.v == "ask" && piOverlap(o, e.op) && piOverlap(t, e.t) {
				fmt.Fprintf(&W, "grants %d 行目 %s %s %s は、approvals %d 行目 %s = ask に当たる相手では許可にならない(厳しい方が勝つ)\n", n, s, o, t, e.n, e.k)
			}
		}
	}
	return R.String(), S.String(), W.String()
}

// splitBody は bash の while read と同じく行に分ける(末尾の改行は行を増やさない)。
func splitBody(b string) []string {
	if b == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(b, "\n"), "\n")
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(strings.TrimSuffix(s, "\n"), "\n", "\n  ") + "\n"
}

func cmdPolicyImport(args []string, w, stderr io.Writer) error {
	apply := false
	for _, a := range args {
		if a != "--apply" {
			return usage("policy import [--apply]")
		}
		apply = true
	}
	if err := requireHuman("tb policy import"); err != nil {
		return err
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	var ab, gb string
	type src struct{ k, s string }
	var srcs []src
	for _, k := range []string{"approvals", "grants"} {
		body, s, ok, readErr := config.LookupStrict(db.DB, db.Session, k)
		if readErr != "" {
			return die("設定 %s を読めません(%s)", k, readErr)
		}
		if !ok {
			continue
		}
		if k == "approvals" {
			ab = body
		} else {
			gb = body
		}
		srcs = append(srcs, src{k, s})
	}
	if len(srcs) == 0 {
		fmt.Fprintln(w, "古い権限の設定(approvals / grants)はありません。変換するものはありません")
		return nil
	}
	srcList := make([]string, len(srcs))
	for i, s := range srcs {
		srcList[i] = s.k + "=" + s.s
	}
	cdir := config.Dir()
	inCheckout := func(s string) bool { return cdir != "" && strings.HasPrefix(s, "file:"+cdir+"/") }
	// 書く先: 古い設定が全セッション共通(DB の共通の行・checkout のファイル)なら policy も共通の行
	shared := -1
	for _, s := range srcs {
		sc := -1
		switch {
		case s.s == "db:*", inCheckout(s.s):
			sc = 1
		case strings.HasPrefix(s.s, "db:"):
			sc = 0
		}
		if sc < 0 {
			continue // 環境変数のファイルは、書く先を決めない
		}
		if shared >= 0 && shared != sc {
			return die("approvals と grants の置き場所が違います(%s)。tb config で同じ置き場所にそろえてから、もう一度実行してください", strings.Join(srcList, " "))
		}
		shared = sc
	}
	if shared < 0 {
		shared = 0
	}
	rows, skip, warn := policyConvert(ab, gb)
	fmt.Fprintln(w, "変換のもと: "+strings.Join(srcList, " "))
	if rows != "" {
		fmt.Fprint(w, "policy に足す行:\n"+indent(rows))
	} else {
		fmt.Fprintln(w, "policy に足す行: なし")
	}
	if skip != "" {
		fmt.Fprint(w, "変換しない行:\n"+indent(skip))
	}
	if warn != "" {
		fmt.Fprint(w, "確かめてほしい行(厳しい方が勝つため、前より厳しくなりうる):\n"+indent(warn))
	}
	if !apply {
		fmt.Fprintln(w, "(まだ何も書いていません。--apply で policy に足し、古い設定を消します)")
		return nil
	}
	// policy の今の本文(書く先と同じ置き場所のもの)の末尾に足す
	cur, psrc, _, readErr := config.LookupStrict(db.DB, db.Session, "policy")
	if readErr != "" {
		return die("設定 policy を読めないため書きません(%s)", readErr)
	}
	switch {
	case psrc == "none":
		cur = ""
	case strings.HasPrefix(psrc, "file:"):
	case psrc == "db:*":
		if shared == 0 {
			return die("設定 policy は全セッション共通の行にあり、古い設定はこのセッションの行にあります。置き場所をそろえてから実行してください")
		}
	case strings.HasPrefix(psrc, "db:"):
		if shared == 1 {
			return die("設定 policy はこのセッションの行にあり、古い設定は全セッション共通です。置き場所をそろえてから実行してください")
		}
	default:
		cur = ""
	}
	body := cur
	if rows != "" {
		if body != "" {
			body += "\n"
		}
		body += "# " + nowTS() + " に tb policy import が approvals / grants から変換した行\n" + strings.TrimSuffix(rows, "\n")
	}
	if body != cur {
		if bad := perms.ValidatePolicy(body); bad != "" {
			fmt.Fprint(stderr, bad)
			return die("policy の本文に問題があるため書きませんでした")
		}
		if err := requirePolicyWrite(cur, body, "tb policy import"); err != nil {
			return err
		}
		info, _ := config.Info("policy")
		if f := os.Getenv(info.Env); f != "" {
			if err := writeConfigFile(f, body); err != nil {
				return err
			}
		} else if err := config.DBPut(db.DB, db.Session, "policy", body, "import", shared == 1, ""); err != nil {
			return err
		}
		fmt.Fprintln(w, "policy に足しました")
	}
	// 古い設定を消す(DB の行は消す。checkout のファイルは退かす。環境変数のファイルは消さない)
	dest := ""
	for _, s := range srcs {
		switch {
		case s.s == "db:*":
			if err := config.Unset(db.DB, db.Session, s.k, true); err != nil {
				return err
			}
			fmt.Fprintf(w, "%s: 全セッション共通の行を消しました\n", s.k)
		case strings.HasPrefix(s.s, "db:"):
			if err := config.Unset(db.DB, db.Session, s.k, false); err != nil {
				return err
			}
			fmt.Fprintf(w, "%s: このセッションの行を消しました\n", s.k)
		case inCheckout(s.s):
			f := strings.TrimPrefix(s.s, "file:")
			if dest == "" {
				dest = filepath.Join(env.StateDir(), "config-imported", time.Now().Format("20060102-150405"))
				if err := os.MkdirAll(dest, 0o755); err != nil {
					return die("退かす先を作れません: %s", dest)
				}
			}
			if err := moveFile(f, dest); err != nil {
				return die("%s を退かせません(policy には足し済み)", f)
			}
			fmt.Fprintf(w, "%s: %s を %s/ へ退かしました\n", s.k, f, dest)
		default:
			fmt.Fprintf(w, "%s: %s は環境変数で指定したファイルなので消しません。環境変数を外してください\n", s.k, strings.TrimPrefix(s.s, "file:"))
		}
	}
	return nil
}
