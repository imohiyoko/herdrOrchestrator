package identity

// 呼び出し元の確かめ(ADR 0007 段 1a。taskboard/lib/identity.sh の authenticate と同じ)。
// HERDR_PANE_ID は自己申告なので、そのまま信じない。このプロセスが本当にその pane の中で動いているかを確かめる:
// herdr の pane process-info が返す pane の最初のプロセス(shell_pid)が祖先にいるか、同じ端末(tty)を使っているか。
// HERDR_PANE_ID が無くても herdr の中なら、agent の pane を探して HERDR_PANE_ID を付け直す(消して人のふりをする、を止める)。
// 限界: 同じ OS ユーザーの agent が、わざと親子関係と端末を切ったり、tb を書き換えたりして実行するのは止められない(ADR 0007)。

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
)

type procs struct {
	ppid map[int]int
	tty  map[int]string
	comm map[int]string
}

func snapshot() (*procs, bool) {
	ps := "/bin/ps" // PATH の差し替えで偽の ps を使わせない
	if _, err := os.Stat(ps); err != nil {
		ps = "/usr/bin/ps"
	}
	out, err := exec.Command(ps, "-A", "-o", "pid=,ppid=,tty=,comm=").Output()
	if err != nil {
		return nil, false
	}
	p := &procs{map[int]int{}, map[int]string{}, map[int]string{}}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		pp, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		p.ppid[pid], p.tty[pid] = pp, f[2]
		p.comm[pid] = filepath.Base(strings.Join(f[3:], " "))
	}
	_, ok := p.ppid[os.Getpid()]
	return p, ok
}

func ttyOK(t string) bool { return t != "" && t != "?" && t != "??" && t != "-" }

func (p *procs) ancestors(me int) map[int]bool {
	a := map[int]bool{}
	for n, q := 0, me; q != 0 && n < 64; n++ {
		a[q] = true
		nq, ok := p.ppid[q]
		if !ok {
			break
		}
		q = nq
	}
	return a
}

func (p *procs) insideHerdr(me int, anc map[int]bool) bool {
	for q := range anc {
		if p.comm[q] == "herdr" {
			return true
		}
	}
	mt := p.tty[me]
	if !ttyOK(mt) {
		return false
	}
	for q, t := range p.tty {
		if t == mt && p.comm[p.ppid[q]] == "herdr" {
			return true
		}
	}
	return false
}

// inPane: 0=この pane の中 1=違う 2=herdr に聞けない
func (p *procs) inPane(pane string, me int, anc map[int]bool) int {
	out, err := herdr.Call("pane", "process-info", pane)
	if err != nil {
		return 2
	}
	var r struct {
		Result struct {
			ProcessInfo struct {
				ShellPID *int `json:"shell_pid"`
			} `json:"process_info"`
		} `json:"result"`
	}
	if json.Unmarshal(out, &r) != nil || r.Result.ProcessInfo.ShellPID == nil {
		return 2
	}
	sp := *r.Result.ProcessInfo.ShellPID
	if anc[sp] {
		return 0
	}
	if mt := p.tty[me]; ttyOK(mt) && p.tty[sp] == mt {
		return 0
	}
	return 1
}

// Authenticate は呼び出し元を確かめる。確かめられなければ理由を返す(呼ぶ側は何もせず終了コード 3)。
// herdr の中で HERDR_PANE_ID が無ければ、見つけた agent の pane を HERDR_PANE_ID に入れる。
func Authenticate() (state, errMsg string) {
	p, ok := snapshot()
	if !ok {
		return "", "プロセスの一覧(ps)を読めないため、呼び出し元を確かめられません"
	}
	me := os.Getpid()
	anc := p.ancestors(me)
	if claim := os.Getenv("HERDR_PANE_ID"); claim != "" {
		switch p.inPane(claim, me, anc) {
		case 0:
			return "pane", ""
		case 2:
			return "", "herdr に pane " + claim + " を問い合わせられないため、呼び出し元を確かめられません"
		default:
			return "", "HERDR_PANE_ID=" + claim + " の pane の中から実行されていません(呼び出し元を確かめられないため、何もしません)"
		}
	}
	if !p.insideHerdr(me, anc) {
		return "outside", ""
	}
	as, err := herdr.Agents()
	if err != nil {
		return "", "herdr agent list に失敗したため、呼び出し元を確かめられません"
	}
	for _, a := range as {
		if a.PaneID == "" {
			continue
		}
		switch p.inPane(a.PaneID, me, anc) {
		case 0:
			os.Setenv("HERDR_PANE_ID", a.PaneID)
			return "found", ""
		case 2:
			return "", "herdr に pane " + a.PaneID + " を問い合わせられないため、呼び出し元を確かめられません"
		}
	}
	return "outside", ""
}
