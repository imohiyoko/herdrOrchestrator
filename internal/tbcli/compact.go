package tbcli

import (
	"bufio"
	"bytes"
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

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/transport"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
)

// tb compact-check(taskboard/lib/compact.sh): 文脈が大きくなった待機中の agent に /compact を送る。LLM は使わない。

var (
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

// kindSupports は kind_supports <kind> <項目>。
func kindSupports(kind, item string) bool {
	switch {
	case kind == "claude":
		return true
	case (kind == "codex" || kind == "gemini") && item == "launch":
		return true
	}
	return false
}

// recordOp は record_op(新しい行): agent_ops に結果を 1 行足す。
func recordOp(db *store.DB, op, target, args, by string, rc int, result string) {
	agents.RecordOp(db.DB, db.Session, op, target, args, by, rc, result)
}

// recordOpID は record_op の既存の行(承認待ちの L-<id>)を結果で閉じる。
func recordOpID(db *store.DB, id int64, rc int, result string) {
	st := "done"
	if rc != 0 {
		st = "failed"
	}
	_, _ = db.Exec(`UPDATE main.agent_ops SET status=?, result=?, decided_at=? WHERE herdr_session=? AND id=?`, st, result, nowTS(), db.Session, id)
}

func cmdCompactCheck(args []string, w, stderr io.Writer) error {
	apply := false
	for _, a := range args {
		switch a {
		case "--dry-run":
			apply = false
		case "--apply":
			apply = true
		default:
			return usage("compact-check: 不明な引数 %s", a)
		}
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	body, _, has := config.Lookup(db.DB, db.Session, "compact")
	cfg := func(k, def string) string { return compactCfg(body, has, k, def) }
	if cfg("enabled", "0") != "1" {
		fmt.Fprintln(w, "compact-check: disabled (config/compact.conf の enabled = 1 で有効)")
		return nil
	}
	thrS, minS, ex := cfg("threshold_tokens", "150000"), cfg("min_interval_sec", "1800"), cfg("exclude", "")
	if !reDigits.MatchString(thrS) || !reDigits.MatchString(minS) {
		return die("compact.conf の threshold_tokens / min_interval_sec は 0 以上の整数")
	}
	thr, _ := strconv.ParseInt(thrS, 10, 64)
	minint, _ := strconv.ParseInt(minS, 10, 64)
	live, err := agents.SyncLive(db.DB, db.Session)
	if err != nil {
		fmt.Fprintln(stderr, "tb: herdr agent list に失敗しました")
		return &exit{exitErr, ""}
	}
	self := ""
	if pane := os.Getenv("HERDR_PANE_ID"); pane != "" {
		if self, err = paneAgent(pane); err != nil {
			return die("herdr agent list に失敗したため、実行者を特定できません")
		}
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
			fmt.Fprintf(w, "%s - skip(excluded)\n", name)
			continue
		}
		if st != "idle" && st != "done" {
			if st == "" {
				st = "unknown"
			}
			fmt.Fprintf(w, "%s - skip(%s)\n", name, st)
			continue
		}
		var ck string
		_ = db.QueryRow(`SELECT COALESCE(kind,'claude') FROM agents WHERE name=?`, name).Scan(&ck)
		k := ck
		if k == "" {
			k = "claude"
		}
		if !kindSupports(k, "compact") {
			fmt.Fprintf(w, "%s - skip(kind=%s: compact 非対応)\n", name, ck)
			continue
		}
		if sid == "" {
			_ = db.QueryRow(`SELECT COALESCE(session_id,'') FROM agents WHERE name=?`, name).Scan(&sid)
		}
		tok := compactTokens(sid)
		if !reDigits.MatchString(tok) {
			fmt.Fprintf(w, "%s - skip(no transcript usage)\n", name)
			continue
		}
		if t, _ := strconv.ParseInt(tok, 10, 64); t <= thr {
			fmt.Fprintf(w, "%s %s skip(below threshold %d)\n", name, tok, thr)
			continue
		}
		last := int64(-1)
		_ = db.QueryRow(`SELECT COALESCE(CAST(strftime('%s','now') - strftime('%s', MAX(decided_at)) AS INTEGER), -1) FROM agent_ops WHERE op='compact' AND status='done' AND target=?`, name).Scan(&last)
		if last >= 0 && last < minint {
			fmt.Fprintf(w, "%s %s skip(compacted %ds ago, min_interval %ds)\n", name, tok, last, minint)
			continue
		}
		if tier, _ := j.Authorize("agent:compact:"+name, ""); tier != "allow" {
			fmt.Fprintf(w, "%s %s skip(approval: agent:compact:%s is %s; add '* agent:compact * = allow' to policy)\n", name, tok, name, tier)
			continue
		}
		if !apply {
			fmt.Fprintf(w, "%s %s would-compact(dry-run)\n", name, tok)
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
			by := self
			if by == "" {
				by = "human"
			}
			recordOp(db, "compact", name, `{"tokens":`+tok+`}`, by, 0, "compact sent at "+tok+" tokens"+line)
		} else {
			act = "skip(busy or send failed)"
		}
		fmt.Fprintf(w, "%s %s %s%s\n", name, tok, act, line)
	}
	return nil
}
