package agents

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// 役割を渡す(taskboard/lib/roles.sh の load_role / last_briefed_hash / record_briefing)。

// Role は load_role が埋めるもの。Files が空でも Label は空。本文が無ければ Hash と Text は空。
type Role struct {
	Files      []string
	Label      string // hr.md,manager.md
	Hash, Text string
}

// roleMax は ROLE_MAX(渡す本文の上限。超えたら切る)。
func roleMax() int {
	if n, err := strconv.Atoi(os.Getenv("TB_ROLE_MAX")); err == nil {
		return n
	}
	return 12000
}

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
	if max := roleMax(); len(body) > max {
		cut := max
		for cut > 0 && !utf8.RuneStart(body[cut]) { // 文字の途中では切らない
			cut--
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
