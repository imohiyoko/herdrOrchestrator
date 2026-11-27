package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/transport"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
)

// 文脈が大きくなった待機中の agent に /compact を送る(tb compact-check。taskboard/lib/compact.sh)。LLM は使わない。

// CompactReq は agent.compact.check の入力。Apply が false なら送らずに何をするかだけを返す。
type CompactReq struct {
	Apply bool `api:"apply,optional"`
}

// CompactResult は agent ごとの 1 行(tb の出力のまま)。
type CompactResult struct {
	Lines   []string     `api:"lines" json:"lines"`
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter。
func (r *CompactResult) SetPending(p *app.Pending) { r.Pending = p }

func (r *CompactResult) add(s string)                 { r.Lines = append(r.Lines, s) }
func (r *CompactResult) addf(format string, a ...any) { r.add(fmt.Sprintf(format, a...)) }

func compactOp(env *app.Env) app.Op {
	return app.Command(env, "agent.compact.check", app.Entry("cmd:agent:compact-check", "tb compact-check"),
		func(ctx context.Context, in CompactReq) (CompactResult, error) {
			who := by(ctx)
			if who == "" {
				return CompactResult{}, define.Errorf(define.CodePermissionDenied, "呼び手を決められません")
			}
			return withDB(env, func(db *store.DB) (CompactResult, error) { return compactCheck(env, db, who, in.Apply) })
		},
		define.Doc("文脈が大きくなった待機中の agent に /compact を送る(tb compact-check。policy の agent:compact:<名前> が allow のものだけ)"),
		define.Authz("cmd:agent:compact-check"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/agents/compact-check"),
	)
}

var (
	reDigits     = regexp.MustCompile(`^[0-9]+$`)
	reCompactSID = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// compactCfg は compact_cfg: TB_COMPACT_<キー大文字> > 設定 compact > 既定。
func compactCfg(body string, has bool, key, def string) string {
	if v := os.Getenv("TB_COMPACT_" + strings.ToUpper(key)); v != "" {
		return v
	}
	if has {
		for _, line := range config.Lines(body) {
			line = config.Trim(cutHash(line))
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			if config.Trim(k) == key {
				return config.Trim(v)
			}
		}
	}
	return def
}

// usageSum は compact_usage_sum: transcript の 1 行の usage の合計(読めなければ空)。
func usageSum(line string) string {
	if line == "" {
		return ""
	}
	var v any
	d := json.NewDecoder(strings.NewReader(line))
	d.UseNumber()
	if d.Decode(&v) != nil {
		return ""
	}
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	var u any
	if msg, ok := m["message"]; ok && msg != nil {
		mm, ok := msg.(map[string]any)
		if !ok {
			return ""
		}
		u = mm["usage"]
	}
	if u == nil || u == false {
		u = m["usage"]
	}
	um, ok := u.(map[string]any)
	if !ok {
		return ""
	}
	sum := 0.0
	for _, k := range []string{"input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"} {
		x := um[k]
		if x == nil || x == false {
			continue
		}
		n, ok := x.(json.Number)
		if !ok {
			return ""
		}
		f, err := n.Float64()
		if err != nil {
			return ""
		}
		sum += f
	}
	if sum != math.Trunc(sum) || sum < 0 || sum > 1e17 {
		return strconv.FormatFloat(sum, 'g', -1, 64) // 整数でなければ数の判定で落ちる
	}
	return strconv.FormatInt(int64(sum), 10)
}

// lastUsageLine は grep -a '"usage"' | tail -n 1(skipFirst なら 1 行目を捨てる。sed 1d)。
func lastUsageLine(b []byte, skipFirst bool) string {
	last := ""
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64*1024), 1<<30)
	first := true
	for sc.Scan() {
		if first && skipFirst {
			first = false
			continue
		}
		first = false
		if bytes.Contains(sc.Bytes(), []byte(`"usage"`)) {
			last = sc.Text()
		}
	}
	return last
}

// compactTokens は compact_tokens: session id の transcript の、最後の usage の合計(見つからなければ空)。
func compactTokens(sid string) string {
	if !reCompactSID.MatchString(sid) {
		return ""
	}
	root := os.Getenv("TB_CLAUDE_PROJECTS")
	if root == "" {
		root = filepath.Join(os.Getenv("HOME"), ".claude", "projects")
	}
	es, _ := os.ReadDir(root)
	var dirs []string
	for _, e := range es {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if st, err := os.Stat(filepath.Join(root, e.Name())); err == nil && st.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs)
	f := ""
	for _, d := range dirs {
		p := filepath.Join(root, d, sid+".jsonl")
		if isFile(p) {
			f = p
			break
		}
	}
	if f == "" {
		return ""
	}
	// 末尾だけを読む(transcript は数十 MB になりうる)。末尾の 1 行目は行の途中からでありうるので捨てる
	tail := int64(4194304)
	if v, err := strconv.ParseInt(os.Getenv("COMPACT_TAIL_BYTES"), 10, 64); err == nil {
		tail = v
	}
	var tok string
	if fh, err := os.Open(f); err == nil {
		st, _ := fh.Stat()
		off := int64(0)
		if st != nil && st.Size() > tail {
			off = st.Size() - tail
		}
		if _, err := fh.Seek(off, io.SeekStart); err == nil {
			b, _ := io.ReadAll(fh)
			tok = usageSum(lastUsageLine(b, true))
		}
		fh.Close()
	}
	if !reDigits.MatchString(tok) {
		b, _ := os.ReadFile(f)
		tok = usageSum(lastUsageLine(b, false))
	}
	return tok
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func compactCheck(env *app.Env, db *store.DB, who string, apply bool) (CompactResult, error) {
	var out CompactResult
	w := &out
	body, _, has := config.Lookup(db.DB, db.Session, "compact")
	cfg := func(k, def string) string { return compactCfg(body, has, k, def) }
	if cfg("enabled", "0") != "1" {
		w.add("compact-check: disabled (config/compact.conf の enabled = 1 で有効)")
		return out, nil
	}
	thrS, minS, ex := cfg("threshold_tokens", "150000"), cfg("min_interval_sec", "1800"), cfg("exclude", "")
	if !reDigits.MatchString(thrS) || !reDigits.MatchString(minS) {
		return out, define.Errorf(define.CodeFailedPrecondition, "compact.conf の threshold_tokens / min_interval_sec は 0 以上の整数")
	}
	thr, _ := strconv.ParseInt(thrS, 10, 64)
	minint, _ := strconv.ParseInt(minS, 10, 64)
	live, err := agents.SyncLive(db.DB, db.Session)
	if err != nil {
		return out, define.Errorf(define.CodeUnavailable, "herdr agent list に失敗しました")
	}
	self := ""
	if who != "human" && !strings.HasPrefix(who, "system:") {
		self = who
	}
	conductors := " " + strings.Join(message.ConductorNames(db.DB, db.Session), " ") + " "
	excl := " " + ex + " "
	j := approval.NewJudge(db.DB, db.Session)
	for _, a := range live {
		name := a.Name
		if name == "" {
			continue
		}
		st, _ := a.AgentStatus.(string)
		sid := a.AgentSession.Value
		if strings.Contains(conductors, " "+name+" ") || strings.Contains(excl, " "+name+" ") || name == self {
			w.addf("%s - skip(excluded)", name)
			continue
		}
		if st != "idle" && st != "done" {
			if st == "" {
				st = "unknown"
			}
			w.addf("%s - skip(%s)", name, st)
			continue
		}
		var ck string
		_ = db.QueryRow(`SELECT COALESCE(kind,'claude') FROM agents WHERE name=?`, name).Scan(&ck)
		k := ck
		if k == "" {
			k = "claude"
		}
		if !KindSupports(k, "compact") {
			w.addf("%s - skip(kind=%s: compact 非対応)", name, ck)
			continue
		}
		if sid == "" {
			_ = db.QueryRow(`SELECT COALESCE(session_id,'') FROM agents WHERE name=?`, name).Scan(&sid)
		}
		tok := compactTokens(sid)
		if !reDigits.MatchString(tok) {
			w.addf("%s - skip(no transcript usage)", name)
			continue
		}
		if t, _ := strconv.ParseInt(tok, 10, 64); t <= thr {
			w.addf("%s %s skip(below threshold %d)", name, tok, thr)
			continue
		}
		last := int64(-1)
		_ = db.QueryRow(`SELECT COALESCE(CAST(strftime('%s','now') - strftime('%s', MAX(decided_at)) AS INTEGER), -1) FROM agent_ops WHERE op='compact' AND status='done' AND target=?`, name).Scan(&last)
		if last >= 0 && last < minint {
			w.addf("%s %s skip(compacted %ds ago, min_interval %ds)", name, tok, last, minint)
			continue
		}
		if tier, _ := j.Authorize("agent:compact:"+name, ""); tier != "allow" {
			w.addf("%s %s skip(approval: agent:compact:%s is %s; add '* agent:compact * = allow' to policy)", name, tok, name, tier)
			continue
		}
		if !apply {
			w.addf("%s %s would-compact(dry-run)", name, tok)
			continue
		}
		// 送る直前に状態を見直す(tb slash と同じ送信の経路: idle/done でなければ送らず、送ったら動き出したかを確かめる)
		act, line := "sent", ""
		r := transport.Send(db.DB, db.Session, name, "/compact", "compact:"+name, "compact", io.Discard)
		if r == transport.Sent && !transport.VerifyPrompt(name) {
			r = 1
		}
		if r <= 1 {
			if r != 0 {
				line = " (処理の開始を確認できない。pane を見てください)"
			}
			by := who // system:* はそのまま記録する(人の操作と取り違えない)
			if by == "" {
				by = "human"
			}
			agents.RecordOp(db.DB, db.Session, "compact", name, `{"tokens":`+tok+`}`, by, 0, "compact sent at "+tok+" tokens"+line)
		} else {
			act = "skip(busy or send failed)"
		}
		w.addf("%s %s %s%s", name, tok, act, line)
	}
	return out, nil
}
