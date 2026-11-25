package tbcli

import (
	"errors"
	"io"
	"os"

	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/gh"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/identity"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/perms"
)

// cmdGh は tb gh <対象> <動詞>(GitHub の PR のコメント・レビュー。internal/gh)。
func cmdGh(args []string, w, stderr io.Writer) error {
	s := &gh.Svc{W: w, Err: stderr, Stdin: os.Stdin}
	s.Authorize = func(op, repo, what string) error {
		by, err := ghAuthorize(op, repo, what)
		s.Actor = by
		return err
	}
	err := s.Run(args)
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
	tier, rule := approval.NewJudge(db.DB, db.Session).Authorize(op+":"+repo, who)
	db.Close()
	if tier == "deny" {
		return by, &exit{4, what + " " + repo + " は実行できません(" + op + " = deny。" + rule + ")"}
	}
	return by, permErr(perms.EnforceTarget(op, repo, tier, rule, what))
}
