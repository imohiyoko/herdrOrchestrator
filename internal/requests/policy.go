package requests

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/perms"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

var reAction = regexp.MustCompile(`^[A-Za-z0-9._:*?@-]{1,128}$`)

// ValidAction は操作名(のパターン)に使える文字か。
func ValidAction(a string) error {
	if !reAction.MatchString(a) {
		return usage("操作名に使えない文字があります: %s", a)
	}
	return nil
}

// CutHash は # から後ろ(注釈)を落とす。
func CutHash(s string) string {
	if i := strings.IndexByte(s, '#'); i >= 0 {
		return s[:i]
	}
	return s
}

// PolicyCheck は policy を old から new に変える前の確かめ(tb の requirePolicyWrite)。nil なら確かめない(人だけが呼ぶとき)。
type PolicyCheck func(old, new string) error

// SetApproval は set_approval: 設定 policy に「* <操作名> <対象> = allow|ask」の行を書く(val は allow か ask)
// (主体・操作名・対象が同じ行があれば書き換え、無ければ末尾に足す)。書いたあとに、もっと厳しい行が当たっていないか確かめる。
// 人だけの確かめ(require_human)は呼ぶ側が先にする。
func SetApproval(db *store.DB, val, pat string, w, stderr io.Writer) error {
	return SetApprovalChecked(db, val, pat, w, stderr, nil)
}

// SetApprovalChecked は SetApproval に、書く前の確かめ check を足したもの。
func SetApprovalChecked(db *store.DB, val, pat string, w, stderr io.Writer, check PolicyCheck) error {
	if err := ValidAction(pat); err != nil {
		return err
	}
	eff := "ask"
	if val == "allow" {
		eff = "allow"
	}
	op, target := perms.SplitAct(pat)
	if target == "" {
		target = "*"
	}
	ops := perms.OpsOf(op)
	if i := slices.Index(ops, "role:change"); i >= 0 { // 誰に許すかは人が決めて policy に書く(役割の名前をコードで決め打ちしない)
		if len(ops) == 1 {
			return refuse("tb %s: role:change は誰に許すかを決めて policy に直接書く(例: @<役割> role:change * = allow。tb config edit policy)", val)
		}
		ops = slices.Delete(slices.Clone(ops), i, i+1)
		fmt.Fprintf(stderr, "tb: role:change は書きません(誰に許すかを決めて policy に直接書く: tb config edit policy)\n")
	}
	if len(ops) == 0 {
		return refuse("tb %s: %s はコマンドの中の操作に当たりません(コマンド cmd:・承認の委任 approve:・権限の変更 policy: は policy に直接書く: tb config edit policy)", val, pat)
	}
	cur, _, has, readErr := config.LookupStrict(db.DB, db.Session, "policy")
	if readErr != "" {
		return refuse("設定 policy を読めないため書きません(%s)", readErr)
	}
	var lines []string
	if has && cur != "" {
		lines = strings.Split(cur, "\n")
	}
	ts := Now()
	var b strings.Builder
	done := map[string]bool{}
	for _, line := range lines {
		l := config.Trim(CutHash(line))
		lhs, _, isRule := strings.Cut(l, "=")
		f := strings.Fields(lhs)
		hit := ""
		if isRule && len(f) == 3 && f[2] == target {
			for _, o := range ops {
				if f[0] == "*" && f[1] == o {
					hit = o
				}
			}
		}
		if hit == "" {
			b.WriteString(line + "\n")
			continue
		}
		if done[hit] {
			continue
		}
		b.WriteString("* " + hit + " " + target + " = " + eff + "   # " + ts + " に人が変更(tb approval " + val + ")\n")
		done[hit] = true
	}
	body := strings.TrimSuffix(b.String(), "\n")
	for _, o := range ops {
		if done[o] {
			continue
		}
		row := "* " + o + " " + target + " = " + eff + "   # " + ts + " に人が追加(tb approval " + val + ")"
		if body == "" {
			body = row
		} else {
			body += "\n" + row
		}
	}
	if bad := perms.ValidatePolicy(body); bad != "" {
		fmt.Fprint(stderr, bad)
		return refuse("policy の本文に問題があるため書きませんでした")
	}
	if check != nil {
		if err := check(cur, body); err != nil {
			return err
		}
	}
	info, _ := config.Info("policy")
	if f := os.Getenv(info.Env); f != "" {
		if err := config.WriteFile(f, body); err != nil {
			var sl *config.ErrSymlink
			if errors.As(err, &sl) {
				return refuse("%s", sl.Error())
			}
			return err
		}
	} else if err := config.DBPut(db.DB, db.Session, "policy", body, val, false, ""); err != nil {
		return err
	}
	if _, err := db.Exec(`INSERT INTO main.approval_changes (herdr_session, pattern, value, created_at) VALUES (?, ?, ?, ?)`, db.Session, pat, val, Now()); err != nil {
		return err
	}
	for _, o := range ops {
		fmt.Fprintf(w, "* %s %s = %s\n", o, target, eff)
	}
	if val == "allow" && !strings.ContainsAny(pat, "*?") {
		if tier, rule := approval.NewJudge(db.DB, db.Session).Authorize(pat, ""); tier != "allow" {
			fmt.Fprintf(stderr, "tb: 注意: もっと厳しい行が当たるため、%s はまだ %s です(%s)\n", pat, tier, rule)
		}
	}
	return nil
}
