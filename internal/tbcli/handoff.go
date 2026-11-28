package tbcli

import (
	"fmt"
	"io"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	apphandoff "github.com/imohiyoko/herdrorchestrator/internal/app/handoff"
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
	var r apphandoff.WriteResult
	if err := callAppW(db, app.Human(), io.Discard, "handoff.write", apphandoff.WriteReq{Task: tid}, &r); err != nil {
		return err
	}
	f := r.Path
	fmt.Fprintln(w, f)
	fmt.Fprintln(w, "(人が内容を確かめて貼る文面です。tb は何も送っていません)")
	return nil
}
