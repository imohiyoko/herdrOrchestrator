package transport

import (
	"encoding/json"
	"os"
	"strconv"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
)

// 起動直後・送信直後の agent の様子を見る(lib/herdr-ready.sh の herdr_wait_ready / herdr_verify_prompt)。

func envSec(k, def string) float64 {
	v := os.Getenv(k)
	if v == "" {
		v = def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0 // awk は数でない値を 0 とみなす
	}
	return f
}

// tries は _hr_tries: 上限と間隔から繰り返し回数(最低 1 回)。
func tries(t, p float64) int {
	if p <= 0 {
		p = 1
	}
	n := int(t/p) + 1
	if n < 1 {
		n = 1
	}
	return n
}

// state は _hr_state: agent の状態と interactive_ready(取れなければ空)。
func state(name string) (st string, ready bool) {
	out, _ := herdr.Call("agent", "get", name) // 失敗しても出力は読む(bash のパイプと同じ)
	var g struct {
		Result struct {
			Agent struct {
				AgentStatus      any `json:"agent_status"`
				InteractiveReady any `json:"interactive_ready"`
			} `json:"agent"`
		} `json:"result"`
	}
	if json.Unmarshal(out, &g) != nil {
		return "", false
	}
	r, _ := g.Result.Agent.InteractiveReady.(bool)
	return jqText(g.Result.Agent.AgentStatus), r
}

func sleepSec(s float64) { time.Sleep(time.Duration(s * float64(time.Second))) }

// WaitReady は herdr_wait_ready: idle かつ interactive_ready になるまで待つ(TB_SPAWN_READY_SEC / TB_SPAWN_POLL_SEC)。
func WaitReady(name string) bool {
	poll := envSec("TB_SPAWN_POLL_SEC", "2")
	n := tries(envSec("TB_SPAWN_READY_SEC", "60"), poll)
	for i := 0; i < n; i++ {
		if st, r := state(name); st == "idle" && r {
			return true
		}
		if i < n-1 {
			sleepSec(poll)
		}
	}
	return false
}

// VerifyPrompt は herdr_verify_prompt: 送信後に idle を抜けた(動き出した)か確かめる。idle のままなら Enter だけを 1 回送って
// もう一度見る(本文は再送しない)。TB_PROMPT_VERIFY_SEC=0 なら確かめない。
func VerifyPrompt(name string) bool {
	if os.Getenv("TB_PROMPT_VERIFY_SEC") == "0" {
		return true
	}
	poll := envSec("TB_VERIFY_POLL_SEC", "1")
	n := tries(envSec("TB_PROMPT_VERIFY_SEC", "15"), poll)
	for pass := 1; pass <= 2; pass++ {
		for i := 0; i < n; i++ {
			if st, _ := state(name); st != "" && st != "idle" {
				return true
			}
			if i < n-1 {
				sleepSec(poll)
			}
		}
		if pass == 1 {
			_, _ = herdr.CallCombined("agent", "send-keys", name, "Enter")
		}
	}
	return false
}
