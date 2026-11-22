// Package herdr は herdr との境界(ADR 0002 の herdr アダプタ。taskboard/lib/transport.sh の herdr_call)。
// tb の Go 版で herdr を実行するのはこのパッケージだけにする。テストでは HERDR_BIN を疑似 herdr にする。
package herdr

import (
	"encoding/json"
	"os"
	"os/exec"
)

// Bin は herdr の実体(HERDR_BIN。既定は PATH の herdr)。
func Bin() string {
	if b := os.Getenv("HERDR_BIN"); b != "" {
		return b
	}
	return "herdr"
}

// Call は herdr を引数そのままで実行し、標準出力を返す(標準エラーは捨てる。herdr_call 2>/dev/null と同じ)。
func Call(args ...string) ([]byte, error) {
	return exec.Command(Bin(), args...).Output()
}

// Agent は herdr agent list の 1 件(使う項目だけ)。
type Agent struct {
	PaneID       string `json:"pane_id"`
	Name         string `json:"name"`
	AgentStatus  any    `json:"agent_status"`
	AgentSession struct {
		Value string `json:"value"`
	} `json:"agent_session"`
}

// Agents は herdr agent list の .result.agents。
func Agents() ([]Agent, error) {
	out, err := Call("agent", "list")
	if err != nil {
		return nil, err
	}
	return ParseAgents(out)
}

// ParseAgents は herdr agent list の出力を読む(読めなければ err)。
func ParseAgents(out []byte) ([]Agent, error) {
	var r struct {
		Result struct {
			Agents []json.RawMessage `json:"agents"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return nil, err
	}
	var as []Agent
	for _, raw := range r.Result.Agents {
		var a Agent
		if json.Unmarshal(raw, &a) == nil {
			as = append(as, a)
		}
	}
	return as, nil
}

// CallCombined は herdr を実行し、標準出力と標準エラーをまとめて返す(out=$(herdr_call ... 2>&1) と同じ)。
func CallCombined(args ...string) ([]byte, error) {
	return exec.Command(Bin(), args...).CombinedOutput()
}
