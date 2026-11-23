package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
)

// herdr.go は herdr CLI の呼び出し(読むだけ)と短いキャッシュ。

const cacheTTL = 3 * time.Second

type cacheEntry struct {
	at time.Time
	v  any
}

var (
	cacheMu sync.Mutex
	cacheM  = map[string]cacheEntry{}
)

// cached はキーをセッションごとに分けて、cacheTTL のあいだ覚えておく。
func cached[T any](sess, key string, fn func() T) T {
	k := sess + "\x00" + key
	cacheMu.Lock()
	if hit, ok := cacheM[k]; ok && time.Since(hit.at) < cacheTTL {
		cacheMu.Unlock()
		return hit.v.(T)
	}
	cacheMu.Unlock()
	v := fn()
	cacheMu.Lock()
	cacheM[k] = cacheEntry{time.Now(), v}
	if len(cacheM) > 200 {
		type kv struct {
			k  string
			at time.Time
		}
		var all []kv
		for k, e := range cacheM {
			all = append(all, kv{k, e.at})
		}
		sort.Slice(all, func(i, j int) bool { return all[i].at.Before(all[j].at) })
		for _, x := range all[:100] {
			delete(cacheM, x.k)
		}
	}
	cacheMu.Unlock()
	return v
}

// childEnv は reqctx.child_env: herdr / tb の子プロセスに渡す環境。プロセスのセッションと違うときは HERDR_SESSION で明示する。
func childEnv(sess string) []string {
	env := os.Environ()
	if sess == SessionName() {
		return env
	}
	fromFile := ""
	var out []string
	for _, kv := range env {
		k, val, _ := strings.Cut(kv, "=")
		switch k {
		case "HERDR_SOCKET_PATH", "HERDR_SESSION":
			continue
		case "HERDR_SESSION_FROM_FILE":
			fromFile = val
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "HERDR_SESSION="+sess)
	if fromFile == sess {
		out = append(out, "HERDR_SESSION_FROM_FILE="+fromFile)
	}
	return out
}

// runHerdr は (出力, エラー文)。herdr が無い・失敗・時間切れでも止まらない。
func runHerdr(sess string, args ...string) (string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, herdr.Bin(), args...) // 実体の場所は herdr アダプタ(kernel/herdr)の Bin と同じ
	c.Env = childEnv(sess)
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ctx.Err() == nil {
			msg := strings.TrimSpace(strings.ToValidUTF8(errb.String(), "�"))
			if r := []rune(msg); len(r) > 200 {
				msg = string(r[:200])
			}
			return "", fmt.Sprintf("herdr が失敗しました (exit %d): %s", ee.ExitCode(), msg)
		}
		name := "OSError"
		if ctx.Err() != nil {
			name = "TimeoutExpired"
		} else if _, ok := err.(*exec.Error); ok {
			name = "FileNotFoundError"
		}
		return "", "herdr を実行できません: " + name
	}
	return strings.ToValidUTF8(out.String(), "�"), ""
}

type agentList struct {
	agents []map[string]any
	err    string
}

// getAgents は herdr agent list の agents(dict だけ)。
func getAgents(sess string) ([]map[string]any, string) {
	r := cached(sess, "agents", func() agentList {
		out, err := runHerdr(sess, "agent", "list")
		if err != "" {
			return agentList{nil, err}
		}
		var d struct {
			Result struct {
				Agents json.RawMessage `json:"agents"`
			} `json:"result"`
		}
		bad := agentList{nil, "herdr agent list の出力を解釈できません"}
		dec := json.NewDecoder(strings.NewReader(out))
		dec.UseNumber()
		if dec.Decode(&d) != nil || d.Result.Agents == nil {
			return bad
		}
		var items []any
		dec = json.NewDecoder(bytes.NewReader(d.Result.Agents))
		dec.UseNumber()
		if dec.Decode(&items) != nil || items == nil {
			return bad
		}
		var as []map[string]any
		for _, it := range items {
			if m, ok := it.(map[string]any); ok {
				as = append(as, m)
			}
		}
		return agentList{as, ""}
	})
	return r.agents, r.err
}

// agentKey は name か pane_id。
func agentKey(a map[string]any) string {
	if truthy(a["name"]) {
		return pyStr(a["name"])
	}
	if truthy(a["pane_id"]) {
		return pyStr(a["pane_id"])
	}
	return ""
}

// agentSession は agent_session.value。
func agentSession(a map[string]any) any {
	if m, ok := a["agent_session"].(map[string]any); ok {
		return m["value"]
	}
	return nil
}
