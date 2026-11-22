package gate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// IngestConf は gate-ingest.conf を読んだ結果(_gate_load_ingest_conf)。
type IngestConf struct {
	GH, Login        string
	SevKeys, SevVals []string
	Warns            []string
}

var (
	reIngSection = regexp.MustCompile(`^\[([a-z]+)\]$`)
	reLogin      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,38}(\[bot\])?$`)
	reSevKey     = regexp.MustCompile(`^[a-z][a-z0-9 -]{0,31}$`)
	evRE         = regexp.MustCompile(`^ts=([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z) task=([A-Za-z0-9._-]{1,64}) repo=([A-Za-z0-9._-]+/[A-Za-z0-9._-]+) pr=([0-9]+) kind=(review|inline|issue) event=([0-9]{1,15}) actor=([A-Za-z0-9][A-Za-z0-9-]{0,38}(\[bot\])?) actor_id=([0-9]{1,12}) actor_type=(user|bot|org) assoc=([a-z_]{1,32}) author=([01]) sha=([0-9a-f]{40}|-) state=([a-z_]{1,32}|-) label=(blocker|should|none) addr=([01]) body_sha256=([0-9a-f]{64}) upd=([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z|-) review=([0-9]{1,15}|-) orig_sha=([0-9a-f]{40}|-) outdated=([01]) head=([0-9a-f]{40}) by=ingest$`)
)

// LoadIngestConf は _gate_load_ingest_conf(needLogin: login を必須にするか)。
func LoadIngestConf(f string, needLogin bool) (c IngestConf, err string) {
	c.SevKeys = []string{"critical", "major", "potential issue", "minor", "trivial", "nitpick", "refactor suggestion"}
	c.SevVals = []string{"blocker", "blocker", "should", "should", "should", "should", "should"}
	if !existsFollow(f) {
		return c, "設定ファイルがありません(config/gate-ingest.conf)。gh の専用ラッパーの絶対パスを指定してください"
	}
	body, ok := readable(f)
	if !ok {
		return c, "設定ファイルを読めません"
	}
	sec := ""
	for i, line := range lines(body) {
		n := i + 1
		line = trim(cutHash(line))
		if line == "" {
			continue
		}
		if m := reIngSection.FindStringSubmatch(line); m != nil {
			sec = m[1]
			if sec != "gh" && sec != "source" && sec != "severity" {
				return c, fmt.Sprintf("L%d: 未知の節 [%s]", n, sec)
			}
			continue
		}
		k, v, has := strings.Cut(line, "=")
		if sec == "" || !has {
			return c, fmt.Sprintf("L%d: '[節]' の後の 'キー = 値' の形ではありません", n)
		}
		k, v = trim(k), trim(v)
		switch {
		case sec == "gh" && k == "path":
			c.GH = v
		case sec == "source" && k == "login":
			if !reLogin.MatchString(v) {
				return c, fmt.Sprintf("L%d: login の形が不正です", n)
			}
			c.Login = v
		case sec == "severity":
			if !reSevKey.MatchString(k) {
				return c, fmt.Sprintf("L%d: 重大度の表記は小文字英数字・空白・- の 32 文字までです: %s", n, k)
			}
			if v != "blocker" && v != "should" {
				return c, fmt.Sprintf("L%d: 重大度の対応先は blocker | should です: %s", n, v)
			}
			found := false
			for j := range c.SevKeys {
				if c.SevKeys[j] != k {
					continue
				}
				found = true
				if c.SevVals[j] == "blocker" && v == "should" {
					c.Warns = append(c.Warns, "緩め設定は無効: [severity] "+k+" = should(blocker のまま)")
				} else {
					c.SevVals[j] = v
				}
			}
			if !found {
				c.SevKeys, c.SevVals = append(c.SevKeys, k), append(c.SevVals, v)
			}
		default:
			return c, fmt.Sprintf("L%d: 未知のキー [%s] %s", n, sec, k)
		}
	}
	if needLogin && c.Login == "" {
		return c, "[source] login がありません(例: coderabbitai[bot])"
	}
	if c.GH == "" {
		return c, "[gh] path が指定されていないため止めます(fail-closed)。専用の gh ラッパーの絶対パスを指定してください。PATH 上の gh には落としません"
	}
	st, serr := os.Stat(c.GH)
	if !strings.HasPrefix(c.GH, "/") || serr != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0o111 == 0 {
		return c, "指定された gh を使えないため止めます(fail-closed): " + c.GH
	}
	return c, ""
}

// Ingest は ingest-review の判定(jq の固定のプログラムと同じ)。status は none | skipped | ok。
type Ingest struct {
	Status               string
	Blocker, Should, Unk int
}

// IngestReview は reviews / inline / issue のコメントから、head の sha に対する外部レビューの件数を数える。解析できなければ err。
func (c IngestConf) IngestReview(rv, cm, is []any, sha string) (res Ingest, err error) {
	defer func() {
		if r := recover(); r != nil {
			if r != errBad {
				panic(r)
			}
			err = errBad
		}
	}()
	var heads []any
	for _, r := range rv {
		if !equal(mustGet(r, "user", "login"), c.Login) {
			continue
		}
		cid := alt(mustGet(r, "commit_id"), "")
		if !re40.MatchString(str(cid)) || !strings.HasPrefix(cid.(string), sha) {
			continue
		}
		st := mustGet(r, "state")
		if !equal(st, "COMMENTED") && !equal(st, "APPROVED") && !equal(st, "CHANGES_REQUESTED") {
			continue
		}
		heads = append(heads, r)
	}
	sortBy(heads, func(x any) any { return alt(mustGet(x, "submitted_at"), "") })
	if len(heads) == 0 {
		return Ingest{Status: "none"}, nil
	}
	h := heads[len(heads)-1]
	if skipmark(mustGet(h, "body")) {
		return Ingest{Status: "skipped"}, nil
	}
	hs := alt(mustGet(h, "submitted_at"), "")
	for _, x := range is {
		if equal(mustGet(x, "user", "login"), c.Login) && less(hs, alt(mustGet(x, "updated_at"), "")) && skipmark(mustGet(x, "body")) {
			return Ingest{Status: "skipped"}, nil
		}
	}
	res.Status = "ok"
	for _, x := range cm {
		if !equal(mustGet(x, "user", "login"), c.Login) {
			continue
		}
		if mustGet(x, "position") == nil && !equal(mustGet(x, "subject_type"), "file") {
			continue
		}
		if addressed(mustGet(x, "body")) {
			continue
		}
		switch c.sev(mustGet(x, "body")) {
		case "blocker":
			res.Blocker++
		case "unknown":
			res.Unk++
			res.Should++
		default:
			res.Should++
		}
	}
	return res, nil
}

// AppendRecord は _gate_append_record: 固定の置き場に 1 行だけ追記する(symlink・通常でないファイルは拒否)。
func AppendRecord(f string, task, by, pr, lin, sha string, blk, shd int, ver, ev string) error {
	if err := os.MkdirAll(filepath.Dir(f), 0o777); err != nil {
		return fmt.Errorf("記録の置き場を作れません")
	}
	if st, err := os.Lstat(f); err == nil && (st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular()) {
		return fmt.Errorf("記録の置き場が通常のファイルではありません")
	}
	line := fmt.Sprintf("ts=%s task=%s by=%s pr=%s REVIEW %s %s blocker=%d should=%d verdict=%s evidence=%s", now(), task, by, pr, lin, sha, blk, shd, ver, ev)
	if !recRE.MatchString(line) {
		return fmt.Errorf("記録の形を作れませんでした(内部エラー)")
	}
	if err := appendFile(f, line+"\n"); err != nil {
		return fmt.Errorf("記録に失敗しました")
	}
	return nil
}

func appendFile(f, s string) error {
	fh, err := os.OpenFile(f, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600) // umask 077
	if err != nil {
		return err
	}
	if _, err := fh.WriteString(s); err != nil {
		fh.Close()
		return err
	}
	return fh.Close()
}

func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }

// EventsResult は ingest-events の結果。
type EventsResult struct{ New, Unchanged int }

// EventsError は記録しなかった理由(Code: 1 置き場・既存の記録 / 12 取得・解析)。
type EventsError struct {
	Code int
	Msg  string
}

func (e *EventsError) Error() string { return e.Msg }

// EventKeys は既存の記録を厳密に読み、同じ発言の同じ内容のキーを返す(壊れていれば止まる)。
func EventKeys(f string) (map[string]bool, error) {
	keys := map[string]bool{}
	if st, err := os.Lstat(f); err == nil && (st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular()) {
		return nil, &EventsError{1, "記録の置き場が通常のファイルではありません"}
	}
	body, ok := readable(f)
	if !ok {
		return keys, nil
	}
	for i, l := range lines(body) {
		if l == "" {
			continue
		}
		m := evRE.FindStringSubmatch(l)
		if m == nil {
			return nil, &EventsError{1, fmt.Sprintf("既存の記録の形式違反(L%d)のため止めます", i+1)}
		}
		keys[strings.Join([]string{m[2], m[3], m[4], m[5], m[6], m[17], m[18], m[14], m[13], m[21], m[7], m[10], m[11]}, "|")] = true
	}
	return keys, nil
}

// IngestEvents は ingest-events の取り出し: PR に付いた全レビューを身元つきの行にし、新しいものだけを返す(1 件でも不正なら何も返さない)。
func (c IngestConf) IngestEvents(keys map[string]bool, pr map[string]any, rv, cm, is []any, tid, owner, repo, num string) (newLines []string, skip int, err error) {
	defer func() {
		if r := recover(); r != nil {
			if r != errBad {
				panic(r)
			}
			err = &EventsError{12, "解析に失敗したため記録しません"}
		}
	}()
	aid := alt(mustGet(pr, "user", "id"), "")
	head := tostring(alt(mustGet(pr, "head", "sha"), ""))
	who := func(x any) []string {
		return []string{tostring(alt(mustGet(x, "user", "login"), "")), tostring(alt(mustGet(x, "user", "id"), "")), tostring(alt(mustGet(x, "user", "type"), "")),
			asciiDown(str(alt(mustGet(x, "author_association"), "")))}
	}
	isAuth := func(x any) string { return strconv.FormatBool(equal(alt(mustGet(x, "user", "id"), ""), aid)) }
	body := func(x any) string { return tostring(alt(mustGet(x, "body"), "")) }
	var evs [][]string
	for _, x := range rv {
		f := append([]string{"review", tostring(mustGet(x, "id"))}, who(x)...)
		f = append(f, isAuth(x), tostring(alt(mustGet(x, "commit_id"), "-")), asciiDown(str(alt(mustGet(x, "state"), "-"))), "none", "0",
			tostring(alt(mustGet(x, "submitted_at"), "-")), tostring(mustGet(x, "id")), "-", "0", head, body(x))
		evs = append(evs, f)
	}
	for _, x := range cm {
		f := append([]string{"inline", tostring(mustGet(x, "id"))}, who(x)...)
		s := c.sev(mustGet(x, "body"))
		if s == "unknown" {
			s = "should"
		}
		addr := "0"
		if addressed(mustGet(x, "body")) {
			addr = "1"
		}
		outd := "0"
		if mustGet(x, "position") == nil && !equal(mustGet(x, "subject_type"), "file") {
			outd = "1"
		}
		f = append(f, isAuth(x), tostring(alt(mustGet(x, "commit_id"), "-")), "-", s, addr, tostring(alt(mustGet(x, "updated_at"), "-")),
			tostring(alt(mustGet(x, "pull_request_review_id"), "-")), tostring(alt(mustGet(x, "original_commit_id"), "-")), outd, head, body(x))
		evs = append(evs, f)
	}
	for _, x := range is {
		f := append([]string{"issue", tostring(mustGet(x, "id"))}, who(x)...)
		f = append(f, isAuth(x), "-", "-", "none", "0", tostring(alt(mustGet(x, "updated_at"), "-")), "-", "-", "0", head, body(x))
		evs = append(evs, f)
	}
	ts := now()
	seen := map[string]bool{}
	for k := range keys {
		seen[k] = true
	}
	for _, e := range evs {
		kind, id, login, lid, ltype, assoc, auth, sha, state, label, addr, upd, rvid, osha, outd, hd := e[0], e[1], e[2], e[3], e[4], e[5], e[6], e[7], e[8], e[9], e[10], e[11], e[12], e[13], e[14], e[15]
		t := map[string]string{"User": "user", "Bot": "bot", "Organization": "org"}[ltype]
		if assoc == "" {
			assoc = "unknown"
		}
		a := "0"
		if auth == "true" {
			a = "1"
		}
		sum := sha256.Sum256([]byte(e[16]))
		hs := hex.EncodeToString(sum[:])
		line := fmt.Sprintf("ts=%s task=%s repo=%s/%s pr=%s kind=%s event=%s actor=%s actor_id=%s actor_type=%s assoc=%s author=%s sha=%s state=%s label=%s addr=%s body_sha256=%s upd=%s review=%s orig_sha=%s outdated=%s head=%s by=%s",
			ts, tid, owner, repo, num, kind, id, login, lid, t, assoc, a, sha, state, label, addr, hs, upd, rvid, osha, outd, hd, IngestSender)
		if t == "" || !evRE.MatchString(line) {
			return nil, 0, &EventsError{12, fmt.Sprintf("形式が不正な発言があるため、何も記録しません(種類 %s、id %s)", kind, firstN(id, 20))}
		}
		key := strings.Join([]string{tid, owner + "/" + repo, num, kind, id, hs, upd, state, sha, outd, login, t, assoc}, "|")
		if seen[key] {
			skip++
			continue
		}
		seen[key] = true
		newLines = append(newLines, line)
	}
	return newLines, skip, nil
}

// AppendLines は記録に行を足す(umask 077)。
func AppendLines(f string, ls []string) error {
	if len(ls) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o777); err != nil {
		return fmt.Errorf("記録の置き場を作れません")
	}
	if err := appendFile(f, strings.Join(ls, "\n")+"\n"); err != nil {
		return fmt.Errorf("記録に失敗しました")
	}
	return nil
}

// Reviewers は _gate_cmd_reviewers の集計(表示だけ)。記録が無ければ ok=false。読めない・形式違反は err。
func Reviewers(f, repo, num, task string) (events int, out []string, ok bool, err error) {
	if !existsFollow(f) {
		return 0, nil, false, nil
	}
	lst, lerr := os.Lstat(f)
	body, rok := readable(f)
	if lerr != nil || lst.Mode()&os.ModeSymlink != 0 || !rok {
		return 0, nil, true, fmt.Errorf("記録を読めません(通常のファイルではない)")
	}
	type latest struct {
		ord     int
		summary []string
	}
	last := map[string]*latest{}
	n := 0
	for i, l := range lines(body) {
		n = i + 1
		if l == "" {
			continue
		}
		m := evRE.FindStringSubmatch(l)
		if m == nil {
			return 0, nil, true, fmt.Errorf("記録の形式違反(L%d)", n)
		}
		if m[3] != repo || m[4] != num || (task != "" && m[2] != task) {
			continue
		}
		k := m[5] + ":" + m[6]
		last[k] = &latest{n, []string{m[9], m[7], m[10], m[11], m[12], m[5], m[15], m[18]}}
	}
	var ls []*latest
	for _, v := range last {
		ls = append(ls, v)
	}
	sort.Slice(ls, func(i, j int) bool { return ls[i].ord < ls[j].ord })
	type agg struct {
		logins             []string
		cur, ty, as, la    string
		au                 bool
		nr, ni, ns, nb, nh int
	}
	ids := map[string]*agg{}
	var order []string
	for _, v := range ls {
		id, login, typ, assoc, auth, kind, label, upd := v.summary[0], v.summary[1], v.summary[2], v.summary[3], v.summary[4], v.summary[5], v.summary[6], v.summary[7]
		a := ids[id]
		if a == nil {
			a = &agg{}
			ids[id] = a
			order = append(order, id)
		}
		a.cur = login
		has := false
		for _, x := range a.logins {
			if x == login {
				has = true
			}
		}
		if !has {
			a.logins = append(a.logins, login)
		}
		a.ty, a.as = typ, assoc
		if auth == "1" {
			a.au = true
		}
		switch kind {
		case "review":
			a.nr++
		case "inline":
			a.ni++
		case "issue":
			a.ns++
		}
		if label == "blocker" {
			a.nb++
		}
		if label == "should" {
			a.nh++
		}
		if upd > a.la {
			a.la = upd
		}
	}
	sort.Slice(order, func(i, j int) bool {
		x, _ := strconv.ParseInt(order[i], 10, 64)
		y, _ := strconv.ParseInt(order[j], 10, 64)
		return x < y
	})
	for _, id := range order {
		a := ids[id]
		au := 0
		if a.au {
			au = 1
		}
		lv := a.la
		if lv == "" {
			lv = "-"
		}
		chg := ""
		if len(a.logins) > 1 {
			chg = " login-changed"
		}
		out = append(out, fmt.Sprintf("reviewer: %s id=%s type=%s assoc=%s author=%d reviews=%d inline=%d issue=%d label-blocker=%d label-should=%d last=%s%s",
			a.cur, id, a.ty, a.as, au, a.nr, a.ni, a.ns, a.nb, a.nh, lv, chg))
	}
	return len(last), out, true, nil
}

// PRValid は PR 本体の必須項目(作者の数値 id と head の sha)があるか。
func PRValid(pr map[string]any) bool {
	id, err := get(pr, "user", "id")
	if err != nil {
		return false
	}
	if _, ok := num(id); !ok {
		return false
	}
	sha, err := get(pr, "head", "sha")
	if err != nil {
		return false
	}
	s, ok := sha.(string)
	return ok && re40.MatchString(s)
}
