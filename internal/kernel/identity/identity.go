// Package identity は呼び出し元が誰か(人か、どの agent か)を herdr から引く(ADR 0004 の土台。taskboard/lib/identity.sh)。
// 自己申告は使わない。
package identity

import (
	"os"
	"regexp"

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

// Label は resolve_label が herdr から引いた報告者。
type Label struct{ Name, Pane, Session string }

var (
	reLabelName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	reLabelPane = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
)

// ResolveLabel は resolve_label: HERDR_PANE_ID の agent を herdr から引く(自己申告にしない)。
// 引けなければ理由(bash の stderr の 1 行。tb: を含む)を msg に入れて ok=false。msgf は設定 messages の文面(config.Msg)。
func ResolveLabel(msgf func(code, def string) string) (l Label, msg string, ok bool) {
	pane := os.Getenv("HERDR_PANE_ID")
	if pane == "" {
		return l, "tb: [identity.no-pane] " + msgf("identity.no-pane", "HERDR_PANE_ID がありません。報告者を特定できないため保存しません"), false
	}
	out, err := herdr.Call("agent", "list")
	if err != nil {
		return l, "tb: [identity.herdr-failed] " + msgf("identity.herdr-failed", "herdr agent list に失敗。報告者を特定できないため保存しません"), false
	}
	as, _ := herdr.ParseAgents(out) // 読めなければ該当なし(bash の jq の失敗と同じ)
	var a *herdr.Agent
	for i := range as {
		if as[i].PaneID == pane {
			a = &as[i]
			break
		}
	}
	if a == nil {
		return l, "tb: pane " + pane + " に該当する agent がありません。保存しません", false
	}
	l = Label{Name: a.Name, Pane: pane, Session: a.AgentSession.Value}
	switch {
	case l.Name == "":
		return l, "tb: pane " + pane + " の agent に名前がありません(herdr agent rename)。保存しません", false
	case l.Session == "":
		return l, "tb: pane " + pane + " の agent に session がありません。保存しません", false
	case !reLabelName.MatchString(l.Name) || !reLabelPane.MatchString(l.Pane):
		return l, "tb: 報告者名/pane に使えない文字があります。保存しません", false
	}
	return l, "", true
}
