package pr

import (
	"os"
	"os/exec"
	"strings"
)

// 戻り値の取り決め(bash の pq_* と同じ): 0 使える / 1 使えない(理由つき)/ 2 確かめられない(行は触らず次の回)。

// git は PR_QUEUE_GIT(差し替え用)。bash で "$git" と書いた所だけがこれを使い、git と書いた所は本物の git を使う。
func gitBin() string { return envOr("PR_QUEUE_GIT", "git") }

func repoDir() string { return os.Getenv("PR_QUEUE_REPO_DIR") }

// out は $(cmd 2>/dev/null) と同じ(末尾の改行を落とす)。
func out(bin string, args ...string) (string, error) {
	b, err := exec.Command(bin, args...).Output()
	return strings.TrimRight(string(b), "\n"), err
}

// fakeGit は "$git" -C "$d" ...。realGit は git -C "$d" ...。
func fakeGit(args ...string) (string, error) {
	return out(gitBin(), append([]string{"-C", repoDir()}, args...)...)
}
func realGit(args ...string) (string, error) {
	return out("git", append([]string{"-C", repoDir()}, args...)...)
}

// awk の既定の区切り(空白とタブの並び。先頭の空白は飛ばす)。
func awkFields(l string) []string {
	return strings.FieldsFunc(l, func(r rune) bool { return r == ' ' || r == '\t' })
}

func lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// lsHeadSHA は ls-remote --heads の出力から refs/heads/<b> の SHA(無ければ "")。
func lsHeadSHA(o, b string) (string, bool) {
	t := "refs/heads/" + b
	for _, l := range lines(o) {
		if f := awkFields(l); len(f) >= 2 && f[1] == t {
			return f[0], true
		}
	}
	return "", false
}

// defaultBranch は pq_default_branch: origin の既定ブランチ(git ls-remote --symref origin HEAD)。
func defaultBranch() (string, bool) {
	o, err := fakeGit("ls-remote", "--symref", "origin", "HEAD")
	if err != nil {
		return "", false
	}
	for _, l := range lines(o) {
		f := awkFields(l)
		if len(f) >= 3 && f[0] == "ref:" && f[2] == "HEAD" {
			b := strings.TrimPrefix(f[1], "refs/heads/")
			return b, b != "" && validRef(b)
		}
	}
	return "", false
}

// originState は pq_origin_state: 0 ある / 1 無い / 2 確かめられない。
func originState(b string) int {
	o, err := fakeGit("ls-remote", "--heads", "origin", "refs/heads/"+b)
	if err != nil {
		return 2
	}
	if _, ok := lsHeadSHA(o, b); ok {
		return 0
	}
	return 1
}

// originSHA は pq_origin_sha。
func originSHA(b string) (string, bool) {
	o, err := fakeGit("ls-remote", "--heads", "origin", "refs/heads/"+b)
	if err != nil {
		return "", false
	}
	s, ok := lsHeadSHA(o, b)
	return s, ok && s != ""
}

// repoDirOK は pq_repo_dir_ok。
func repoDirOK() (string, bool) {
	d := repoDir()
	switch {
	case d == "":
		return "PR_QUEUE_REPO_DIR が空(差分のガードは必須。clone のパスを指定する)", false
	case !strings.HasPrefix(d, "/"):
		return "PR_QUEUE_REPO_DIR が絶対パスではない", false
	}
	if st, err := os.Stat(d); err != nil || !st.IsDir() {
		return "PR_QUEUE_REPO_DIR が見つからない", false
	}
	if _, err := realGit("rev-parse", "-q", "--git-dir"); err != nil {
		return "PR_QUEUE_REPO_DIR が git のリポジトリではない", false
	}
	return "", true
}

func hasCommit(ref string) bool {
	_, err := realGit("rev-parse", "-q", "--verify", ref+"^{commit}")
	return err == nil
}

// syncOrigin は pq_sync_origin: origin の SHA を取り、ブランチと base を追跡参照に取り込み、一致を確かめる。dry なら fetch しない。
func syncOrigin(b, base string, dry bool) (sha, reason string, rc int) {
	o, err := fakeGit("ls-remote", "--heads", "origin", "refs/heads/"+b)
	if err != nil {
		return "", "origin の SHA を取得できない", 2
	}
	osha, _ := lsHeadSHA(o, b)
	if osha == "" {
		return "", "ブランチが origin に無い", 1
	}
	if !dry {
		if _, err := fakeGit("fetch", "-q", "origin", "+refs/heads/"+b+":refs/remotes/origin/"+b, "+refs/heads/"+base+":refs/remotes/origin/"+base); err != nil {
			return "", "origin から取り込めない(fetch に失敗)", 2
		}
	}
	tsha, err := realGit("rev-parse", "-q", "--verify", "refs/remotes/origin/"+b+"^{commit}")
	if err != nil {
		return "", "追跡参照が無い", 2
	}
	if tsha != osha {
		return "", "追跡参照が origin の SHA と一致しない(取り込み後に origin が変わった)", 2
	}
	return osha, "", 0
}
