package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"

	"github.com/imohiyoko/API/port"
)

// Tokens は、入口が確かめた主体をプロセス内の短命のトークンにする(port.Authenticator)。
// imohiyoko/API は確かめ済みの主体を直接渡す口を持たないので、入口は Issue で得たトークンを Invoke に渡し、
// 呼び終えたら取り消す。トークンはプロセスの外に出さない(ファイル・環境変数・HTTP に書かない)。
type Tokens struct {
	mu sync.Mutex
	m  map[string]port.Principal
}

// Issue は p のトークンと、それを取り消す関数を返す。
func (t *Tokens) Issue(p port.Principal) (string, func()) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("app: crypto/rand: " + err.Error()) // Go 1.24 以降の crypto/rand は失敗しない
	}
	tok := hex.EncodeToString(b[:])
	p.Token = ""
	t.mu.Lock()
	if t.m == nil {
		t.m = map[string]port.Principal{}
	}
	t.m[tok] = p
	t.mu.Unlock()
	return tok, func() {
		t.mu.Lock()
		delete(t.m, tok)
		t.mu.Unlock()
	}
}

// Authenticate は port.Authenticator の実装。
func (t *Tokens) Authenticate(_ context.Context, token string) (port.Principal, error) {
	t.mu.Lock()
	p, ok := t.m[token]
	t.mu.Unlock()
	if !ok || token == "" {
		return port.Principal{}, port.ErrUnauthenticated
	}
	p.Token = token
	return p, nil
}
