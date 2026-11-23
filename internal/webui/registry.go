package webui

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// registry は待ち受けたポートの記録(STATE_DIR/gateway.json、ディレクトリ 700・ファイル 600)。

const maxEntry = 4096

type Gateway struct{ Port, PID int }

func gatewayPath() string { return filepath.Join(StateDir(), "gateway.json") }

func WriteGateway(port int) error {
	p := gatewayPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + "." + strconv.Itoa(os.Getpid()) + ".tmp"
	b, _ := json.Marshal(map[string]int{"port": port, "pid": os.Getpid()})
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// ReadGateway は動いている Web UI(checkAlive=false なら、動いているかは見ずに記録)か nil。
func ReadGateway(checkAlive bool) *Gateway {
	p := gatewayPath()
	st, err := os.Lstat(p)
	if err != nil || !st.Mode().IsRegular() || !ownedByMe(st) || st.Size() > maxEntry {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var d map[string]any
	if json.Unmarshal(b, &d) != nil {
		return nil
	}
	port, ok1 := wholeNum(d["port"])
	pid, ok2 := wholeNum(d["pid"])
	if !ok1 || !ok2 || port < 1 || port > 65535 {
		return nil
	}
	g := &Gateway{Port: port, PID: pid}
	if checkAlive && !g.alive() {
		return nil
	}
	return g
}

func wholeNum(v any) (int, bool) {
	f, ok := v.(float64)
	if !ok || f != float64(int(f)) {
		return 0, false
	}
	return int(f), true
}

func (g *Gateway) alive() bool {
	if !pidAlive(g.PID) {
		return false
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(g.Port)), 300*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// ClearGateway は自分(この pid)が書いた記録だけ消す。
func ClearGateway() {
	if g := ReadGateway(false); g != nil && g.PID == os.Getpid() {
		os.Remove(gatewayPath())
	}
}
