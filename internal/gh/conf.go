// Package gh は GitHub のサービス(tb gh): PR のコメント・レビューを読む・書く、approve する。
// GitHub へは、人が設定 config/github.conf に書いたコマンド(gh か、ghtkn など gh と同じ引数を受けるラッパー)を通してだけ出る。
// PATH 上の gh には落とさない(fail-closed)。誰が何をしてよいかは設定 policy の gh:<操作> <owner/repo> の行で決める(ADR 0007)。
package gh

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// AuthRC はラッパーが「認証で止まった(auth-locked)」を伝える終了コード(pr-queue と同じ。docs/pr-queue-gh-wrapper.md)。
const AuthRC = 75

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// ConfPath は設定ファイル: TB_GITHUB_CONF(既定は <tb>/config/github.conf)。
func ConfPath() string {
	return envOr("TB_GITHUB_CONF", filepath.Join(os.Getenv("TB_ROOT"), "config", "github.conf"))
}

// confValue は "<key> = <値>" の最後の行の値(最初の # から後ろと前後の空白を落とす。pr-queue.conf と同じ書き方)。
func confValue(body, key string) string {
	re := regexp.MustCompile(`^[[:space:]]*` + regexp.QuoteMeta(key) + `[[:space:]]*=`)
	v := ""
	for _, l := range strings.Split(body, "\n") {
		if !re.MatchString(l) {
			continue
		}
		_, l, _ = strings.Cut(l, "=")
		l, _, _ = strings.Cut(l, "#")
		v = strings.TrimSpace(l)
	}
	return v
}

// keysOf は操作 op に使うコマンドのキー(先に当たったもの)。
// GitHub App の token を操作ごとに切り替えるラッパー(ghtkn など)のために、操作ごと → 読み / 書き → 共通 の順に引く。
func keysOf(op string) []string {
	name := strings.TrimPrefix(op, "gh:")
	if op == OpRead {
		return []string{"gh_read", "gh"}
	}
	return []string{"gh_" + name, "gh_write", "gh"}
}

// Command は操作 op に使うコマンド(実行ファイルと、前に付ける固定の引数)と、使ったキー。
// 引く順は keysOf(gh_<操作> → gh_write → gh。読みは gh_read → gh)。
// 値は「絶対パスの実行ファイル [固定の引数...]」(空白で区切る。空白を含む引数はラッパーのスクリプトに書く)。
// 未指定・絶対パスでない・実行できないときは、理由を返す(PATH 上の gh には落とさない)。
func Command(op string) (argv []string, key, problem string) {
	f := ConfPath()
	b, err := os.ReadFile(f)
	if err != nil {
		return nil, "", "設定 " + f + " を読めません。github.conf.example を写して gh を書いてください(PATH 上の gh には落としません)"
	}
	keys := keysOf(op)
	v := ""
	for _, key = range keys {
		if v = confValue(string(b), key); v != "" {
			break
		}
	}
	if v == "" {
		return nil, "", "設定 " + f + " に " + strings.Join(keys, " / ") + " のどれもありません(PATH 上の gh には落としません)"
	}
	argv = strings.Fields(v)
	if !executable(argv[0]) {
		return nil, "", "設定 " + f + " の " + key + " を使えません。絶対パスの実行ファイルを指定してください: " + argv[0]
	}
	return argv, key, ""
}

func executable(p string) bool {
	if !filepath.IsAbs(p) {
		return false
	}
	st, err := os.Stat(p)
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	return runtime.GOOS == "windows" || st.Mode()&0o111 != 0
}

// limitRead は本文を上限まで読む(上限を超えたら ok=false)。
func limitRead(r io.Reader, max int) (string, bool, error) {
	b, err := io.ReadAll(io.LimitReader(r, int64(max)+1))
	if err != nil {
		return "", false, err
	}
	if len(b) > max {
		return "", false, nil
	}
	return string(b), true, nil
}
