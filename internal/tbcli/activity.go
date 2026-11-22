package tbcli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/activity"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/env"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// touchTimeoutMS は touch_impl の SQL_TIMEOUT(ロックされていても 1 秒で諦める)。
const touchTimeoutMS = 1000

// cmdTouch は cmd_touch と同じ: 何も出さず、失敗は touch-errors.log に「時刻 event 理由コード」の 1 行だけを書く。
func cmdTouch(args []string) {
	reason := touch(args)
	if reason == "" {
		return
	}
	log := os.Getenv("TB_TOUCH_LOG")
	if log == "" {
		log = filepath.Join(filepath.Dir(env.TBDB()), "touch-errors.log")
	}
	_ = os.MkdirAll(filepath.Dir(log), 0o700)
	f, err := os.OpenFile(log, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s event=%s reason=%s\n", time.Now().UTC().Format("2006-01-02T15:04:05Z"), activity.LogEvent(args), reason)
}

func touch(args []string) (reason string) {
	defer func() {
		if recover() != nil {
			reason = "unknown"
		}
	}()
	t, reason := activity.ParseTouch(args)
	if reason != "" {
		return reason
	}
	var opt store.Options
	opt.BusyTimeoutMS = touchTimeoutMS
	opt.NoSession = true // 見張りなしで書く(行を別の herdr セッションから移すことがある)
	if d := os.Getenv("TB_SCHEMA_DIR"); d != "" {
		opt.Schema = os.DirFS(d)
	}
	session := env.SessionName()
	db, err := store.Open(env.TBDB(), session, &opt)
	if err != nil {
		return activity.Reason(err)
	}
	defer db.Close()
	if err := activity.Upsert(db.DB, session, t); err != nil {
		return activity.Reason(err)
	}
	return ""
}

func cmdActivity(args []string, w, stderr io.Writer) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	js, stuck := false, "10"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			js = true
		case "--stuck-min":
			if i+1 >= len(args) {
				return usage("%s に値が必要です", args[i])
			}
			i++
			stuck = args[i]
		default:
			return usage("activity: 不明な引数 %s", args[i])
		}
	}
	if !activity.ValidMinutes(stuck) {
		return usage("分は数字: %s", stuck)
	}
	return activity.List(db.DB, db.Session, stuck, js, func(cols []string, vals []any) error {
		if js {
			s, err := rowJSON(cols, vals)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(w, s)
			return err
		}
		parts := make([]string, len(vals))
		for i, v := range vals {
			if v == nil {
				parts[i] = "-" // sqlite3 -nullvalue -
			} else {
				parts[i] = tickets.Text(v)
			}
		}
		_, err := fmt.Fprintln(w, strings.Join(parts, "\t"))
		return err
	})
}
