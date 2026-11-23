package sched

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const usage = `usage: herdrctl sched <command>

commands:
  --features   受け持てるものを空白区切りで出す
  lint <job>   標準入力のジョブ定義を読めるかを確かめる(上書き設定と SCHED_* は今のものを使う。setup/check.sh が使う)
  show <job>   実際に効く設定(定義 + 上書き設定 + SCHED_*)を JSON 1 行で出す(scheduler/bin/show.sh が渡す)
  tick <job>   この回に実行する回を判定して実行し、logs/<job>.log に記録する(scheduler/bin/tick.sh が渡す)
  jobs         ジョブ名の一覧(scheduler/bin/jobs.sh が渡す)
  dispatch <job> [<job> ...]
               複数のジョブの tick を並べて動かす(scheduler/bin/dispatch.sh が渡す)
  gen-plist <job> | --dispatch <job> [<job> ...]
               launchd の plist を作る(scheduler/bin/gen-plist.sh が渡す)
  install {install|uninstall|status} <job> | --dispatch
               launchd に登録する・外す・状態を見る(scheduler/bin/install.sh が渡す)
`

// Features は受け持てるもの(scheduler/bin/lib.sh の sched_go が、このサブコマンドがあるかをこれで確かめる。無ければ herdrctl が古い)。
var Features = []string{"show", "tick", "jobs", "dispatch", "gen-plist", "install", "lint"}

// Run は herdrctl sched <args>。終了コードは scheduler に合わせる(die は 2)。
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "show":
		name := ""
		if len(args) > 1 {
			name = args[1]
		}
		return finish(show(DefaultEnv(), name, stdout), stderr)
	case "tick":
		name := ""
		if len(args) > 1 {
			name = args[1]
		}
		return Tick(DefaultTickEnv(), name, stdout, stderr)
	case "jobs":
		for _, j := range DefaultEnv().JobNames() {
			fmt.Fprintln(stdout, j)
		}
		return 0
	case "dispatch":
		return Dispatch(args[1:], stdout, stderr)
	case "gen-plist":
		return finish(GenPlist(DefaultEnv(), DefaultTickEnv().LogDir, args[1:], stdout), stderr)
	case "install":
		rc, err := Launchd(DefaultEnv(), args[1:], stdout, stderr)
		if err != nil {
			return finish(err, stderr)
		}
		return rc
	case "lint":
		name := ""
		if len(args) > 1 {
			name = args[1]
		}
		body, err := io.ReadAll(os.Stdin)
		if err != nil {
			return finish(err, stderr)
		}
		e := DefaultEnv()
		e.Bodies = map[string]string{"sched-job:" + name: string(body)}
		j, err := e.Load(name)
		if err == nil {
			_, err = DaysToNums(j.Name, j.Days) // show / tick と同じく DAYS も確かめる
		}
		return finish(err, stderr)
	case "--features":
		fmt.Fprintln(stdout, strings.Join(Features, " "))
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprintf(stderr, "herdrctl sched: 不明なコマンド: %s\n%s", args[0], usage)
	return 2
}

func finish(err error, stderr io.Writer) int {
	if err == nil {
		return 0
	}
	var d *Die
	if errors.As(err, &d) {
		fmt.Fprintln(stderr, "error: "+d.Msg)
		return 2
	}
	fmt.Fprintln(stderr, "error: "+err.Error())
	return 1
}

// ShowJSON は show.sh の出力(キーの順も同じ)。
func ShowJSON(j *Job) ([]byte, error) {
	kind := "prompt"
	if j.Command != "" {
		kind = "command"
	}
	nullable := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	var imin any
	if j.IntervalMin >= 0 {
		imin = j.IntervalMin
	}
	kv := []struct {
		k string
		v any
	}{
		{"job", j.Name}, {"kind", kind}, {"target", j.Target}, {"interval_min", imin}, {"interval_sec", j.IntervalSec},
		{"start", j.Start}, {"end", j.End}, {"days", j.Days}, {"enabled", j.Enabled == "1"}, {"tz", j.TZ},
		{"watcher", nullable(j.Watcher)}, {"owner", nullable(j.Owner)}, {"gate", j.Gate != ""},
		{"prompt_rules", len(j.Rules)}, {"prompt_cmd", j.PromptCmd != ""}, {"prompt_vars", len(j.Vars)},
		{"overridden", j.SettingsSrc != "none"}, {"source", j.Src}, {"settings_source", j.SettingsSrc},
	}
	var b bytes.Buffer
	b.WriteByte('{')
	for i, p := range kv {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := marshal(p.k)
		v, err := marshal(p.v)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

func show(e Env, name string, w io.Writer) error {
	j, err := e.Load(name)
	if err != nil {
		return err
	}
	if j.DayNums, err = DaysToNums(j.Name, j.Days); err != nil {
		return err
	}
	b, err := ShowJSON(j)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", b)
	return err
}
