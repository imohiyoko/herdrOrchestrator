package tickets

import (
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

// チケットの状態は設定 statuses で足したり外したりできる(taskboard/lib/core.sh の statuses_load と同じ読み方)。
// 1 行 1 つ「<状態> <分類> [= 説明]」。# 以降はコメント。分類は Categories のどれか。
// 設定が無い・不正なら組み込みの 6 つ(Statuses)。組み込みの名前は、設定に書かなくても組み込みの分類で数える。

// Categories は状態の分類(コードはこれで判断する)。
var Categories = []string{"todo", "active", "review", "blocked", "done", "cancelled"}

// builtin は組み込みの状態と分類(Statuses と同じ順)。
var builtin = [][2]string{{"todo", "todo"}, {"in_progress", "active"}, {"review", "review"}, {"done", "done"}, {"blocked", "blocked"}, {"cancelled", "cancelled"}}

// StatusSet は読んだ状態の一覧。
type StatusSet struct {
	Names  []string          // 設定した状態(書いた順)
	Broken bool              // 設定はあるが不正な本文(組み込みに戻した。tb と同じく警告する)
	cat    map[string]string // 状態 → 分類(組み込みの名前を含む)
}

// ParseStatuses は本文を読む。不正な行・必要な分類の欠けがあれば ok=false。
func ParseStatuses(body string) (names []string, cats map[string]string, ok bool) {
	cats = map[string]string{}
	have := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		if i := strings.IndexByte(line, '='); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if len(f) != 2 {
			return nil, nil, false
		}
		k, c := f[0], f[1]
		if utf8.RuneCountInString(k) > 30 || strings.ContainsAny(k, ",\"'`\\*?[]") || !slices.Contains(Categories, c) {
			return nil, nil, false
		}
		if _, dup := cats[k]; dup {
			return nil, nil, false
		}
		cats[k] = c
		have[c] = true
		names = append(names, k)
	}
	if len(names) == 0 || !have["todo"] || !have["active"] || !have["done"] {
		return nil, nil, false
	}
	return names, cats, true
}

// LoadStatuses は tb の config_lookup と同じ順に設定を引く:
// TB_STATUSES のファイル > DB のこのセッションの行 > 共通の行(*) > TB_CONFIG_DIR/statuses.conf。どこにも無ければ組み込み。
func LoadStatuses(db *sql.DB, session string) StatusSet {
	body, found := "", false
	if f := os.Getenv("TB_STATUSES"); f != "" {
		if b, err := os.ReadFile(f); err == nil {
			body, found = string(b), true
		}
	} else {
		var b string
		err := db.QueryRow(`SELECT body FROM main.config WHERE key='statuses' AND herdr_session IN (?, '*') ORDER BY herdr_session='*' LIMIT 1`, session).Scan(&b)
		if err == nil {
			body, found = b, true
		} else if d := os.Getenv("TB_CONFIG_DIR"); d != "" {
			if b, err := os.ReadFile(filepath.Join(d, "statuses.conf")); err == nil {
				body, found = string(b), true
			}
		}
	}
	var s StatusSet
	if found {
		if names, cats, ok := ParseStatuses(body); ok {
			s.Names, s.cat = names, cats
		} else {
			s.Broken = true
		}
	}
	if s.cat == nil {
		s.cat = map[string]string{}
		for _, b := range builtin {
			s.Names = append(s.Names, b[0])
			s.cat[b[0]] = b[1]
		}
	}
	for _, b := range builtin {
		if _, ok := s.cat[b[0]]; !ok {
			s.cat[b[0]] = b[1]
		}
	}
	return s
}

// In はその分類の状態(設定した順の後に組み込みの名前)。
func (s StatusSet) In(cats ...string) []string {
	var out []string
	add := func(k string) {
		if !slices.Contains(out, k) && slices.Contains(cats, s.cat[k]) {
			out = append(out, k)
		}
	}
	for _, k := range s.Names {
		add(k)
	}
	for _, b := range builtin {
		add(b[0])
	}
	return out
}

// SQLList は SQL の IN (...) の中身('a','b')。当たる状態が無ければ NULL。
func (s StatusSet) SQLList(cats ...string) string {
	var q []string
	for _, k := range s.In(cats...) {
		q = append(q, "'"+strings.ReplaceAll(k, "'", "''")+"'")
	}
	if len(q) == 0 {
		return "NULL"
	}
	return strings.Join(q, ",")
}
