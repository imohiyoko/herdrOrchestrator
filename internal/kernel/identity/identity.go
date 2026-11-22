// Package identity は呼び出し元が誰か(人か、どの agent か)を herdr から引く(ADR 0004 の土台。taskboard/lib/identity.sh)。
// 自己申告は使わない。
package identity

import (
	"os"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
)

// PaneOwnerName は pane の agent の名前(無ければ空)。一覧を取れなければ err。
func PaneOwnerName(pane string) (string, error) {
	as, err := herdr.Agents()
	if err != nil {
		return "", err
	}
	for _, a := range as {
		if a.PaneID == pane {
			return a.Name, nil
		}
	}
	return "", nil
}

// Denied は require_human が断った理由(bash の die の文面)。
type Denied struct{ Msg string }

func (d *Denied) Error() string { return d.Msg }

// RequireHuman は人の端末からの実行か確かめる(agent の pane からは承認・設定変更をさせない)。what は文面に入れる操作の名前。
func RequireHuman(what string) error {
	pane := os.Getenv("HERDR_PANE_ID")
	if pane == "" {
		return nil
	}
	n, err := PaneOwnerName(pane)
	if err != nil {
		return &Denied{what + ": herdr agent list に失敗したため、人の端末からの実行か確かめられません"}
	}
	if n != "" {
		return &Denied{what + " は人が自分の端末で実行します(agent '" + n + "' の pane からは実行できません)"}
	}
	return nil
}
