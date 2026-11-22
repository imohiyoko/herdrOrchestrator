// Package recipe は部品とレシピ(ADR 0005。taskboard/lib/recipes.sh)。
package recipe

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
)

// RiskLevels はリスク階層(低い順)。
var RiskLevels = []string{"low", "mid", "high", "human-only"}

var (
	sides   = " none local-write external-send external-write "
	levels  = " low mid high human-only "
	runners = " inside outside "
)

// Part は部品の表の 1 行。Ops と Args は表のまま(空白区切り)。
type Part struct {
	Name, Side, Risk, Runner, Ops, Args, Desc string
}

// HumanOnly は人だけの部品か(リスク階層が human-only)。
func (p Part) HumanOnly() bool { return p.Risk == "human-only" }

// Table は部品の表(recipes.sh の PARTS_TABLE と同じ。表は TestTableMatchesBash が bash と比べる)。
const Table = `agent-status     | none           | low        | inside  | -                                       | target:agent                   | agent の状態(idle / working など)を読む
wait-idle        | none           | low        | inside  | -                                       | target:agent timeout:int:1-600 | agent が idle か done になるまで待つ(秒の上限つき。過ぎたら失敗)
task-show        | none           | low        | inside  | -                                       | task:task                      | チケットを読む(tb show と同じ出力)
send-info        | local-write    | mid        | inside  | send:info:$to                           | to:agent text:text:2000        | 情報共有を送る(tb send --kind info と同じ判定)
agent-stop       | local-write    | mid        | outside | agent:stop:$target                      | target:agent                   | agent を止める(tb agent-stop と同じ判定)
agent-revive     | local-write    | mid        | outside | agent:revive:$target                    | target:agent prompt?:text:4000 | 止めた agent を同じ会話から戻す(tb agent-revive と同じ判定)
agent-restart    | local-write    | mid        | outside | agent:stop:$target agent:revive:$target | target:agent prompt?:text:4000 | 止めて同じ会話で戻す。stop と revive の両方の判定が要る。自分自身の再起動は pane の外の実行役が行う
agent-force-stop | local-write    | human-only | outside | -                                       | target:agent                   | agent を強制で止める(tb agent-stop --force)
comment-resolve  | local-write    | human-only | inside  | -                                       | comment:comment                | 報告(question / blocked)を対応済みにする(tb resolve)
pr-queue-approve | external-write | human-only | outside | -                                       | id:prq                         | PR キューの依頼を承認する(tb pr-queue approve)
config-set       | local-write    | human-only | inside  | -                                       | key:config-key                 | 設定を書き換える(tb config set)`

var (
	reName    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	reArgName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,15}\??$`)
	reInt     = regexp.MustCompile(`^([0-9]{1,6})-([0-9]{1,6})$`)
	reEnum    = regexp.MustCompile(`^[a-z0-9_-]{1,32}(/[a-z0-9_-]{1,32}){0,15}$`)
	reText    = regexp.MustCompile(`^[0-9]{1,5}$`)
	reDigits6 = regexp.MustCompile(`^[0-9]{1,6}$`)
	reTask    = regexp.MustCompile(`^T-[A-Za-z0-9_]+-[0-9]{3,}$`)
	reComment = regexp.MustCompile(`^C-[0-9]{1,18}$`)
	rePRQ     = regexp.MustCompile(`^PQ-[0-9]{1,18}$`)
	reCfgKey  = regexp.MustCompile(`^[a-z][a-z-]{0,31}$`)
	reCtrl    = regexp.MustCompile("[\x01-\x08\x0b-\x1f\x7f]")
	reOp      = regexp.MustCompile(`^([a-z]+:[a-z]+):\$([a-z][a-z0-9_]*)$`)
	reOpOut   = regexp.MustCompile(`^[A-Za-z0-9._:@-]{1,128}$`)
)

func trim(s string) string { return config.Trim(s) }

// Parse は表を読む(parts_load)。表の誤りはコードの誤りなのでエラーを返す。
func Parse(table string) ([]Part, error) {
	var out []Part
	seen := map[string]bool{}
	for _, line := range strings.Split(table, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var f [7]string
		copy(f[:], strings.SplitN(line, "|", 7))
		for i := range f {
			f[i] = trim(f[i])
		}
		p := Part{f[0], f[1], f[2], f[3], f[4], f[5], f[6]}
		if !reName.MatchString(p.Name) || seen[p.Name] {
			return nil, fmt.Errorf("部品の表の誤り(名前): %s", p.Name)
		}
		if !strings.Contains(sides, " "+p.Side+" ") || !strings.Contains(levels, " "+p.Risk+" ") || !strings.Contains(runners, " "+p.Runner+" ") || p.Desc == "" {
			return nil, fmt.Errorf("部品の表の誤り(副作用・リスク階層・実行役・説明): %s", p.Name)
		}
		for _, spec := range strings.Fields(p.Args) {
			nm, t, ok := strings.Cut(spec, ":")
			if !ok || !reArgName.MatchString(nm) || !TypeOK(t) {
				return nil, fmt.Errorf("部品の表の誤り(引数): %s %s", p.Name, spec)
			}
		}
		if p.Ops == "-" {
			p.Ops = ""
		}
		seen[p.Name] = true
		out = append(out, p)
	}
	return out, nil
}

// Parts は部品の一覧(表の順)。
func Parts() []Part {
	ps, err := Parse(Table)
	if err != nil {
		panic(err) // 表は固定。テストが確かめる
	}
	return ps
}

// Lookup は名前の部品。
func Lookup(name string) (Part, bool) {
	for _, p := range Parts() {
		if name != "" && p.Name == name {
			return p, true
		}
	}
	return Part{}, false
}

func atoi10(s string) int { n, _ := strconv.Atoi(s); return n }

// TypeOK は part_type_ok: 型の書き方が正しいか。
func TypeOK(t string) bool {
	switch {
	case t == "agent" || t == "task" || t == "comment" || t == "prq" || t == "config-key":
		return true
	case strings.HasPrefix(t, "int:"):
		m := reInt.FindStringSubmatch(t[4:])
		return m != nil && atoi10(m[1]) <= atoi10(m[2])
	case strings.HasPrefix(t, "enum:"):
		return reEnum.MatchString(t[5:])
	case strings.HasPrefix(t, "text:"):
		n := t[5:]
		return reText.MatchString(n) && atoi10(n) >= 1 && atoi10(n) <= 10000
	}
	return false
}

func isConfigKey(v string) bool {
	for _, k := range config.Keys {
		if k == v {
			return true
		}
	}
	return false
}

// charLen は char_len(UTF-8 の続きのバイトを数えない)。
func charLen(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < 0x80 || s[i] > 0xbf {
			n++
		}
	}
	return n
}

// ValueOK は part_value_ok: 値が型に合うか。合わなければ理由。
func ValueOK(t, v string) string {
	switch {
	case t == "agent":
		if v == "human" || !agents.NameRE.MatchString(v) {
			return "agent 名ではありません"
		}
	case t == "task":
		if !reTask.MatchString(v) {
			return "タスク ID(T-<project>-NNN)ではありません"
		}
	case t == "comment":
		if !reComment.MatchString(v) {
			return "報告の ID(C-<数字>)ではありません"
		}
	case t == "prq":
		if !rePRQ.MatchString(v) {
			return "PR キューの ID(PQ-<数字>)ではありません"
		}
	case t == "config-key":
		if !reCfgKey.MatchString(v) || !isConfigKey(v) {
			return "設定のキーではありません"
		}
	case strings.HasPrefix(t, "int:"):
		r := t[4:]
		lo, hi := r[:strings.LastIndex(r, "-")], r[strings.Index(r, "-")+1:]
		if !reDigits6.MatchString(v) || atoi10(v) < atoi10(lo) || atoi10(v) > atoi10(hi) {
			return lo + "〜" + hi + " の整数ではありません"
		}
	case strings.HasPrefix(t, "enum:"):
		if v == "" || strings.Contains(v, "/") || !strings.Contains("/"+t[5:]+"/", "/"+v+"/") {
			return t[5:] + " のどれかではありません"
		}
	case strings.HasPrefix(t, "text:"):
		if charLen(v) > atoi10(t[5:]) {
			return t[5:] + " 字を超えています"
		}
		if reCtrl.MatchString(v) {
			return "制御文字(改行とタブのほか)を含みます"
		}
	default:
		return "不明な型 " + t
	}
	return ""
}

// CheckArgs は part_args_check: <名前>=<値> の並びを確かめ、名前 → 値を返す。だめなら理由。
func CheckArgs(p Part, kvs []string) (map[string]string, string) {
	val := map[string]string{}
	seen := map[string]bool{}
	specs := strings.Fields(p.Args)
	for _, kv := range kvs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, "引数は <名前>=<値>: " + kv
		}
		if seen[k] {
			return nil, "引数 " + k + " が 2 回あります"
		}
		seen[k] = true
		t := ""
		for _, spec := range specs {
			nm, ty, _ := strings.Cut(spec, ":")
			if strings.TrimSuffix(nm, "?") == k {
				t = ty
			}
		}
		if t == "" {
			can := p.Args
			if can == "" {
				can = "なし"
			}
			return nil, "部品 " + p.Name + " に引数 " + k + " はありません(使えるのは: " + can + ")"
		}
		if why := ValueOK(t, v); why != "" {
			return nil, p.Name + " の引数 " + k + ": " + why
		}
		val[k] = v
	}
	for _, spec := range specs {
		nm, _, _ := strings.Cut(spec, ":")
		if strings.HasSuffix(nm, "?") {
			continue
		}
		if !seen[nm] {
			return nil, "部品 " + p.Name + " の引数 " + nm + " がありません"
		}
	}
	return val, ""
}

// Ops は part_ops: 判定の操作名($<名前> を値に置き換える)。
func Ops(p Part, val map[string]string) ([]string, error) {
	var out []string
	for _, op := range strings.Fields(p.Ops) {
		m := reOp.FindStringSubmatch(op)
		if m == nil {
			return nil, fmt.Errorf("部品の表の誤り(%s の操作名は <種別>:<操作>:$<引数>: %s)", p.Name, op)
		}
		v := val[m[2]]
		if v == "" {
			return nil, fmt.Errorf("部品の表の誤り(%s の操作名 %s の $%s に値がありません)", p.Name, op, m[2])
		}
		o := m[1] + ":" + v
		if !reOpOut.MatchString(o) {
			return nil, fmt.Errorf("部品の操作名に使えない文字があります: %s", o)
		}
		out = append(out, o)
	}
	return out, nil
}

// RiskRank は part_risk_rank(知らない値は human-only)。
func RiskRank(r string) int {
	switch r {
	case "low":
		return 0
	case "mid":
		return 1
	case "high":
		return 2
	}
	return 3
}

// RiskMax は part_risk_max: 部品の並びのリスク階層(最大。一覧にない部品は human-only)。
func RiskMax(names []string) string {
	r := 0
	for _, n := range names {
		x := 3
		if p, ok := Lookup(n); ok {
			x = RiskRank(p.Risk)
		}
		if x > r {
			r = x
		}
	}
	return RiskLevels[r]
}
