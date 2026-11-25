package identity

// 呼び出し元の確かめ(ADR 0007 段 1a。taskboard/lib/identity.sh の authenticate と同じ)。
// HERDR_PANE_ID は自己申告なので、そのまま信じない。このプロセスが本当にその pane の中で動いているかを確かめる:
// herdr の pane process-info が返す pane の最初のプロセス(shell_pid)が祖先にいるか、同じ端末(tty)を使っているか。
// HERDR_PANE_ID が無くても herdr の中なら、agent の pane を探して HERDR_PANE_ID を付け直す(消して人のふりをする、を止める)。
// 限界: 同じ OS ユーザーの agent が、わざと親子関係と端末を切ったり、tb を書き換えたりして実行するのは止められない(ADR 0007)。

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
)

var errBadList = errors.New("agent list")

// System は system:<名前> の主体(ADR 0007。bash の AUTH_SYSTEM)。herdr の外から、起動の仕組み(scheduler の tick.sh)が
// TB_SYSTEM=<名前> を付けて呼んだときだけ入る。印は自己申告だが、herdr の外の呼び出しは元から人なので、名乗っても権限は増えない
// (policy の system: の行で締まるだけ)。herdr の中では使わない。
var System string

var reSystem = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

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
	out, err := herdr.Call("pane", "process-info", "--pane", pane)
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
// Windows には ps が無いので確かめない(段 1a より前と同じく HERDR_PANE_ID を信じる。ADR 0007 の許容すること)。
func Authenticate() (state, errMsg string) {
	System = ""
	if runtime.GOOS == "windows" {
		return "unchecked", ""
	}
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
		if n := os.Getenv("TB_SYSTEM"); n != "" {
			if !reSystem.MatchString(n) {
				return "", "TB_SYSTEM は英小文字・数字・- の名前です: " + n
			}
			System = "system:" + n
		}
		return "outside", ""
	}
	out, err := herdr.Call("agent", "list")
	var shape struct {
		Result struct {
			Agents []json.RawMessage `json:"agents"`
		} `json:"result"`
	}
	if err == nil && (json.Unmarshal(out, &shape) != nil || shape.Result.Agents == nil) { // 読めない一覧を「agent はいない」と取らない
		err = errBadList
	}
	var as []herdr.Agent
	if err == nil {
		as, err = herdr.ParseAgents(out)
	}
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

// Descendants は pid の子孫のプロセス(pid と comm)。ps を読めなければ ok=false(Windows も false。確かめられない)。
// tb herdr の close が、pane のシェルの下で動いているもの(背景のジョブなど)を見つけるのに使う。
func Descendants(pid int) (out []string, ok bool) {
	if runtime.GOOS == "windows" {
		return nil, false
	}
	p, ok := snapshot()
	if !ok {
		return nil, false
	}
	kids := map[int][]int{}
	for c, pp := range p.ppid {
		kids[pp] = append(kids[pp], c)
	}
	seen := map[int]bool{pid: true}
	q := []int{pid}
	for len(q) > 0 {
		x := q[0]
		q = q[1:]
		for _, c := range kids[x] {
			if !seen[c] {
				seen[c] = true
				q = append(q, c)
				out = append(out, strconv.Itoa(c)+" "+p.comm[c])
			}
		}
	}
	return out, true
}
