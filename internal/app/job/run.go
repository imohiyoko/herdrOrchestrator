package job

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/jobs"
	"github.com/imohiyoko/herdrorchestrator/internal/sched"
)

// RunReq は job.run の入力(ジョブを今すぐ 1 回動かす)。
type RunReq struct {
	Name string `api:"name,path"`
}

// RunResult は job.run の結果: その回の記録(scheduler/logs/<job>.log に足した行)の要点。
type RunResult struct {
	Name   string `api:"name" json:"name"`
	Action string `api:"action" json:"action"` // sent / ran / failed / skipped_state / skipped_gate / skipped_disabled など
	Reason string `api:"reason,optional" json:"reason,omitempty"`
	Exit   *int   `api:"exit,optional" json:"exit,omitempty"` // コマンド型の終了コード
	Stdout string `api:"stdout,optional" json:"stdout,omitempty"`
	OK     bool   `api:"ok" json:"ok"` // sent か ran
}

// runTimeout は 1 回の上限(COMMAND_TIMEOUT などはジョブの中で効く。これは子が戻らないときの守り)。
const runTimeout = 15 * time.Minute

func runOp(env *app.Env) app.Op {
	return app.Command(env, "job.run", app.Cmd("cmd:job:run", "tb job run"),
		func(ctx context.Context, in RunReq) (RunResult, error) {
			if !jobs.ValidName(in.Name) {
				return RunResult{}, define.Errorf(define.CodeInvalidArgument, "ジョブ名は英数字で始まり、英数字・_・- の 64 字以内(dispatch は予約名): %s", in.Name)
			}
			te, childEnv := tickEnv()
			if _, err := te.Load(in.Name); err != nil {
				return RunResult{}, define.Errorf(define.CodeFailedPrecondition, "%s", err.Error())
			}
			bin := os.Getenv("SCHED_HERDRCTL")
			if bin == "" {
				var err error
				if bin, err = os.Executable(); err != nil {
					return RunResult{}, define.Errorf(define.CodeInternal, "herdrctl の場所が分かりません: %s", err.Error())
				}
			}
			log := filepath.Join(te.LogDir, in.Name+".log")
			var off int64
			if st, err := os.Stat(log); err == nil {
				off = st.Size()
			}
			cctx, cancel := context.WithTimeout(ctx, runTimeout)
			defer cancel()
			var errb bytes.Buffer
			c := exec.CommandContext(cctx, bin, "sched", "tick", "--now", in.Name)
			c.Stdout, c.Stderr, c.Env = io.Discard, &errb, childEnv
			runErr := c.Run()
			r := RunResult{Name: in.Name}
			if rec := lastAppended(log, off); rec != nil {
				r.Action, _ = rec["action"].(string)
				r.Reason, _ = rec["reason"].(string)
				r.Stdout, _ = rec["stdout"].(string)
				if x, ok := rec["exit"].(float64); ok {
					n := int(x)
					r.Exit = &n
				}
			} else if runErr != nil {
				return r, define.Errorf(define.CodeInternal, "ジョブを動かせませんでした: %s", strings.TrimSpace(errb.String()+" "+runErr.Error()))
			}
			r.OK = r.Action == "sent" || r.Action == "ran"
			return r, nil
		},
		define.Doc("scheduler のジョブを時刻に関係なく今すぐ 1 回動かす(tb job run。ENABLED・GATE・SKIP_IF は効く。既定は人だけ)"),
		define.Authz("cmd:job:run", define.Resource("job", "name")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/jobs/{name}/run"),
	)
}

// tickEnv は scheduler の置き場所: SCHED_ROOT / LOG_DIR が無ければ tb と同じ所(TB_SCHED_DIR か <TB_ROOT>/../scheduler)。
// 子(herdrctl sched tick --now)にも同じ置き場所を渡す。
func tickEnv() (sched.TickEnv, []string) {
	dir := jobs.DefaultEnv().Dir
	env := os.Environ()
	if os.Getenv("SCHED_ROOT") == "" {
		env = append(env, "SCHED_ROOT="+dir)
	}
	if os.Getenv("LOG_DIR") == "" {
		root := os.Getenv("SCHED_ROOT")
		if root == "" {
			root = dir
		}
		env = append(env, "LOG_DIR="+filepath.Join(root, "logs"))
	}
	te := sched.DefaultTickEnv()
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "SCHED_ROOT":
			te.Root = v
			if os.Getenv("JOBS_DIR") == "" {
				te.JobsDir = filepath.Join(v, "jobs")
			}
			if os.Getenv("SCHED_SETTINGS_DIR") == "" {
				te.SettingsDir = filepath.Join(v, "settings")
			}
		case "LOG_DIR":
			te.LogDir = v
		}
	}
	return te, env
}

// lastAppended はログの off より後に足された行のうち、最後の JSON(無ければ nil)。
func lastAppended(path string, off int64) map[string]any {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil
	}
	var last map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) == nil {
			last = m
		}
	}
	return last
}
