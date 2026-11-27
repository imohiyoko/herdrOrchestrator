package tbcli

import (
	"fmt"
	"io"
	"strings"

	appconfig "github.com/imohiyoko/herdrorchestrator/internal/app/config"
)

// tb db-import <旧 DB のパス> [--session <名前>] [--dry-run]: 旧形式の DB を共有の DB の 1 セッションとして取り込む。
// 取り込みの中身は API(config.db.import。internal/app/config/dbimport.go)。ここは引数の読み取りと表示だけ。
func cmdDBImport(args []string, w, stderr io.Writer) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	in := appconfig.DBImportReq{Session: db.Session}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--session":
			v, err := needVal(args, i)
			if err != nil {
				return err
			}
			in.Session = v
			i++
		case a == "--dry-run":
			in.DryRun = true
		case strings.HasPrefix(a, "-"):
			return usage("db-import: 不明な引数 %s", a)
		default:
			if in.Path != "" {
				return usage("db-import: 旧 DB は 1 つだけ")
			}
			in.Path = a
		}
	}
	if in.Path == "" {
		return usage("db-import <旧 DB のパス> [--session <名前>] [--dry-run]")
	}
	if !appconfig.ReSessName.MatchString(in.Session) {
		return usage("db-import: セッション名は英数字で始まり、英数字と . _ - の 64 字以内: %s", in.Session)
	}
	var r appconfig.DBImportResult
	if err := callApp(db, "tb db-import", "config.db.import", in, &r); err != nil {
		return err
	}
	fmt.Fprint(w, r.Out)
	return nil
}
