package webui

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/scrypt"
)

// webui/auth.py と同じ。ファイル(auth.json・sessions.json・lockout.json・auth.lock・allow-reset)の形も同じなので、
// Python 版と Go 版を入れ替えてもログインは続く。

const (
	MinPassword   = 12
	MaxField      = 256
	FormTokenTTL  = 1800
	ResetWindow   = 600
	scryptN       = 1 << 14
	scryptR       = 8
	scryptP       = 1
	scryptDKLen   = 32
	sessionTTLDef = 12 * 3600
)

func envInt(name string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(name)); err == nil {
		return n
	}
	return def
}

func SessionTTL() int { return envInt("WEBUI_SESSION_TTL", sessionTTLDef) }
func lockMax() int    { return envInt("WEBUI_LOCK_MAX", 5) }
func lockSec() int    { return envInt("WEBUI_LOCK_SEC", 300) }

// FailDelay は失敗時の遅延(WEBUI_FAIL_DELAY 秒、既定 1)。
func FailDelay() time.Duration {
	f, err := strconv.ParseFloat(os.Getenv("WEBUI_FAIL_DELAY"), 64)
	if err != nil {
		f = 1.0
	}
	return time.Duration(f * float64(time.Second))
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// writePrivateJSON は 0600 で原子的に書く。
func writePrivateJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%s.tmp", path, randHex(4))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func hashPassword(pw string, salt []byte, n, r, p, dk int) (map[string]any, error) {
	if salt == nil {
		salt = make([]byte, 16)
		_, _ = rand.Read(salt)
	}
	h, err := scrypt.Key([]byte(pw), salt, n, r, p, dk)
	if err != nil {
		return nil, err
	}
	return map[string]any{"algo": "scrypt", "n": n, "r": r, "p": p, "dklen": dk, "salt": hex.EncodeToString(salt), "hash": hex.EncodeToString(h)}, nil
}

func asInt(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		if x == float64(int(x)) {
			return int(x), true
		}
	case string:
		n, err := strconv.Atoi(x)
		return n, err == nil
	}
	return 0, false
}

func verifyPassword(pw string, rec map[string]any) bool {
	var ps [4]int
	for i, k := range []string{"n", "r", "p", "dklen"} {
		n, ok := asInt(rec[k])
		if !ok {
			return false
		}
		ps[i] = n
	}
	salt, err := hex.DecodeString(fmt.Sprint(rec["salt"]))
	if err != nil || ps[0] > 1<<20 || ps[1] > 64 || ps[2] > 16 || ps[3] > 1024 {
		return false
	}
	calc, err := hashPassword(pw, salt, ps[0], ps[1], ps[2], ps[3])
	if err != nil {
		return false
	}
	want, _ := rec["hash"].(string)
	return hmac.Equal([]byte(calc["hash"].(string)), []byte(want))
}

// SafeNext は同一オリジンのパスだけ許す。外部 URL・スキーム付き・// 始まりは "/"。
func SafeNext(n string) string {
	if !strings.HasPrefix(n, "/") || strings.HasPrefix(n, "//") || strings.Contains(n, `\`) {
		return "/"
	}
	for _, c := range n {
		if c < 32 || c == 127 {
			return "/"
		}
	}
	p := n
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	if p == "/login" || p == "/setup" || p == "/logout" {
		return "/"
	}
	return n
}

// Auth は auth.py の Auth。
type Auth struct {
	Dir, AllowPath, authPath, sessPath, lockoutPath, lockPath string
	mu                                                        sync.Mutex
	key                                                       []byte
}

func NewAuth(dir string) (*Auth, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	return &Auth{Dir: dir, AllowPath: filepath.Join(abs, "allow-reset"), authPath: filepath.Join(dir, "auth.json"),
		sessPath: filepath.Join(dir, "sessions.json"), lockoutPath: filepath.Join(dir, "lockout.json"),
		lockPath: filepath.Join(dir, "auth.lock"), key: key}, nil
}

// locked はプロセス内とプロセス間(flock)の両方で排他する。
func (a *Auth) locked(fn func() error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	unlock, err := flockFile(a.lockPath)
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

func (a *Auth) Configured() bool { return isFile(a.authPath) }

func isFile(p string) bool { st, err := os.Stat(p); return err == nil && st.Mode().IsRegular() }

// Setup は auth.json が無いときだけ作る。作れたら true。
func (a *Auth) Setup(user, pw string) (bool, error) {
	ph, err := hashPassword(pw, nil, scryptN, scryptR, scryptP, scryptDKLen)
	if err != nil {
		return false, err
	}
	b, _ := json.Marshal(map[string]any{"version": 1, "username": user, "password": ph})
	tmp := fmt.Sprintf("%s.%s.tmp", a.authPath, randHex(4))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, err
	}
	_, err = f.Write(b)
	f.Close()
	defer os.Remove(tmp)
	if err != nil {
		return false, err
	}
	if err := os.Link(tmp, a.authPath); err != nil { // 既にあれば失敗する(排他的な作成)
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (a *Auth) load() map[string]any {
	b, err := os.ReadFile(a.authPath)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func (a *Auth) readLockout() (int, float64) {
	b, err := os.ReadFile(a.lockoutPath)
	if err != nil {
		return 0, 0
	}
	var d map[string]any
	if json.Unmarshal(b, &d) != nil {
		return 0, 0
	}
	f, ok1 := d["fails"].(float64)
	u, ok2 := d["until"].(float64)
	if !ok1 || !ok2 || f != float64(int(f)) {
		return 0, 0
	}
	return int(f), u
}

func (a *Auth) writeLockout(fails int, until float64) error {
	return writePrivateJSON(a.lockoutPath, map[string]any{"fails": fails, "until": until})
}

// LockedFor はロック中なら残り秒(切り上げ)、でなければ 0。期限切れのロックは解く。
func (a *Auth) LockedFor() int {
	_, until := a.readLockout()
	left := until - now()
	if left <= 0 && until != 0 {
		_ = a.locked(func() error {
			if _, u2 := a.readLockout(); u2 == until {
				return a.writeLockout(0, 0)
			}
			return nil
		})
	}
	if left > 0 {
		return int(left) + 1
	}
	return 0
}

// Attempt は true/false。失敗の理由は区別しない(常に両方を検証する)。
func (a *Auth) Attempt(user, pw string) bool {
	rec := a.load()
	ok := false
	if rec != nil {
		u, _ := rec["username"].(string)
		uOK := subtle.ConstantTimeCompare([]byte(u), []byte(user)) == 1
		pr, _ := rec["password"].(map[string]any)
		pOK := verifyPassword(pw, pr)
		ok = uOK && pOK
	}
	_ = a.locked(func() error {
		fails, until := a.readLockout()
		if ok {
			if fails != 0 || until != 0 {
				return a.writeLockout(0, 0)
			}
			return nil
		}
		fails++
		if fails >= lockMax() {
			until = now() + float64(lockSec())
		}
		return a.writeLockout(fails, until)
	})
	return ok
}

func hashTok(tok string) string { s := sha256.Sum256([]byte(tok)); return hex.EncodeToString(s[:]) }

func (a *Auth) readSessions() map[string]any {
	b, err := os.ReadFile(a.sessPath)
	if err != nil {
		return map[string]any{}
	}
	var d map[string]any
	if json.Unmarshal(b, &d) != nil || d == nil {
		return map[string]any{}
	}
	return d
}

func (a *Auth) writeSessions(d map[string]any) error {
	t := now()
	out := map[string]any{}
	for k, v := range d {
		if f, ok := v.(float64); ok && f > t {
			out[k] = f
		}
	}
	return writePrivateJSON(a.sessPath, out)
}

// CreateSession は新しいセッションの値(32 バイトの乱数)を返す。サーバー側には SHA-256 だけを期限付きで残す。
func (a *Auth) CreateSession() (string, error) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	tok := base64.RawURLEncoding.EncodeToString(b)
	err := a.locked(func() error {
		d := a.readSessions()
		d[hashTok(tok)] = now() + float64(SessionTTL())
		return a.writeSessions(d)
	})
	return tok, err
}

// SessionHash は有効なセッションならそのハッシュ、でなければ ""。
func (a *Auth) SessionHash(tok string) string {
	if tok == "" {
		return ""
	}
	h := hashTok(tok)
	if f, ok := a.readSessions()[h].(float64); ok && f > now() {
		return h
	}
	return ""
}

func (a *Auth) DestroySession(tok string) {
	if tok == "" {
		return
	}
	_ = a.locked(func() error {
		d := a.readSessions()
		if _, ok := d[hashTok(tok)]; !ok {
			return nil
		}
		delete(d, hashTok(tok))
		return a.writeSessions(d)
	})
}

func (a *Auth) sign(ctx string, ts int64, nonce string) string {
	m := hmac.New(sha256.New, a.key)
	fmt.Fprintf(m, "%s|%d|%s", ctx, ts, nonce)
	return hex.EncodeToString(m.Sum(nil))
}

// FormToken はフォームの隠しトークン(署名付き・短命。署名鍵はプロセス内だけ)。
func (a *Auth) FormToken(ctx string) string {
	ts, nonce := time.Now().Unix(), randHex(8)
	return fmt.Sprintf("%d.%s.%s", ts, nonce, a.sign(ctx, ts, nonce))
}

func (a *Auth) CheckFormToken(tok, ctx string) bool {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return false
	}
	ts, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	if err != nil {
		return false
	}
	age := now() - float64(ts)
	if age < -5 || age > FormTokenTTL {
		return false
	}
	return hmac.Equal([]byte(a.sign(ctx, ts, parts[1])), []byte(parts[2]))
}

// ResetState は "ok" | "missing" | "expired" | "invalid"。通常ファイルで自分の所有のものだけ有効。
func (a *Auth) ResetState() string {
	st, err := os.Lstat(a.AllowPath)
	if errors.Is(err, os.ErrNotExist) {
		return "missing"
	}
	if err != nil {
		return "invalid"
	}
	if !st.Mode().IsRegular() || !ownedByMe(st) {
		return "invalid"
	}
	age := now() - float64(st.ModTime().UnixNano())/1e9
	if age < -60 {
		return "invalid"
	}
	if age <= ResetWindow {
		return "ok"
	}
	return "expired"
}

// ResetPassword は許可が有効なら、ハッシュ更新・許可ファイル削除・全セッション無効化・ロックアウト解除。成功は "done"。
func (a *Auth) ResetPassword(pw string) string {
	out := ""
	_ = a.locked(func() error {
		if out = a.ResetState(); out != "ok" {
			return nil
		}
		rec := a.load()
		if rec == nil {
			out = "missing"
			return nil
		}
		if os.Remove(a.AllowPath) != nil {
			out = "invalid"
			return nil
		}
		ph, err := hashPassword(pw, nil, scryptN, scryptR, scryptP, scryptDKLen)
		if err != nil {
			out = "invalid"
			return nil
		}
		rec["password"] = ph
		if err := writePrivateJSON(a.authPath, rec); err != nil {
			out = "invalid"
			return nil
		}
		_ = writePrivateJSON(a.sessPath, map[string]any{})
		_ = a.writeLockout(0, 0)
		out = "done"
		return nil
	})
	return out
}
