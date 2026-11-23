package webui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// 部品ごとの確かめ(Python 版の webui/tests/test_webui.py が app/ の中を直接呼んで確かめていたもの)。

// testDB は本物のスキーマの DB を作り、sql を流して、WEBUI_TB_DB に渡す。
func testDB(t *testing.T, sql string) string {
	t.Helper()
	root, _ := filepath.Abs("../../webui")
	t.Setenv("WEBUI_ROOT", root)
	p := filepath.Join(t.TempDir(), "tasks.db")
	db, err := store.Open(p, "default", &store.Options{NoSession: true})
	if err != nil {
		t.Fatal(err)
	}
	if sql != "" {
		if _, err := db.Exec(sql); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	t.Setenv("WEBUI_TB_DB", p)
	cacheMu.Lock()
	cacheM = map[string]cacheEntry{}
	cacheMu.Unlock()
	return p
}

func dbExec(t *testing.T, p, sql string) {
	t.Helper()
	db, err := store.Open(p, "default", &store.Options{NoSession: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(sql); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---- 定期実行: 次回の予定・conf の読み取り・説明

func TestNextRuns(t *testing.T) {
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Skip("tzdata が無い")
	}
	defer func() { nowFn = time.Now }()
	info := func() *jobInfo {
		return &jobInfo{enabled: true, tz: "Asia/Tokyo", intervalSec: 1800, start: "09:00", end: "21:00", days: "weekdays"}
	}
	at := func(d, h, m int, n int) []string {
		nowFn = func() time.Time { return time.Date(2026, 11, d, h, m, 0, 0, tokyo) }
		r, _ := nextRuns(info(), n)
		return r
	}
	if r := at(14, 10, 0, 2); !reflect.DeepEqual(r, []string{"2026-11-16 09:00:00", "2026-11-16 09:30:00"}) {
		t.Errorf("土曜の昼は月曜 09:00 から: %v", r)
	}
	if r := at(16, 9, 10, 1); !reflect.DeepEqual(r, []string{"2026-11-16 09:30:00"}) {
		t.Errorf("平日の 09:10 の次は 09:30: %v", r)
	}
	if r := at(20, 22, 0, 1); !reflect.DeepEqual(r, []string{"2026-11-23 09:00:00"}) {
		t.Errorf("終了後は翌営業日: %v", r)
	}
	off := info()
	off.enabled = false
	if r, _ := nextRuns(off, 5); len(r) != 0 {
		t.Errorf("一時停止は予定なし: %v", r)
	}
	bad := info()
	bad.tz = "No/Such"
	if _, note := nextRuns(bad, 5); !strings.Contains(note, "不明") {
		t.Errorf("不正な TZ は「不明」: %q", note)
	}
}

func TestJobConf(t *testing.T) {
	testDB(t, "")
	jobs := t.TempDir()
	t.Setenv("WEBUI_SCHED_JOBS", jobs)
	t.Setenv("SCHED_FILE_FALLBACK", "1")
	v := newSV("default")
	defer v.close()
	defs := func(body string) *jobDefsT {
		write(t, filepath.Join(jobs, "unit.conf"), body)
		d, ok := v.jobDefs("unit")
		if !ok {
			t.Fatal("読めない")
		}
		return d
	}
	for _, q := range []string{"'", `"`} {
		if d := defs("PROMPT_FILE=" + q + "/repo/scheduler/body.txt" + q + "#v2\n"); !d.complex["PROMPT_FILE"] {
			t.Errorf("%s の閉じ引用符の直後の # はコメントにしない(連結として不完全): %v", q, d)
		}
		if d := defs("PROMPT_FILE=" + q + "/repo/scheduler/body.txt" + q + " # note\n"); d.complex["PROMPT_FILE"] || d.get("PROMPT_FILE") != "/repo/scheduler/body.txt" {
			t.Errorf("%s の閉じ引用符のあとに空白を置いた # はコメント(完全): %v", q, d)
		}
	}
	if d := defs("PROMPT='説明\nPROMPT_FILE=/repo/scheduler/body.txt\n終わり'\n"); d.vals["PROMPT_FILE"] != "" || !strings.Contains(d.get("PROMPT"), "PROMPT_FILE=/repo/scheduler/body.txt") {
		t.Errorf("複数行 PROMPT の中の PROMPT_FILE= は代入として読まない: %v", d)
	}
	if d := defs("PROMPT=\"説明\nCOMMAND=rm -rf x\n終わり\"\n"); hasKey(d.vals, "COMMAND") {
		t.Errorf("二重引用符の複数行の中の COMMAND= も代入として読まない: %v", d)
	}
	if d := defs("PROMPT=abc\\\nPROMPT_FILE=/x/y\nSKIP_IF=working\n"); hasKey(d.vals, "PROMPT_FILE") || d.get("SKIP_IF") != "working" {
		t.Errorf("行末 \\ の継続行の中の代入は読まず、引用符の外の次の代入は読む: %v", d)
	}
	if d := defs("PROMPT='a'\nPROMPT_FILE=/repo/scheduler/b.txt\n"); d.get("PROMPT_FILE") != "/repo/scheduler/b.txt" {
		t.Errorf("引用符が閉じたあとの行の代入は読む: %v", d)
	}
	// 説明(conf の先頭コメント)と、COMMAND からの推定
	defs("# 1 行目の説明\n# 2 行目\n# 注意: COMMAND は bash -c でそのまま実行される\n# token=ghp_abcdefghijklmnopqrstuvwxyz0123\nCOMMAND='cd /x && bin/tb flush'\n# 後ろのコメントは説明に入れない\n")
	if ab := v.jobAbout("unit"); len(ab) != 3 || ab[0] != "1 行目の説明" || ab[1] != "2 行目" || strings.Contains(ab[2], "ghp_") {
		t.Errorf("説明: 先頭のコメントだけ、注意書きは省き、トークンはマスク: %q", ab)
	}
	if s := v.jobSummary("unit", &jobInfo{kind: "command"}); s != "1 行目の説明" {
		t.Errorf("一覧の 1 行は先頭コメントの 1 行目: %q", s)
	}
	defs("# 鍵の例\n# -----BEGIN RSA PRIVATE KEY-----\n# MIIEowIBAAKCAQEAsecretbody1234567890\n# -----END RSA PRIVATE KEY-----\nCOMMAND=true\n")
	if ab := strings.Join(v.jobAbout("unit"), " "); strings.Contains(ab, "MIIEow") || !strings.Contains(ab, "[マスク]") {
		t.Errorf("行をまたぐ秘密鍵のブロックは中身ごと伏せる: %q", ab)
	}
	labels := func(body string) []string {
		var out []string
		for _, c := range commandSummary(defs(body).get("COMMAND")) {
			out = append(out, c[0])
		}
		return out
	}
	if l := labels("COMMAND='cd /x && HERDR_SESSION=default bin/tb notify flush; bin/tb digest'\n"); !reflect.DeepEqual(l, []string{"tb notify flush", "tb digest"}) ||
		!strings.Contains(v.jobSummary("unit", &jobInfo{kind: "command"}), "通知") {
		t.Errorf("コメントが無ければ tb のサブコマンドから推す(notify flush は flush と取り違えない): %v", l)
	}
	for body, want := range map[string][]string{
		"COMMAND='cd /x && bin/tb message flush; bin/tb flush'\n":                                          {"tb message flush"},
		"COMMAND='cd /x && bin/tb review pack --mark; bin/tb review-pack'\n":                               {"tb review pack"},
		"COMMAND='cd /x && bin/tb pr watch; bin/tb pr-watch; bin/tb pr queue scan; bin/tb pr-queue run'\n": {"tb pr watch", "tb pr queue scan", "tb pr queue run"},
	} {
		if l := labels(body); !reflect.DeepEqual(l, want) {
			t.Errorf("新しい名前と古い名前は同じ説明: %s → %v", body, l)
		}
	}
}

func hasKey(m map[string]string, k string) bool { _, ok := m[k]; return ok }

// 既定は DB だけ(scheduler と同じ): SCHED_FILE_FALLBACK が無ければ jobs/*.conf のジョブは一覧に出さず、「未取り込み」として知らせる。
func TestJobsDBOnly(t *testing.T) {
	testDB(t, "")
	jobs := t.TempDir()
	t.Setenv("WEBUI_SCHED_JOBS", jobs)
	write(t, filepath.Join(jobs, "job1.conf"), "COMMAND=true\n")
	t.Setenv("SCHED_FILE_FALLBACK", "")
	v := newSV("default")
	defer v.close()
	if contains(v.jobNames(), "job1") || !contains(v.unimportedJobs(), "job1") {
		t.Errorf("既定: ファイルだけのジョブは一覧に出さず、未取り込みとして出す: %v %v", v.jobNames(), v.unimportedJobs())
	}
	if _, ok := v.jobText("job1", 1000); ok {
		t.Error("既定: ファイルの定義は読まない")
	}
	t.Setenv("SCHED_FILE_FALLBACK", "1")
	if _, ok := v.jobText("job1", 1000); !contains(v.jobNames(), "job1") || !ok {
		t.Error("SCHED_FILE_FALLBACK=1: ファイルのジョブも出す")
	}
}

// ---- 権限カタログ

func TestCatalogLoad(t *testing.T) {
	cf := filepath.Join(t.TempDir(), "catalog.conf")
	write(t, cf, "# 注釈\n"+
		"send:task:*   | send | local-write | high | conditional | active | 説明 <script>alert(1)</script> \x01制御 | 縦線を含む説明\n"+
		"send:info:*   | send | local-write | mid | yes | active | 正常な行\n"+
		"agent:new:x   | role | local-write | mid | yes | active | 種別がパターンの先頭と違う\n"+
		"setup:hooks   | setup | local-write | human-only | yes | active | human-only なのに allow 候補が yes\n"+
		"role:change   | role | local-write | high | conditional | active | 対象の無い操作名が conditional\n"+
		"send:info:*   | send | local-write | mid | yes | active | 重複\n"+
		"gate:merge    | gate | external-write | human-only | no | bogus | 状態が不正\n"+
		"gate:x | gate | none | low | no\n")
	old := catalogPath
	defer func() { catalogPath = old }()
	catalogPath = func() string { return cf }
	cat, cerr, cbad := loadCatalog()
	if len(cat) != 2 || cat[0].pattern != "send:task:*" || cat[1].pattern != "send:info:*" || cbad != 6 || cerr != "" {
		t.Fatalf("正しい 2 行だけ読み、不正な行(種別・human-only・conditional・重複・状態・項目不足)を数える: %v %d %q", cat, cbad, cerr)
	}
	if !strings.Contains(cat[0].desc, "縦線を含む説明") || strings.Contains(cat[0].desc, "\x01") {
		t.Errorf("説明の | は残り、制御文字は除かれる: %q", cat[0].desc)
	}
	s := &Server{}
	html := s.catalogSection(pageCtx{opts: &pageOpts{}}, &perms{v: newSV("default")}, "", cat, "", cbad)
	if strings.Contains(html, "<script>") || !strings.Contains(html, "&lt;script&gt;") || !strings.Contains(html, "6 件") {
		t.Errorf("説明のタグはエスケープされ、不正な行のスキップ件数が出る: %s", html)
	}
	catalogPath = func() string { return filepath.Join(t.TempDir(), "none.conf") }
	_, cerr, _ = loadCatalog()
	if cerr == "" || !strings.Contains(s.catalogSection(pageCtx{opts: &pageOpts{}}, nil, "", nil, cerr, 0), "読めません") {
		t.Errorf("ファイルが無ければエラー文を返す: %q", cerr)
	}
}

// ---- ログイン: 再設定の許可の期限、複数のプロセスでの共有

func TestResetWindow(t *testing.T) {
	a, err := NewAuth(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if a.ResetState() != "missing" {
		t.Error("許可なしは missing")
	}
	if err := os.WriteFile(a.AllowPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	age := func(sec int) string {
		at := time.Now().Add(-time.Duration(sec) * time.Second)
		if err := os.Chtimes(a.AllowPath, at, at); err != nil {
			t.Fatal(err)
		}
		return a.ResetState()
	}
	if s := age(540); s != "ok" {
		t.Errorf("9 分後は ok: %s", s)
	}
	if s := age(ResetWindow - 2); s != "ok" {
		t.Errorf("10 分の直前は ok: %s", s)
	}
	if s := age(ResetWindow + 2); s != "expired" {
		t.Errorf("10 分を過ぎたら expired: %s", s)
	}
	if s := age(-3600); s != "invalid" {
		t.Errorf("作成より未来すぎる時刻は invalid: %s", s)
	}
}

func TestAuthShared(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("権限の確かめが posix")
	}
	t.Setenv("WEBUI_FAIL_DELAY", "0")
	d := filepath.Join(t.TempDir(), "shared")
	a1, err := NewAuth(d)
	if err != nil {
		t.Fatal(err)
	}
	a2, _ := NewAuth(d)
	a1.Setup("u", "long-enough-pass-1")
	t.Setenv("WEBUI_LOCK_MAX", "3")
	for i := range 3 {
		[]*Auth{a1, a2}[i%2].Attempt("u", "wrong-wrong-wrong")
	}
	if a1.LockedFor() <= 0 || a2.LockedFor() <= 0 {
		t.Error("失敗回数は置き場で共有(別のプロセスで数えてもロックする)")
	}
	if st, _ := os.Stat(filepath.Join(d, "lockout.json")); st == nil || st.Mode().Perm() != 0o600 {
		t.Error("ロックの記録は 0600")
	}
	if st, _ := os.Stat(d); st.Mode().Perm() != 0o700 {
		t.Error("置き場は 0700")
	}
	t1, _ := a1.CreateSession()
	t2, _ := a2.CreateSession()
	if a2.SessionHash(t1) == "" || a1.SessionHash(t2) == "" {
		t.Error("別のプロセスで作ったセッションも有効(書き込みが互いを消さない)")
	}
	a2.DestroySession(t1)
	if a1.SessionHash(t1) != "" || a1.SessionHash(t2) == "" {
		t.Error("別のプロセスでのログアウトも効く")
	}
}

// ---- 表示にセッション名の生の HTML 特殊文字が出ない(多層防御。URL の名前は SessionRE で絞っている)

func TestSessionNameEscaped(t *testing.T) {
	testDB(t, "")
	n := "a<b>&\"x"
	if l := instLabel(n); strings.Contains(l, "<b>") || !strings.Contains(l, "&lt;b&gt;") {
		t.Errorf("ヘッダ: %s", l)
	}
	s := &Server{}
	if p := s.page(&rq{name: n}, "t", "", "/", "", pageOpts{}); strings.Contains(p, "<b>") {
		t.Error("タイトル")
	}
}

// ---- セッションの一覧: DB の行・herdr のディレクトリ・default・プロセスのセッション。'*' や不正な名前は出さない

func TestKnownSessions(t *testing.T) {
	testDB(t, "INSERT INTO tasks (id,project,title,status,created_at,updated_at,herdr_session) VALUES ('T-b-1','b','x','todo','t','t','beta');"+
		"INSERT INTO config (herdr_session,key,body,updated_by,updated_at) VALUES ('*','projects','x','human','t');")
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HERDR_SESSION", "zeta")
	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("HERDR_SESSION_FILE", filepath.Join(xdg, "none"))
	for _, d := range []string{"gamma", "bad name"} {
		os.MkdirAll(filepath.Join(xdg, "herdr", "sessions", d), 0o755)
	}
	write(t, filepath.Join(xdg, "herdr", "sessions", "afile"), "")
	ForgetSessions()
	defer ForgetSessions()
	if got := KnownSessions(); !reflect.DeepEqual(got, []string{"beta", "default", "gamma", "zeta"}) {
		t.Errorf("一覧(DB の beta と default、ディレクトリの gamma、プロセスの zeta): %v", got)
	}
	for _, n := range []string{"nope", "*", "bad name", "afile"} {
		if IsKnownSession(n) {
			t.Errorf("一覧に無い名前・不正な名前は通さない: %q", n)
		}
	}
	if !IsKnownSession("gamma") {
		t.Error("gamma")
	}
}

// ---- /docs から run/ とログイン情報の置き場を読めない

func TestDocsDenied(t *testing.T) {
	tmp := t.TempDir()
	rb := filepath.Join(tmp, "docs-runbase")
	for _, sub := range []string{"", "instance-other"} {
		write(t, filepath.Join(rb, sub, "notes.txt"), "x")
	}
	t.Setenv("WEBUI_RUN_BASE", rb)
	t.Setenv("WEBUI_RUN_DIR", "")
	t.Setenv("WEBUI_STATE_DIR", filepath.Join(tmp, "other-state"))
	t.Setenv("WEBUI_AUTH_DIR", "")
	real := func(p string) string { r, _ := filepath.EvalSymlinks(p); return r }
	if denied(real(filepath.Join(rb, "notes.txt"))) == "" {
		t.Error("run/ 配下は開かない")
	}
	if denied(filepath.Join(real(filepath.Join(rb, "instance-other")), "auth.json")) == "" {
		t.Error("旧版の run/instance-<名>/ も開かない")
	}
	alias := filepath.Join(tmp, "docs-run-alias")
	if err := os.Symlink(rb, alias); err == nil {
		if denied(filepath.Join(alias, "instance-other", "auth.json")) == "" || denied(filepath.Join(alias, "instance-other", "nope.json")) == "" {
			t.Error("run への symlink 経由(存在しないファイルでも)開かない")
		}
	}
	upper := filepath.Join(tmp, "DOCS-RUNBASE")
	if _, err := os.Stat(upper); err == nil { // 大文字小文字を区別しないボリューム(macOS の既定)
		if denied(filepath.Join(upper, "instance-other", "auth.json")) == "" {
			t.Error("綴りを変えた run(大文字小文字違い)でも開かない")
		}
		os.MkdirAll(filepath.Join(tmp, "docs-case", ".ssh"), 0o755)
		if denied(filepath.Join(tmp, "docs-case", ".SSH", "x")) == "" {
			t.Error("綴りを変えた .SSH も開かない")
		}
	}
	if r := denied(filepath.Join(tmp, "no-such-dir", "a.md")); r != "" {
		t.Errorf("無関係な存在しないパスは開く判定: %s", r)
	}
	if r := denied(filepath.Join(tmp, "docs-runbase-x", "a.md")); r != "" {
		t.Errorf("run の隣の別ディレクトリ(前方一致だけ同じ名前)は denied でない: %s", r)
	}
	ex := filepath.Join(tmp, "docs-explicit-run")
	os.MkdirAll(ex, 0o755)
	t.Setenv("WEBUI_RUN_DIR", ex)
	if denied(filepath.Join(real(ex), "auth.json")) == "" {
		t.Error("WEBUI_RUN_DIR 明示でも、その場所は開かない")
	}
	sd := filepath.Join(tmp, "docs-state")
	os.MkdirAll(sd, 0o755)
	t.Setenv("WEBUI_STATE_DIR", sd)
	if denied(filepath.Join(sd, "auth.json")) == "" || denied(filepath.Join(sd, "gateway.json")) == "" {
		t.Error("共通のログイン情報・記録(ホームの置き場)は開かない")
	}
}

// ---- 関係グラフ(tb agents --graph と同じ SQL とまとめ方)と規則の @<役割>

func TestGraphAndRoles(t *testing.T) {
	testDB(t, "")
	rd := t.TempDir()
	for _, r := range []string{"conductor", "worker", "watcher"} {
		write(t, filepath.Join(rd, r+".md"), "x")
	}
	write(t, filepath.Join(rd, "map.conf"), "*-conductor = conductor\nworker* = worker\nwatch* = watcher\n")
	pc := filepath.Join(t.TempDir(), "projects.conf")
	write(t, pc, "# c\nprojb = b-conductor\n")
	t.Setenv("TB_ROLES", rd)
	t.Setenv("TB_CONFIG", pc)
	v := newSV("default")
	defer v.close()
	var got []string
	for _, c := range [][2]string{{"conductor", ""}, {"b-conductor", ""}, {"worker1", ""}, {"helper", "worker"}, {"helper", ""}, {"_x", ""}} {
		got = append(got, v.roleOf(c[0], c[1]))
	}
	if !reflect.DeepEqual(got, []string{"conductor", "conductor", "worker", "worker", "", ""}) {
		t.Errorf("役割は名前の .md > 台帳の role > map.conf: %v", got)
	}
	rows := []row{
		{"verb": "lead", "src": nil, "dst": "worker2", "project": "projb", "status": "ok", "n": int64(1), "last_at": "a"},
		{"verb": "lead", "src": nil, "dst": "worker2", "project": "proja", "status": "ok", "n": int64(1), "last_at": "b"},
		{"verb": "send:task", "src": "conductor", "dst": "worker1", "project": nil, "status": "ok", "n": int64(2), "last_at": "c"},
		{"verb": "report", "src": "worker2", "dst": nil, "project": "projb", "status": "ok", "n": int64(1), "last_at": "d"},
		{"verb": "spawn", "src": "worker1", "dst": "worker1", "project": nil, "status": "ok", "n": int64(1), "last_at": "e"},
	}
	es := v.edges(rows)
	var flat []string
	for _, e := range es {
		flat = append(flat, e.from+" "+e.verb+" "+e.to)
	}
	if !reflect.DeepEqual(flat, []string{"b-conductor lead worker2", "conductor send:task worker1", "conductor lead worker2", "worker2 report b-conductor"}) {
		t.Errorf("project の端は指揮官で埋め、自分への辺は捨てる(1 体に複数の上流): %v", flat)
	}
	var ns []gnode
	for _, n := range []string{"conductor", "b-conductor", "worker1", "worker2", "<b>x</b>"} {
		ns = append(ns, gnode{name: n, role: v.roleOf(n, "")})
	}
	mm := mermaidGraph(ns, append(es, &edge{verb: "send:task", from: "worker1", to: "worker2", status: "denied", n: 1}))
	for _, w := range []string{`subgraph g0["conductor"]`, `subgraph g2["役割なし"]`, "-- 依頼 x2 -->", "-. 依頼 拒否 .->"} {
		if !strings.Contains(mm, w) {
			t.Errorf("ロールごとの subgraph とラベルつきの辺(拒否は点線)に %q が無い:\n%s", w, mm)
		}
	}
	if strings.Contains(mm, "<b>") {
		t.Error("名前のタグは出さない")
	}
	var ms []bool
	for _, c := range [][2]string{{"worker1", "@worker"}, {"worker1", "@cond*"}, {"x", "@worker"}, {"x", "x"}} {
		ms = append(ms, v.nameMatches(c[0], c[1], ""))
	}
	if !reflect.DeepEqual(ms, []bool{true, false, false, true}) {
		t.Errorf("規則の @役割: %v", ms)
	}
	write(t, filepath.Join(rd, "manager.md"), "x")
	write(t, filepath.Join(rd, "hr.md"), "x")
	ms = nil
	for _, p := range []string{"@hr", "@hr+manager", "@hr+worker", "@manag*"} {
		ms = append(ms, v.nameMatches("helper", p, "hr,manager"))
	}
	if v.roleOf("helper", "hr, manager") != "hr,manager" || !reflect.DeepEqual(ms, []bool{true, true, false, true}) {
		t.Errorf("複数ロール(台帳はカンマ区切り)と @a+b: %v", ms)
	}
	// ロールは herdr セッション固有(このセッションの roles/sessions/<s>/ があればそこだけ)
	write(t, filepath.Join(rd, "sessions", "s2", "auditor.md"), "a")
	write(t, filepath.Join(rd, "sessions", "s2", "map.conf"), "worker* = auditor\n")
	in := func(s, name, reg string) string { w := newSV(s); defer w.close(); return w.roleOf(name, reg) }
	if got := []string{in("default", "worker1", ""), in("s2", "worker1", ""), in("s2", "worker1", "worker"), in("s9", "worker1", "")}; !reflect.DeepEqual(got, []string{"worker", "auditor", "auditor", "worker"}) {
		t.Errorf("セッション固有のロール: %v", got)
	}
	if w := newSV("s2"); !w.nameMatches("worker1", "@auditor", "") {
		t.Error("s2 では worker1 は @auditor")
	}
}

func rulesOf(lines ...string) []*rule {
	var out []*rule
	for i, ln := range lines {
		lhs, val, _ := strings.Cut(ln, "=")
		f := strings.Fields(lhs)
		raw := strings.Join(f, " ")
		out = append(out, &rule{no: i + 1, subject: f[0], op: f[1], target: f[2], effect: strings.TrimSpace(val), raw: raw, pattern: raw})
	}
	return out
}

func TestDecideRoles(t *testing.T) {
	root, _ := filepath.Abs("../../webui")
	t.Setenv("WEBUI_ROOT", root)
	rd := t.TempDir()
	for _, r := range []string{"conductor", "worker"} {
		write(t, filepath.Join(rd, r+".md"), "x")
	}
	write(t, filepath.Join(rd, "map.conf"), "worker* = worker\n")
	t.Setenv("TB_ROLES", rd)
	t.Setenv("TB_CONFIG", filepath.Join(rd, "none.conf"))
	// 台帳は読めるが行が無い
	testDB(t, "")
	v := newSV("default")
	defer v.close()
	var got []string
	for _, op := range []string{"send:task:worker1", "send:task:conductor", "role:change"} {
		p := &perms{v: v, rules: rulesOf("* send:* @worker = allow"), ops: loadOps()}
		e, _ := p.decide(op)
		got = append(got, e)
	}
	if !reflect.DeepEqual(got, []string{"allow", "ask", "ask"}) {
		t.Errorf("対象 @役割 の行を役割で照合(当たらなければ既定の表): %v", got)
	}
	one := func(v *sv, op string, lines ...string) string {
		p := &perms{v: v, rules: rulesOf(lines...), ops: loadOps()}
		e, _ := p.decide(op)
		return e
	}
	if a, b := one(v, "send:task:worker1", "* send:task * = allow", "* send:task @worker = deny"), one(v, "send:task:worker1", "* send:task @worker = deny", "* send:task * = allow"); a != "deny" || b != "deny" {
		t.Errorf("いちばん厳しい行が勝つ(行の順番は関係ない): %s %s", a, b)
	}
	if e := one(v, "send:task:worker1", "* send:task @!(conductor) = ask", "* send:task * = allow"); e != "unknown" {
		t.Errorf("拡張 glob の行(!(...) など)は判定できないとして止まる: %s", e)
	}
	// 台帳を読めない
	t.Setenv("WEBUI_TB_DB", filepath.Join(rd, "none.db"))
	nv := newSV("default")
	defer nv.close()
	got = []string{one(nv, "send:task:worker1", "* send:* @worker = allow"), one(nv, "send:task:conductor", "* send:* @conductor = allow"),
		one(nv, "send:task:worker1", "* send:task @worker = deny", "* send:task * = allow")}
	if !reflect.DeepEqual(got, []string{"ask", "allow", "deny"}) {
		t.Errorf("台帳を読めないときは @役割 を map.conf で一致させない(名前の役割ファイルは使う。確かめられない行は締める向きだけ): %v", got)
	}
}

// ---- 待ち受けたポートの記録: 形式どおりのものだけ、書いたプロセスが居なければ動いていない扱い

func TestGatewayRecord(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("権限の確かめが posix")
	}
	d := filepath.Join(t.TempDir(), "state")
	os.MkdirAll(d, 0o700)
	t.Setenv("WEBUI_STATE_DIR", d)
	gp := filepath.Join(d, "gateway.json")
	for _, x := range []any{map[string]any{"port": "80", "pid": os.Getpid()}, map[string]any{"port": true, "pid": os.Getpid()},
		map[string]any{"port": 80, "pid": 0}, map[string]any{"port": 80, "pid": -1}, map[string]any{"port": 80, "pid": 1<<22 + 12345}, []int{1}} {
		b, _ := json.Marshal(x)
		os.WriteFile(gp, b, 0o600)
		if g := ReadGateway(true); g != nil {
			t.Errorf("形式違い・pid が 0 や負・居ないプロセスの記録は動いていない扱い: %s", b)
		}
	}
	if pidAlive(0) || pidAlive(-1) {
		t.Error("pid が 0 や負なら生きている扱いにしない")
	}
	os.WriteFile(gp, []byte(`{"port": 80, "pid": 1}`), 0o600)
	ClearGateway()
	if !isFile(gp) {
		t.Error("他のプロセスの記録は消さない")
	}
	if err := WriteGateway(80); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(gp); st.Mode().Perm() != 0o600 {
		t.Error("自分の記録は 600 で書く")
	}
	ClearGateway()
	if isFile(gp) {
		t.Error("自分の記録は消せる")
	}
}

// ---- DB は全セッション共有。各セッションの画面は自分のセッションの行だけを読む(一時ビュー)

func TestSessionViews(t *testing.T) {
	if !contains(store.SessionTables(), "tasks") || !contains(store.SessionTables(), "config") {
		t.Error("一時ビューを作る表は store.SessionTables(tb と同じ一覧)")
	}
	p := testDB(t, "INSERT INTO tasks (id,project,title,status,created_at,updated_at,herdr_session) VALUES "+
		"('T-1','pj','d','in_progress','t','t','default'), ('T-o-001','o','other','todo','t','t','other');"+
		"INSERT INTO comments (task_id,kind,text,from_name,from_pane,from_session,created_at,herdr_session) VALUES "+
		"('T-1','question','q','x','p','s','t','default'), ('T-o-001','question','other q','x','p','s','t','other');")
	ids := func(s string) []string {
		v := newSV(s)
		defer v.close()
		var out []string
		for _, r := range v.q("SELECT id FROM tasks ORDER BY id") {
			out = append(out, pyStr(r["id"]))
		}
		return out
	}
	if got := ids("default"); !reflect.DeepEqual(got, []string{"T-1"}) {
		t.Errorf("default の画面に別のセッションの行は出ない: %v", got)
	}
	if got := ids("other"); !reflect.DeepEqual(got, []string{"T-o-001"}) {
		t.Errorf("other の画面は other の行だけ: %v", got)
	}
	v := newSV("default")
	if n := intOf(v.q("SELECT COUNT(*) AS n FROM comments c JOIN tasks t ON t.id=c.task_id")[0]["n"]); n != 1 {
		t.Errorf("JOIN もこのセッションだけ: %d", n)
	}
	if _, ok := v.qChecked("INSERT INTO tasks (id,project,title,created_at,updated_at) VALUES ('x','x','x','t','t') RETURNING id"); ok {
		t.Error("読み取り専用のまま(書き込めない)")
	}
	v.close()
	// herdr の結果のキャッシュはセッションごと
	var calls []string
	for _, s := range []string{"sa", "sb", "sa"} {
		cached(s, "t-key", func() string { calls = append(calls, s); return s })
	}
	if !reflect.DeepEqual(calls, []string{"sa", "sb"}) {
		t.Errorf("キャッシュはセッションごと: %v", calls)
	}
	// 子プロセスには文脈のセッションを HERDR_SESSION で渡し、socket のパスは外す
	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("HERDR_SESSION", "other")
	t.Setenv("HERDR_SESSION_FILE", filepath.Join(t.TempDir(), "none"))
	t.Setenv("HERDR_SOCKET_PATH", "/h/.config/herdr/sessions/other/herdr.sock")
	env := strings.Join(childEnv("default"), "\n") + "\n"
	if !strings.Contains(env, "HERDR_SESSION=default\n") || strings.Contains(env, "HERDR_SOCKET_PATH=") {
		t.Errorf("子プロセスの環境: %s", env)
	}
	if !reflect.DeepEqual(childEnv("other"), os.Environ()) {
		t.Error("プロセスと同じセッションなら環境はそのまま")
	}
	// 設定(config 表): このセッションの行 → 共通の行(*) → checkout のファイル。環境変数で明示したファイルが最優先
	dbExec(t, p, "INSERT INTO config (herdr_session,key,body,updated_by,updated_at) VALUES ('*','projects','pj = shared-cond','human','t'),"+
		"('sa','projects','pj = sa-cond','human','t'), ('*','policy','* send:* * = allow','human','t'),"+
		"('sa','role:zrole','z の役割','human','t'), ('*','role-map','z* = zrole','human','t')")
	t.Setenv("TB_CONFIG", "")
	t.Setenv("WEBUI_POLICY", "")
	t.Setenv("TB_POLICY", "")
	t.Setenv("TB_ROLES", t.TempDir())
	cfg := func(s string) (string, string) {
		v := newSV(s)
		defer v.close()
		_, src, _ := v.configText("projects")
		return v.targetFor("pj"), src
	}
	if a, as := cfg("sa"); a != "sa-cond" || as != "db:sa" {
		t.Errorf("projects はこのセッションの行: %s %s", a, as)
	}
	if b, bs := cfg("sb"); b != "shared-cond" || bs != "db:*" {
		t.Errorf("無ければ共通の行: %s %s", b, bs)
	}
	sb := newSV("sb")
	if txt, src, _ := sb.configText("policy"); txt != "* send:* * = allow" || src != "db:*" {
		t.Errorf("policy も DB から: %q %s", txt, src)
	}
	sb.close()
	tc := filepath.Join(t.TempDir(), "explicit-projects.conf")
	write(t, tc, "pj = file-cond\n")
	t.Setenv("TB_CONFIG", tc)
	if a, _ := cfg("sa"); a != "file-cond" {
		t.Errorf("環境変数で明示したファイルが DB より先: %s", a)
	}
	write(t, tc, "pj = file-cond\n* = star-lead\n")
	sa := newSV("sa")
	if sa.targetFor("pj") != "file-cond" || sa.targetFor("other") != "star-lead" {
		t.Error("projects の * の行は既定の宛先で、project の行が優先(core.sh と同じ)")
	}
	// 役割の定義も DB から(role:<名前> / role-map)。このセッションの行 > 共通の行 > roles/ のファイル
	if sa.roleOf("z1", "") != "zrole" || !contains(sa.roleNames(), "zrole") {
		t.Errorf("DB の役割と対応(sa): %q %v", sa.roleOf("z1", ""), sa.roleNames())
	}
	sa.close()
	sb = newSV("sb")
	if sb.roleOf("z1", "") != "" || contains(sb.roleNames(), "zrole") {
		t.Error("ほかのセッションの役割の行は効かない")
	}
	sb.close()
}

// ---- チケットの状態は設定 statuses から読む(tb の lib/core.sh と同じ。名前ではなく分類で判断する)

func TestStatuses(t *testing.T) {
	t.Setenv("TB_STATUSES", "")
	t.Setenv("TB_CONFIG_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "default")
	t.Setenv("HERDR_SOCKET_PATH", "")
	p := testDB(t, "INSERT INTO tasks (id,project,title,status,owner_agent,created_at,updated_at,herdr_session) VALUES "+
		"('T-1','pj','題','in_progress','alpha','2026-11-06T00:00:00Z','2026-11-06T00:00:00Z','default')")
	v := newSV("default")
	if !reflect.DeepEqual(v.statusNames(), []string{"todo", "in_progress", "review", "done", "blocked", "cancelled"}) ||
		!reflect.DeepEqual(v.boardColumns(), []string{"todo", "in_progress", "review", "blocked", "done", "cancelled"}) {
		t.Errorf("設定が無ければ組み込みの 6 つ: %v %v", v.statusNames(), v.boardColumns())
	}
	v.close()
	dbExec(t, p, "INSERT INTO config (herdr_session,key,body,updated_by,updated_at) VALUES "+
		"('*','statuses','backlog todo = 未着手\ndoing active\nshipped done\nqa review\ndropped cancelled  # 取り消し','human','t');"+
		"INSERT INTO tasks (id,project,title,status,owner_agent,created_at,updated_at,herdr_session) VALUES "+
		"('T-s-001','s','作業中','doing','alpha','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','default'),"+
		"('T-s-002','s','期限切れ','backlog',NULL,'t','t','default'),('T-s-003','s','閉じた','shipped',NULL,'t','t','default');"+
		"UPDATE tasks SET due='2000-01-01' WHERE id IN ('T-s-002','T-s-003');"+
		"INSERT INTO agents (herdr_session,name,state,created_by,created_at,updated_at) VALUES ('default','alpha','running','human','t','t');")
	v = newSV("default")
	defer func() { v.close() }()
	if got := v.statusNames(); !reflect.DeepEqual(got, []string{"backlog", "doing", "shipped", "qa", "dropped"}) {
		t.Errorf("設定の順の一覧: %v", got)
	}
	if got := v.boardColumns(); !reflect.DeepEqual(got, []string{"backlog", "doing", "qa", "shipped", "dropped"}) {
		t.Errorf("ボードは分類の順: %v", got)
	}
	if got := v.stNames("active"); !reflect.DeepEqual(got, []string{"doing", "in_progress"}) || v.stMain("blocked") != "blocked" {
		t.Errorf("組み込みの名前も分類で数える: %v %s", got, v.stMain("blocked"))
	}
	if got, _ := v.stq("x IN (__CLOSED_STATUSES__)"); got != "x IN ('shipped','dropped','done','cancelled')" {
		t.Errorf("SQL の置き場を埋める: %s", got)
	}
	now := time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)
	if _, ok := v.taskStale(row{"status": "doing", "updated_at": "2026-01-01T00:00:00Z"}, now); !ok {
		t.Error("作業中の分類で停滞を判断")
	}
	if _, ok := v.taskStale(row{"status": "backlog", "updated_at": "2026-01-01T00:00:00Z"}, now); ok {
		t.Error("作業中でなければ停滞にしない")
	}
	if v.dueState(row{"status": "shipped", "due": "2000-01-01"}, "2026-03-04", 3) != "" || v.dueState(row{"status": "backlog", "due": "2000-01-01"}, "2026-03-04", 3) != "overdue" {
		t.Error("閉じた分類は期限切れにしない")
	}
	if r := v.agentRegistry()["alpha"]; r == nil || intOf(r["open_tasks"]) != 2 {
		t.Errorf("agent の未完了タスクも分類で数える(T-1 の in_progress と doing): %v", r)
	}
	s := &Server{}
	tasks := func(qs map[string][]string) string {
		q := &rq{name: "default"}
		defer func() { q.v.close() }()
		_, b := s.pageTasks(q, pageCtx{opts: &pageOpts{}, qs: qs})
		return b
	}
	if b := tasks(nil); !strings.Contains(b, `name=status value="backlog">`) || !strings.Contains(b, "dropped 0") || !strings.Contains(b, "in_progress 1") {
		t.Errorf("タスク一覧の選択肢と件数は設定の状態: %.400s", b)
	}
	if b := tasks(map[string][]string{"status": {"todo"}}); !strings.Contains(b, "状態は backlog / doing / shipped / qa / dropped のどれかです") {
		t.Error("設定にもチケットにも無い状態での絞り込みは断る")
	}
	b := tasks(map[string][]string{"status": {"in_progress"}})
	if strings.Contains(b, "状態は") || !strings.Contains(b, "T-1") || strings.Contains(b, "T-s-001") {
		t.Error("設定から外した状態のチケットが残っていれば絞り込める")
	}
	if !strings.Contains(b, `name=status value="in_progress" checked>`) {
		t.Error("残った状態も選択肢に出て、選んだまま再送信できる")
	}
	if b := tasks(map[string][]string{"due": {"overdue"}}); !strings.Contains(b, "T-s-002") || strings.Contains(b, "T-s-003") {
		t.Error("期限切れは閉じた分類を数えない")
	}
	dbExec(t, p, "UPDATE config SET body='broken' WHERE key='statuses'")
	v.close()
	v = newSV("default")
	if v.statusNames()[0] != "todo" {
		t.Error("不正な本文なら組み込みに戻る")
	}
}
