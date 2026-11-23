// herdrctl は、bash と Python の部品を順に移していく先の 1 つのバイナリ(ADR 0002)。
// 今はまだ version と help だけ。コマンドは移し終えたものから足す。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/env"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/sched"
	"github.com/imohiyoko/herdrorchestrator/internal/tbcli"
	"github.com/imohiyoko/herdrorchestrator/internal/version"
	"github.com/imohiyoko/herdrorchestrator/internal/webui"
)

const usage = `usage: herdrctl <command>

commands:
  version     版・コミット・ビルド日時・OS/アーキテクチャを出す
  db status   tasks.db の場所と版を出す(JSON。DB は変えない・作らない)
  db migrate  tasks.db のスキーマを最新にする(tb と同じスキーマ。TB_DB で場所を変える)
  tb <cmd>     tb のコマンド(tb --commands で一覧)。ふつうは taskboard/bin/tb から呼ぶ
  sched show <job>  scheduler のジョブの実際に効く設定(JSON。scheduler/bin/show.sh が渡す)
  webui serve  Web UI の Go 版(移行中。ふつうは WEBUI_IMPL=go python3 webui/server.py から呼ぶ)
  help        この一覧を出す
`

// 終了コードは tb に合わせる: 0 成功 / 1 エラー / 2 使い方の誤り
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, version.String())
		return 0
	case "tb":
		return tbcli.Run(args[1:], stdout, stderr)
	case "db":
		return runDB(args[1:], stdout, stderr)
	case "sched":
		return sched.Run(args[1:], stdout, stderr)
	case "webui":
		if len(args) == 2 && args[1] == "serve" {
			return webui.Serve(stdout, stderr)
		}
		fmt.Fprint(stderr, "使い方: herdrctl webui serve\n")
		return 2
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "herdrctl: 不明なコマンド: %s\n%s", args[0], usage)
		return 2
	}
}

func runDB(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	path := env.TBDB()
	switch args[0] {
	case "status":
		st, err := store.Inspect(path)
		if err != nil {
			fmt.Fprintf(stderr, "herdrctl: %v\n", err)
			return 1
		}
		enc := json.NewEncoder(stdout)
		enc.SetEscapeHTML(false)
		enc.Encode(st)
		return 0
	case "migrate":
		db, err := store.Open(path, env.SessionName(), nil)
		if err != nil {
			fmt.Fprintf(stderr, "herdrctl: %v\n", err)
			return 1
		}
		defer db.Close()
		v, err := db.UserVersion()
		if err != nil {
			fmt.Fprintf(stderr, "herdrctl: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "%s: user_version=%d\n", path, v)
		return 0
	default:
		fmt.Fprintf(stderr, "herdrctl: 不明なコマンド: db %s\n%s", args[0], usage)
		return 2
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
