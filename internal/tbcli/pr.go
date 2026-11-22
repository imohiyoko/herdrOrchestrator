package tbcli

import (
	"errors"
	"fmt"
	"io"

	"github.com/imohiyoko/herdrorchestrator/internal/pr"
)

const prUsage = `tb pr queue add|list|show|approve|reject|retry|run|scan ...   PR 作成キュー(引数は tb help)
tb pr watch   許可リストの PR を gh の読み取りだけで見て、新規・approve・変更依頼・マージを積む
(古い名前 tb pr-queue / tb pr-watch も同じに使える)
`

// cmdPr は tb pr queue|watch。legacy は古い名前(pr-queue / pr-watch)で呼ばれたときのその名前。
func cmdPr(args []string, w, stderr io.Writer, legacy string) error {
	sub := legacy
	if sub == "" {
		if len(args) == 0 {
			fmt.Fprint(w, prUsage)
			return &exit{exitUsage, ""}
		}
		sub, args = args[0], args[1:]
		switch sub {
		case "help", "-h", "--help":
			fmt.Fprint(w, prUsage)
			return nil
		case "queue", "watch":
			sub = "pr-" + sub
		default:
			return usage("pr: 不明なサブコマンド %s(queue / watch)", sub)
		}
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	q := &pr.Q{DB: db.DB, Sess: db.Session, W: w, Err: stderr}
	if sub == "pr-watch" {
		if len(args) != 0 {
			return usage("pr-watch(引数なし)")
		}
		err = q.Watch()
	} else {
		err = q.Cmd(args, rowJSON)
	}
	var pe *pr.Err
	switch {
	case errors.As(err, &pe):
		if pe.Code == 2 {
			return usage("%s", pe.Msg)
		}
		return die("%s", pe.Msg)
	case errors.Is(err, pr.ErrStop):
		return &exit{exitErr, ""}
	}
	return err
}
