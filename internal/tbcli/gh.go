package tbcli

import (
	"database/sql"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/gh"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/identity"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/perms"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
)

// cmdGh は tb gh <対象> <動詞>(GitHub の PR のコメント・レビューと、ほかの gh のコマンド。internal/gh)。
func cmdGh(args []string, w, stderr io.Writer) error {
	return ghRun(w, stderr, func(s *gh.Svc) error { return s.Run(args) })
}

// cmdGit は tb git fetch|pull|push|clone|ls-remote(GitHub のリモート。internal/gh)。
func cmdGit(args []string, w, stderr io.Writer) error {
	return ghRun(w, stderr, func(s *gh.Svc) error { return s.Git(args) })
}

func ghRun(w, stderr io.Writer, run func(*gh.Svc) error) error {
	if f, ok := w.(interface{ Flush() error }); ok {
		defer f.Flush()
	}
	s := &gh.Svc{W: w, Err: stderr, Stdin: os.Stdin}
	s.Authorize = func(op, repo, what string) error {
		by, err := ghAuthorize(op, repo, what)
		s.Actor = by
		return err
	}
	err := run(s)
	var ge *gh.Err
	if errors.As(err, &ge) {
		switch {
		case ge.Msg == "":
			return &exit{ge.Code, ""}
		case ge.Code == exitUsage:
			return usage("%s", ge.Msg)
		}
		return &exit{ge.Code, ge.Msg}
	}
	return err
}

// ghJudge は gh:<操作> <owner/repo> の効果と根拠。GitHub と同じく、owner/repo は大文字・小文字を区別せずに policy の対象と照合する
// (agent 名・役割は / を含まないので、今までどおり Roles.Match で照合する)。
func ghJudge(db *sql.DB, session, who, op, repo string) (tier, rule string) {
	j := approval.NewJudge(db, session)
	m := perms.Matcher{Rel: j.Relation, Name: func(name, pat string) int {
		if strings.Contains(name, "/") {
			if util.Glob(strings.ToLower(pat), strings.ToLower(name)) {
				return 0
			}
			return 1
		}
		return j.Roles.Match(name, pat)
	}}
	return perms.Authorize(db, session, who, op, strings.ToLower(repo), m)
}

// ghAuthorize は gh:<操作> <owner/repo> を設定 policy で判定する(既定は perms/ops.tbl)。
// allow なら通す。ask なら承認待ち Q-<n> に積んで 20(承認されると依頼者の権限で 1 回だけ実行)。deny なら 4。
// 呼び出し元(human / agent 名 / system:<名前>)も返す。
func ghAuthorize(op, repo, what string) (string, error) {
	by, err := requester()
	if err != nil {
		return "", err
	}
	if by == "human" && identity.System != "" {
		by = identity.System
	}
	db, err := openDB()
	if err != nil {
		return by, err
	}
	who := by
	if identity.System != "" {
		who = "human" // system:<名前> は Judge が identity.System から当てる
	}
	tier, rule := ghJudge(db.DB, db.Session, who, op, repo)
	db.Close()
	if tier == "deny" {
		return by, &exit{4, what + " " + repo + " は実行できません(" + op + " = deny。" + rule + ")"}
	}
	return by, permErr(perms.EnforceTarget(op, repo, tier, rule, what))
}
