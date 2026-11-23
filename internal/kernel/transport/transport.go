// Package transport は herdr への送信と、その記録(ADR 0004 の土台。taskboard/lib/transport.sh の herdr_send / log_message)。
// 送ったもの・送れなかったものは messages に残す。
package transport

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
)

// 送信の結果(herdr_send の終了コードと同じ)
const (
	Sent   = 0
	Busy   = 10 // 宛先が使用中
	Failed = 11 // 状態取得・送信の失敗
)

func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }

// LogMessage は log_message: 送信の記録を messages に 1 行足す(書けなくても送信の結果は変えない)。
func LogMessage(db *sql.DB, session, kind, ref, target, body, result string) {
	_, _ = db.Exec(`INSERT INTO main.messages (herdr_session, kind, ref, target, body, result, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		session, kind, ref, target, body, result, now())
}

// Send は herdr_send: 宛先の状態を見て idle/done のときだけ送る。理由は stderr に bash と同じ文面で出す。
// session は herdr のセッション名(記録の herdr_session と文面に使う)。kind が空なら report。
func Send(db *sql.DB, session, target, msg, id, kind string, stderr io.Writer) int {
	if kind == "" {
		kind = "report"
	}
	if target == "" { // 既定の宛先はコードで決めない(設定 projects の * の行)
		fmt.Fprintf(stderr, "tb: 宛先が決まっていません(設定 projects に *=<名前> か <project>=<名前> を書く: tb config edit projects)。未送信(pending に残す): %s\n", id)
		return Failed
	}
	raw, _ := herdr.Call("agent", "get", target)
	var g struct {
		Result struct {
			Agent struct {
				AgentStatus any `json:"agent_status"`
			} `json:"agent"`
		} `json:"result"`
		Error struct {
			Code any `json:"code"`
		} `json:"error"`
	}
	st, ecode := "", ""
	if json.Unmarshal(raw, &g) == nil {
		st, ecode = jqText(g.Result.Agent.AgentStatus), jqText(g.Error.Code)
	}
	switch st {
	case "idle", "done":
	case "":
		LogMessage(db, session, kind, id, target, msg, "failed")
		if ecode == "agent_not_found" {
			fmt.Fprintf(stderr, "tb: 宛先 '%s' が herdr セッション '%s' に見つかりません(セッション違いの可能性。HERDR_SESSION=<名前> で指定)。未送信: %s\n", target, session, id)
		} else {
			fmt.Fprintf(stderr, "tb: 宛先 '%s' の状態を取得できません(herdr セッション '%s')。未送信(pending に残す): %s\n", target, session, id)
		}
		return Failed
	default:
		LogMessage(db, session, kind, id, target, msg, "busy")
		fmt.Fprintf(stderr, "tb: 宛先 '%s' が %s のため送りません。未送信(pending に残す): %s\n", target, st, id)
		return Busy
	}
	out, err := herdr.CallCombined("agent", "prompt", target, msg)
	if err != nil || hasError(out) {
		LogMessage(db, session, kind, id, target, msg, "failed")
		fmt.Fprintf(stderr, "tb: 送信に失敗しました。未送信(pending に残す): %s\n", id)
		fmt.Fprintln(stderr, strings.TrimRight(string(out), "\n"))
		return Failed
	}
	LogMessage(db, session, kind, id, target, msg, "sent")
	return Sent
}

// jqText は jq -r '.x // empty' の値(null・false は空。文字列以外は JSON の書き方)。
func jqText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case bool:
		if !x {
			return ""
		}
		return "true"
	case string:
		return x
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

// hasError は jq -e 'type=="object" and has("error")' と同じ(値が並ぶときは最後の値で決まる。読めなければ偽)。
func hasError(out []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(out))
	last, seen := false, false
	for {
		var v any
		if err := dec.Decode(&v); err == io.EOF {
			return seen && last
		} else if err != nil {
			return false
		}
		m, ok := v.(map[string]any)
		_, has := m["error"]
		last, seen = ok && has, true
	}
}
