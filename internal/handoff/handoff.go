// Package handoff は他の人への調査依頼の文面づくり(tb handoff。taskboard/lib/handoff.sh)。
// 文面をファイルに書くだけで、何も送らない。
package handoff

import (
	"regexp"
	"strings"
)

var patterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"AWS アクセスキー", regexp.MustCompile(`(?i)(AKIA|ASIA)[0-9A-Z]{16}`)},
	{"秘密鍵のヘッダ", regexp.MustCompile(`(?i)-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{"トークン(GitHub・Slack・API キーの形式)", regexp.MustCompile(`(?i)(gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[abprs]-[A-Za-z0-9-]{10,}|sk-[A-Za-z0-9_-]{20,}|AIza[0-9A-Za-z_-]{30,})`)},
	{"Bearer トークン", regexp.MustCompile(`(?i)bearer +[A-Za-z0-9._~+/=-]{16,}`)},
	{"キー=値の形の秘密", regexp.MustCompile(`(?i)(token|secret|passw(or)?d|api[_-]?key)[[:space:]]*[:=][[:space:]]*[^[:space:]]{8,}`)},
	{"長い英数字の連なり(トークンの疑い)", regexp.MustCompile(`(?i)[A-Za-z0-9]{32,}`)},
}

// SecretScan は handoff_secret_scan と同じ: 秘密らしいもののパターンの名前(grep と同じく行ごとに当てる)。
func SecretScan(text string) []string {
	lines := strings.Split(text, "\n")
	var hits []string
	for _, p := range patterns {
		for _, l := range lines {
			if p.re.MatchString(l) {
				hits = append(hits, p.name)
				break
			}
		}
	}
	return hits
}

// Text は人が貼る依頼文(末尾の改行なし)。
func Text(tid, title, summary string) string {
	if summary == "" {
		summary = "(要約はまだありません。人が書いてください)"
	}
	return `# 調査のお願い(人が貼る文面): ` + tid + `

> この文面は人が貼るためのものです。agent は誰にも連絡しません(送信も投稿もしません)。
> 送るかどうか、誰に・どこに送るかは人が決めます。貼る前に、秘密の値・リポジトリ名・組織名・顧客情報が
> 入っていないか確かめ、あれば <repo-A> のような仮名に置き換えてください。

## 目的
` + title + `
` + summary + `

## 必要なアクセス
<必要なアクセスを人が書く(例: <repo-A> の読み取り)>

## 期限
<期限を人が決める>

## 成果物の返し先
<返し先を人が決める>
結果は人が受け取り、tb comment 相当で指揮官に戻します(本文はデータとして読まれます)。`
}
