// Package requests は承認待ち(表 requests、ID は Q-<n>。ADR 0007 の段 4)を決める中身。
// 送信(outbox)と agent の操作(agent_ops)は元の表に pending の行ができるとトリガーがここに足す(状態の正は元の行)。
// ask で止まったコマンド(source=cmd)は中身(引数と標準入力)ごと積まれ、承認されたら依頼者の権限で機械的に実行する。
// tb approval と Web UI の承認の画面が、アプリケーション API(internal/app/approval)を通して同じものを使う(ADR 0008)。
package requests

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/transport"
)

// Refused は断り(tb は 1、Usage なら使い方の誤りで 2)。Msg は tb の文言(「tb: 」は付けない)。
type Refused struct {
	Msg   string
	Usage bool
}

func (e *Refused) Error() string { return e.Msg }

func refuse(format string, a ...any) error { return &Refused{Msg: fmt.Sprintf(format, a...)} }

func usage(format string, a ...any) error {
	return &Refused{Msg: fmt.Sprintf(format, a...), Usage: true}
}

// Now は記録に残す時刻(UTC の ISO 8601、秒まで)。
var Now = func() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }

var (
	ReQID = regexp.MustCompile(`^Q-[0-9]+$`)
	ReOID = regexp.MustCompile(`^O-[0-9]+$`)
)

// Request は requests の 1 行。
type Request struct {
	ID                    int64
	Source                string
	SourceID              sql.NullInt64
	Requester, Op, Target string
	Priority              int
	Payload               sql.NullString
	Status                string
	DecidedBy             sql.NullString
	Tier                  string
}

// QID は Q-<n>。
func (r Request) QID() string { return "Q-" + strconv.FormatInt(r.ID, 10) }

// Act は承認の権限の行(approve:<act> <依頼者>)に使う操作名(対象が * なら操作名だけ)。
func (r Request) Act() string {
	if r.Target == "" || r.Target == "*" {
		return r.Op
	}
	return r.Op + ":" + r.Target
}

// Load は Q-<id> の行。
func Load(db *sql.DB, id int64) (Request, bool, error) {
	var r Request
	err := db.QueryRow(`SELECT id, source, source_id, requester, op, target, priority, payload, status, decided_by, tier FROM requests WHERE id=?`, id).
		Scan(&r.ID, &r.Source, &r.SourceID, &r.Requester, &r.Op, &r.Target, &r.Priority, &r.Payload, &r.Status, &r.DecidedBy, &r.Tier)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

// Num は "Q-<n>" / "O-<n>" / "L-<n>" の数(読めなければ -1)。
func Num(s string) int64 {
	if len(s) < 3 || s[1] != '-' {
		return -1
	}
	n, err := strconv.ParseInt(s[2:], 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// CmdPayload は積んだコマンドの中身。
type CmdPayload struct {
	Args  []string `json:"args"`
	Stdin string   `json:"stdin"`
}

// Cmd は積んだコマンドの中身を読む(会話の文面を残していない行・読めない行は断る)。
func (r Request) Cmd() (CmdPayload, error) {
	var pl CmdPayload
	var red struct {
		Redacted bool `json:"redacted"`
	}
	if r.Payload.Valid && json.Unmarshal([]byte(r.Payload.String), &red) == nil && red.Redacted {
		return pl, refuse("%s は会話の文面を含みうるため中身を残していません(承認しても実行できません。人が自分の端末で実行するか、却下してください)", r.QID())
	}
	if !r.Payload.Valid || json.Unmarshal([]byte(r.Payload.String), &pl) != nil || len(pl.Args) == 0 {
		return pl, refuse("%s の中身を読めません", r.QID())
	}
	return pl, nil
}

// DetailLine は知らせの文面に入れる依頼の中身(1 行、長ければ切る)。
func (r Request) DetailLine() string {
	var d string
	if pl, err := r.Cmd(); err == nil {
		d = "tb " + strings.Join(pl.Args, " ")
	} else {
		d = r.Op
	}
	d = strings.ReplaceAll(d, "\n", " ")
	if len([]rune(d)) > 200 {
		d = string([]rune(d)[:200]) + "…"
	}
	return d
}

// ConvWords は tb conv の呼び出しなら、文面を含まないコマンドの語(conv <サブコマンド> / conv-<名前>)を返す。
func ConvWords(args []string) ([]string, bool) {
	if len(args) == 0 {
		return nil, false
	}
	if strings.HasPrefix(args[0], "conv-") {
		return args[:1], true
	}
	if args[0] == "conv" {
		if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
			return args[:2], true
		}
		return args[:1], true
	}
	return nil, false
}

// HandBack は承認の結果を依頼者に返して、依頼者が自分で実行する形にするか(pane のある agent だけ)。
func HandBack(requester string) bool {
	return requester != "human" && requester != "" && !strings.HasPrefix(requester, "system:")
}

// AgentAlive は名前の agent が herdr にいるか。
func AgentAlive(name string) (bool, error) {
	as, err := herdr.Agents()
	if err != nil {
		return false, err
	}
	for _, a := range as {
		if a.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// NotifyRequester は決まったことを依頼した agent の pane に直接 1 通送る(tb からの知らせ。agent 同士の送信の経路 routes / delivery は通さない)。
// 記録は messages(kind=approval、ref=Q-<n>)。宛先が使用中・不在なら送らずに false(依頼者は tb approval history --mine で見られる)。
// 人・system: の依頼者には送らない。by は決めた者。
func NotifyRequester(db *store.DB, r Request, by, text string) bool {
	if !HandBack(r.Requester) {
		return true
	}
	msg := "[tb approval / " + r.QID() + " / " + by + "] " + text
	if transport.Send(db.DB, db.Session, r.Requester, msg, r.QID(), "approval", discard{}) != transport.Sent {
		return false
	}
	_, _ = db.Exec(`UPDATE main.requests SET notified_at=? WHERE id=?`, Now(), r.ID)
	return true
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// RecordDecision は送信・agent の操作の承認待ち(Q-<n>)に、決めた結果と誰が決めたかを残す(元の行の状態とは別に、Q の記録として)。
// 元の行を決めたあとに呼ぶ。Q-<n> が無い(承認待ちを通らなかった)行なら何もしない。
func RecordDecision(db *store.DB, source string, sourceID int64, status, by string) {
	_, _ = db.Exec(`UPDATE main.requests SET status=?, decided_by=?, decided_at=? WHERE herdr_session=? AND source=? AND source_id=?`,
		status, by, Now(), db.Session, source, sourceID)
}

// Approver は approver_check の結果(human か agent の名前と根拠)。
type Approver struct{ Name, Rule string }

// Label は記録の文言(人 / agent <名前>(<根拠>))。
func (a Approver) Label() string {
	if a.Name == "human" {
		return "人"
	}
	return "agent " + a.Name + "(" + a.Rule + ")"
}

// IsHuman は who(perms の主体の書き方)が承認では人として扱われるか(人の端末・scheduler などの system:<名前>)。
func IsHuman(who string) bool {
	return who == "" || who == "human" || strings.HasPrefix(who, "system:")
}

// CheckApprover は approver_check <コマンド名> <元の操作名(空 = 人だけ)> <依頼者>: who が決めてよいか。
func CheckApprover(j *approval.Judge, cmd, who, act, by string) (Approver, error) {
	if IsHuman(who) {
		return Approver{Name: "human"}, nil
	}
	if act == "" {
		return Approver{}, refuse("%s: この承認待ちは人が自分の端末で実行します(agent '%s' の pane からは承認できません)", cmd, who)
	}
	if who == by {
		return Approver{}, refuse("%s: 自分の依頼は承認できません(agent '%s')。人が自分の端末で実行します", cmd, who)
	}
	rule, ok := j.GrantOf(who, "approve:"+act, by)
	if !ok {
		return Approver{}, refuse("%s は人が自分の端末で実行します(agent '%s' には %s からの %s を承認する許可がありません。policy に '<承認者> approve:<操作> <依頼者> = allow' の行が要ります)", cmd, who, by, act)
	}
	return Approver{who, rule}, nil
}
