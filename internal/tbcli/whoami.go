package tbcli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/env"
)

// tb whoami: tb が今どこを向いているか(checkout・DB・herdr セッション・launchd ラベル・agent のペインか)と、それぞれの決まり方を出す
// (bash の cmd_whoami)。読み取りだけ。DB を開かず、herdr を呼ばず、ファイルを書かない。呼び出し元の確かめと権限の門の前に答える
// (壊れた DB でも今の向き先を確かめられる)。bin/tb は HERDR_SESSION / HERDR_SOCKET_PATH を書き換えずに渡す。

var (
	reWhoNamed = regexp.MustCompile(`/sessions/([A-Za-z0-9_][A-Za-z0-9._-]*)/herdr\.sock$`)
	reWhoDflt  = regexp.MustCompile(`/herdr/herdr\.sock$`)
	reWhoName  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$`)
	reWhoInst  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// whoamiRoot は checkout(bash の HERDR_ROOT)。bin/tb が渡す HERDR_ROOT、無ければ TB_ROOT の 1 つ上
func whoamiRoot() string {
	if r := os.Getenv("HERDR_ROOT"); r != "" {
		return r
	}
	if t := os.Getenv("TB_ROOT"); t != "" {
		return filepath.Dir(filepath.Clean(t))
	}
	wd, _ := os.Getwd()
	return wd
}

// whoamiSessionFile は bash の herdr_session_file_value(ファイルの 1 行目の名前。使えない文字なら無い)
func whoamiSessionFile(root string) (path, name string, ok bool) {
	path = os.Getenv("HERDR_SESSION_FILE")
	if path == "" {
		path = filepath.Join(root, "herdr-session")
	}
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return path, "", false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return path, "", false
	}
	line, _, _ := strings.Cut(string(b), "\n")
	if line == "" {
		return path, "", false
	}
	line, _, _ = strings.Cut(line, "#")
	line = strings.TrimSpace(line)
	if !reWhoName.MatchString(line) {
		return path, "", false
	}
	return path, line, true
}

// whoamiSession は bash の herdr_session_name と herdr_session_source(socket / env / file / default)
func whoamiSession(root string) (name, src string) {
	p := os.Getenv("HERDR_SOCKET_PATH")
	if m := reWhoNamed.FindStringSubmatch(p); m != nil {
		return m[1], "socket"
	}
	if reWhoDflt.MatchString(p) {
		return "default", "socket"
	}
	if s := os.Getenv("HERDR_SESSION"); s != "" {
		if os.Getenv("HERDR_SESSION_FROM_FILE") == s {
			return s, "file"
		}
		return s, "env"
	}
	if _, v, ok := whoamiSessionFile(root); ok {
		return v, "file"
	}
	return "default", "default"
}

// whoamiReal は存在しなくてもよい path の実体(symlink を辿る。bash の _whoami_real)
func whoamiReal(p string) string {
	for n := 0; n < 20; n++ {
		st, err := os.Lstat(p)
		if err != nil || st.Mode()&os.ModeSymlink == 0 {
			break
		}
		d, err := filepath.EvalSymlinks(filepath.Dir(p))
		if err != nil {
			break
		}
		t, err := os.Readlink(p)
		if err != nil {
			break
		}
		if !filepath.IsAbs(t) {
			t = filepath.Join(d, t)
		}
		p = t
	}
	if d, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		if d, err = filepath.Abs(d); err == nil {
			return strings.TrimSuffix(d, "/") + "/" + filepath.Base(p)
		}
	}
	return p
}

func cmdWhoami(args []string, w, stderr io.Writer) error {
	short := false
	if len(args) > 0 {
		if args[0] != "--short" {
			return usage("whoami [--short]")
		}
		short = true
		args = args[1:]
	}
	if len(args) != 0 {
		return usage("whoami [--short]")
	}
	root := whoamiRoot()
	session, ssrc := whoamiSession(root)
	if short {
		fmt.Fprintln(w, session)
		return nil
	}
	sock := os.Getenv("HERDR_SOCKET_PATH")
	rootReal := root
	if r, err := filepath.EvalSymlinks(root); err == nil {
		if r, err = filepath.Abs(r); err == nil {
			rootReal = r
		}
	}
	db, dbsrc := os.Getenv("TB_DB"), "env TB_DB"
	if db == "" {
		db, dbsrc = filepath.Join(env.StateDir(), "tasks.db"), "既定(全セッション共有。行は herdr_session で分ける)"
	}
	dbreal := whoamiReal(db)
	switch ssrc {
	case "socket":
		ssrc = "env HERDR_SOCKET_PATH"
	case "env":
		ssrc = "env HERDR_SESSION"
	case "file":
		f, _, _ := whoamiSessionFile(root)
		ssrc = "ファイル " + f
	default:
		ssrc = "既定"
	}
	inst, isrc := os.Getenv("TB_INSTANCE"), "env TB_INSTANCE"
	instRaw := inst
	if inst == "" {
		inst, isrc = "(未設定)", "既定"
	}
	perr := false
	prefix := "local.herdr-"
	if instRaw != "" {
		if reWhoInst.MatchString(instRaw) {
			prefix = "local.herdr-" + instRaw + "-"
		} else {
			perr, prefix = true, "(TB_INSTANCE が不正)"
		}
	}
	pane := os.Getenv("HERDR_PANE_ID")
	paneyn := "なし (人のシェル)"
	if pane != "" {
		paneyn = "あり: " + pane + " (agent のペイン)"
	}
	row := func(k, v string) { fmt.Fprintf(w, "%-13s %s\n", k, v) }
	tagged := func(k, v, tag string) { fmt.Fprintf(w, "%-13s %s   [%s]\n", k, v, tag) }
	tagged("checkout", rootReal, "checkout")
	tagged("db", db, dbsrc)
	if dbreal != db {
		row("", "実体: "+dbreal)
	}
	if _, err := os.Stat(db); err != nil {
		row("", "(まだ無い。tb の他のコマンドが初回に作る)")
	}
	legacy := filepath.Join(root, "taskboard", "data", "tasks.db")
	if os.Getenv("TB_DB") == "" {
		if st, err := os.Stat(legacy); err == nil && st.Mode().IsRegular() {
			row("", "旧形式の DB が残っています: "+legacy+"(取り込むには tb db-import)")
		}
	}
	tagged("session", session, ssrc)
	s := sock
	if s == "" {
		s = "(未設定)"
	}
	row("socket", s)
	tagged("instance", inst, isrc)
	tagged("label-prefix", prefix, isrc)
	tagged("pane", paneyn, "env HERDR_PANE_ID")

	// 食い違いは黙らせず notes に出す(見るだけ。何も変えない)
	expect := session
	if session == "default" {
		expect = ""
	}
	if hs := os.Getenv("HERDR_SESSION"); ssrc == "env HERDR_SOCKET_PATH" && hs != "" && hs != session {
		fmt.Fprintf(w, "note: HERDR_SESSION=%s は HERDR_SOCKET_PATH(%s)に負けて効いていません\n", hs, session)
	}
	if sock != "" && ssrc != "env HERDR_SOCKET_PATH" {
		fmt.Fprintf(w, "note: HERDR_SOCKET_PATH(%s)の配置から名前を導けないため、セッション名は [%s] から決めています\n", sock, ssrc)
	}
	if perr {
		fmt.Fprintf(w, "note: TB_INSTANCE は英数字・. _ - の 64 字以内にしてください(今の値: %s)\n", instRaw)
	}
	if !perr && instRaw != expect {
		if instRaw == "" {
			fmt.Fprintf(w, "note: セッション %s ですが TB_INSTANCE が未設定のため、launchd ラベルは従来の %s のままです(同じ接頭辞を使う他の checkout と名前空間を共有します)\n", session, prefix)
		} else {
			fmt.Fprintf(w, "note: TB_INSTANCE=%s がセッション %s と対応していません(default は TB_INSTANCE を渡さず、他は TB_INSTANCE=セッション名が設計の既定)\n", instRaw, session)
		}
	}
	return nil
}
