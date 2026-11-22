package tbcli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/handoff"
	"github.com/imohiyoko/herdrorchestrator/internal/limits"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

const handoffUsage = `tb handoff write <task-id>   人が貼る依頼文(目的・必要なアクセス・期限・返し先)を data/handoff/<task-id>.md に書くだけ。何も送らない
(古い形 tb handoff <task-id> も同じに使える。human_handoff が on のときだけ)
`

// cmdHandoff は tb handoff write <task-id>(古い形 tb handoff <task-id> も)。
func cmdHandoff(args []string, w, stderr io.Writer) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "write":
		args = args[1:]
	case "", "-h", "--help", "help":
		fmt.Fprint(w, handoffUsage)
		if sub == "" {
			return &exit{exitUsage, ""}
		}
		return nil
	}
	if len(args) != 1 {
		return usage("handoff write <task-id>")
	}
	tid := args[0]
	if !reID.MatchString(tid) {
		return usage("タスク ID は T-<project>-NNN 形式: %s", tid)
	}
	if limits.Load(db.DB, db.Session).Get("human_handoff") != "on" {
		return &exit{4, "handoff は使えません(human_handoff が off です。使うのは人が tb limits set human_handoff on)。何も書きません"}
	}
	title, summary, ok, err := tickets.TitleSummary(db.DB, tid)
	if err != nil {
		return err
	}
	if !ok {
		return die("タスクが見つかりません: %s", tid)
	}
	// bash は jq -r の出力を $(...) で受けるので、末尾の改行は落ちる
	text := handoff.Text(tid, strings.TrimRight(title, "\n"), strings.TrimRight(summary, "\n"))
	if hits := handoff.SecretScan(text); len(hits) > 0 {
		return &exit{4, "文面に秘密らしい文字列が含まれるため、書きません(検出: " + strings.Join(hits, " ") + " )。" +
			" タスクの題名・要約から取り除いてから、もう一度 tb handoff write " + tid + " を実行してください(本文は表示しません)"}
	}
	dir := os.Getenv("TB_HANDOFF_DIR")
	if dir == "" {
		dir = filepath.Join(os.Getenv("TB_ROOT"), "data", "handoff")
	}
	f := filepath.Join(dir, tid+".md")
	if _, err := os.Lstat(f); err == nil {
		return die("もう あります: %s(人が編集している可能性があるので上書きしません。消してからやり直してください)", f)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(f, []byte(text+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Fprintln(w, f)
	fmt.Fprintln(w, "(人が内容を確かめて貼る文面です。tb は何も送っていません)")
	return nil
}
