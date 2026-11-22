package tbcli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/conv"
)

const convUsage = `tb conv route|routes-set|draft|voice|voice-check|length|kinds|patterns|lint ...   会話チームの振り分けと下書き(引数は tb help)
tb conv pair-add|pairs|pair-show|candidate-add|ledger|candidate-adopt|candidate-reject|scan|learn-purge ...   会話の学習
tb conv stage|stage-set|stage-reset|stage-log|incident|gate ...   会話チームの段階
(古い名前 tb conv-<サブコマンド> も同じに使える)
`

// convSubs は tb conv のサブコマンド(古い名前は conv-<サブコマンド>)。
var convSubs = map[string]func(*conv.Env, []string) error{
	"route": (*conv.Env).Route, "routes-set": (*conv.Env).RoutesSet, "draft": (*conv.Env).Draft, "voice": (*conv.Env).Voice,
	"voice-check": (*conv.Env).VoiceCheck, "length": (*conv.Env).Length, "kinds": (*conv.Env).KindsCmd, "patterns": (*conv.Env).Patterns,
	"lint": (*conv.Env).Lint, "pair-add": (*conv.Env).PairAdd, "pairs": (*conv.Env).Pairs, "pair-show": (*conv.Env).PairShow,
	"candidate-add": (*conv.Env).CandidateAdd, "ledger": (*conv.Env).Ledger, "candidate-adopt": (*conv.Env).CandidateAdopt,
	"candidate-reject": (*conv.Env).CandidateReject, "scan": (*conv.Env).Scan, "learn-purge": (*conv.Env).LearnPurge,
	"stage": (*conv.Env).Stage, "stage-set": (*conv.Env).StageSet, "stage-reset": (*conv.Env).StageReset,
	"stage-log": (*conv.Env).StageLogCmd, "incident": (*conv.Env).Incident, "gate": (*conv.Env).Gate,
}

// cmdConv は tb conv <サブコマンド>。legacy は古い名前(conv-route など)で呼ばれたときのその名前。
func cmdConv(args []string, w, stderr io.Writer, legacy string) error {
	sub := strings.TrimPrefix(legacy, "conv-")
	if legacy == "" {
		if len(args) == 0 {
			fmt.Fprint(w, convUsage)
			return &exit{exitUsage, ""}
		}
		sub, args = args[0], args[1:]
		if sub == "help" || sub == "-h" || sub == "--help" {
			fmt.Fprint(w, convUsage)
			return nil
		}
	}
	f, ok := convSubs[sub]
	if !ok {
		return usage("conv: 不明なサブコマンド %s(tb conv で一覧)", sub)
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	e := &conv.Env{DB: db.DB, Sess: db.Session, W: w, Err: stderr, In: os.Stdin, StdinIsTTY: stdinIsTTY()}
	err = f(e, args)
	var ce *conv.Err
	if errors.As(err, &ce) {
		switch {
		case ce.Msg == "":
			return &exit{ce.Code, ""}
		case ce.Code == 2:
			return usage("%s", ce.Msg)
		default:
			return &exit{ce.Code, ce.Msg}
		}
	}
	return err
}
