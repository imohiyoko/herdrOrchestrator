package transport

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/capv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
)

// agent の起動(taskboard/lib/transport.sh の herdr_spawn)。準備待ちと送信の確認は ready.go。

// SpawnResult は herdr_spawn の結果(SPAWN_PANE / SPAWN_WS / SPAWN_ERR / SPAWN_KEPT)。
type SpawnResult struct {
	Pane, WS, Err string
	Kept          bool // agent が人の操作待ち(pane は閉じていない)
}

// firstJSON は出力の最初の JSON の値(読めなければ nil)。
func firstJSON(out []byte) map[string]any {
	var v any
	if json.NewDecoder(strings.NewReader(string(out))).Decode(&v) != nil {
		return nil
	}
	m, _ := v.(map[string]any)
	return m
}

// spawnErrorOf は spawn_error_of: 出力からエラーコードと短いメッセージ。
func spawnErrorOf(out []byte, rc int) (code, msg string) {
	if m := firstJSON(out); m != nil {
		if e, ok := m["error"].(map[string]any); ok {
			code, msg = firstLine(jqText(e["code"])), firstLine(jqText(e["message"]))
		}
	}
	if code == "" && msg == "" {
		msg = firstLine(string(out))
	}
	if code == "" {
		code = "exit " + strconv.Itoa(rc)
	}
	if r := []rune(code); len(r) > 64 {
		code = string(r[:64])
	}
	b := []byte(code)
	for i, c := range b {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == ' ' || c == '-') {
			b[i] = '_'
		}
	}
	code = string(b)
	msg = strings.NewReplacer("\t", " ", "\r", " ").Replace(msg)
	max := capv.Env("TB_SPAWN_ERR_MAX", 200) // unlimited で切らない
	if r := []rune(msg); len(r) > max {
		msg = string(r[:max])
	}
	return code, msg
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

// spawnLog は spawn_log_failure: 失敗した回ごとに 1 行(時刻・名前・エラーコード)。書けなくても結果は変えない。
func spawnLog(dbPath, name, code string) {
	p := os.Getenv("TB_SPAWN_LOG")
	if p == "" {
		p = filepath.Join(filepath.Dir(dbPath), "spawn-errors.log")
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	_, _ = w.WriteString(now() + "\t" + name + "\t" + code + "\n")
	_ = w.Flush()
}

func errText(code, msg string) string {
	if msg != "" {
		return code + ": " + msg
	}
	return code
}

// Spawn は herdr_spawn: 専用の workspace(label = 名前)を作り、その root pane で agent を起動する。
// 失敗なら ok=false(Err に理由。作った workspace は閉じる。人の操作待ちなら Kept で閉じない)。
func Spawn(dbPath, kind, name, cwd string, extra []string) (SpawnResult, bool) {
	var r SpawnResult
	out, err := herdr.CallCombined("workspace", "create", "--cwd", cwd, "--label", name, "--no-focus")
	m := firstJSON(out)
	if err == nil && m != nil {
		if res, ok := m["result"].(map[string]any); ok {
			if rp, ok := res["root_pane"].(map[string]any); ok {
				if p, ok := rp["pane_id"]; ok && p != nil && p != false {
					r.Pane = jqText(p)
				}
			}
			if ws, ok := res["workspace"].(map[string]any); ok {
				r.WS = jqText(ws["workspace_id"])
			}
		}
	}
	if err != nil || r.Pane == "" {
		code, msg := spawnErrorOf(out, 1)
		spawnLog(dbPath, name, "workspace:"+code)
		return SpawnResult{Err: "workspace を作れない(" + errText(code, msg) + ")"}, false
	}
	args := []string{"agent", "start", name, "--kind", kind, "--pane", r.Pane}
	if len(extra) > 0 {
		args = append(append(args, "--"), extra...)
	}
	var code, msg string
	for i := 1; i <= 3; i++ {
		out, err := herdr.CallCombined(args...)
		rc := 0
		if err != nil {
			rc = 1
			if ee, ok := err.(interface{ ExitCode() int }); ok && ee.ExitCode() > 0 {
				rc = ee.ExitCode()
			}
		}
		if rc == 0 && !hasError(out) {
			return r, true
		}
		if rc == 0 {
			rc = 1
		}
		code, msg = spawnErrorOf(out, rc)
		spawnLog(dbPath, name, code)
		if strings.Contains(code, "not_ready") || strings.Contains(code, "blocked") {
			r.Kept = true
			r.Err = "agent が人の操作を待っています(" + errText(code, msg) + ")。herdr で pane " + r.Pane + " を開き、確認画面などに答えてください(workspace は閉じていません)"
			return r, false
		}
		if i < 3 {
			sleepSec(envSec("TB_SPAWN_RETRY_SEC", "3"))
		}
	}
	r.Err = "agent を起動できない(" + errText(code, msg) + ")"
	if r.WS != "" {
		_, _ = herdr.CallCombined("workspace", "close", r.WS)
	} else {
		_, _ = herdr.CallCombined("pane", "close", r.Pane)
	}
	return r, false
}
