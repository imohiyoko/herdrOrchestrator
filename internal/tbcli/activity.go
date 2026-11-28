package tbcli

import (
	"fmt"
	"io"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	appactivity "github.com/imohiyoko/herdrorchestrator/internal/app/activity"
)

// cmdTouch は cmd_touch と同じ: 何も出さず、失敗は touch-errors.log に 1 行だけを書く。
func cmdTouch(args []string) { appactivity.Touch(args) }

func cmdActivity(args []string, w, stderr io.Writer) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	in := appactivity.ListReq{StuckMin: "10"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			in.JSON = true
		case "--stuck-min":
			if i+1 >= len(args) {
				return usage("%s に値が必要です", args[i])
			}
			i++
			in.StuckMin = args[i]
		default:
			return usage("activity: 不明な引数 %s", args[i])
		}
	}
	if in.StuckMin == "" { // 操作は空を既定(10)とみなすので、空の指定はここで断る
		return usage("分は数字: ")
	}
	var r appactivity.ListResult
	if err := callAppW(db, app.Human(), io.Discard, "activity.list", in, &r); err != nil {
		return err
	}
	fmt.Fprint(w, r.Text)
	return nil
}
