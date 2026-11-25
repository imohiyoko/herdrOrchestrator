package agents

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/capv"
)

// 役割を渡す(taskboard/lib/roles.sh の load_role / last_briefed_hash / record_briefing)。

// Role は load_role が埋めるもの。Files が空でも Label は空。本文が無ければ Hash と Text は空。
type Role struct {
	Files      []string
	Label      string // hr.md,manager.md
	Hash, Text string
}

// roleMax は ROLE_MAX(渡す本文の上限。超えたら切る)。
// 負の値は既定に戻す。unlimited で切らない。
func roleMax() int { return capv.Env("TB_ROLE_MAX", 12000) }

// LoadRole は load_role: agent に渡す役割の本文(_common のあとに役割を順に)。渡すものが無ければ ok=false。
func (r *Roles) LoadRole(name string) (Role, bool) {
	var ro Role
	ro.Files = r.FilesForLoose(name)
	labels := make([]string, len(ro.Files))
	for i, f := range ro.Files {
		labels[i] = f + ".md"
	}
	ro.Label = strings.Join(labels, ",")
	var b strings.Builder
	if c, ok := r.Lookup("_common"); ok {
		b.WriteString(c + "\n\n")
	}
	for _, f := range ro.Files {
		if c, ok := r.Lookup(f); ok {
			b.WriteString(c + "\n\n")
		}
	}
	body := b.String()
	if strings.TrimFunc(body, func(c rune) bool { return c == ' ' || (c >= '\t' && c <= '\r') }) == "" {
		return ro, false
	}
	sum := sha256.Sum256([]byte(body))
	ro.Hash = hex.EncodeToString(sum[:])
	if max := roleMax(); utf8.RuneCountInString(body) > max { // 数えるのも切るのも文字の単位(UTF-8 のロケールの bash と同じ)
		cut, n := 0, 0
		for cut < len(body) && n < max {
			_, sz := utf8.DecodeRuneInString(body[cut:])
			cut += sz
			n++
		}
		body = body[:cut] + "\n\n(長すぎるため途中で切りました。全文は tb role-show で読んでください)"
	}
	ro.Text = "# あなたの役割(agent 名: " + name + "。tb が自動で渡しています。版 " + ro.Hash[:12] + ")\n\n" + body
	return ro, true
}

// LastBriefedHash は last_briefed_hash: その会話に最後に渡した版(無ければ空)と、渡した時刻。
func LastBriefedHash(db *sql.DB, sid string) (hash, at string) {
	_ = db.QueryRow(`SELECT hash, created_at FROM role_briefings WHERE session_id=? ORDER BY id DESC LIMIT 1`, sid).Scan(&hash, &at)
	return
}

// RecordBriefing は record_briefing。
func RecordBriefing(db *sql.DB, session, sid, agent, file, hash, source, via string) error {
	var f any
	if file != "" {
		f = file
	}
	_, err := db.Exec(`INSERT INTO main.role_briefings (herdr_session, session_id, agent, role_file, hash, source, via, created_at) VALUES (?,?,?,?,?,?,?,?)`,
		session, sid, agent, f, hash, source, via, time.Now().UTC().Format("2006-01-02T15:04:05Z"))
	return err
}
