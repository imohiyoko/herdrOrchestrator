package tbcli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	appdigest "github.com/imohiyoko/herdrorchestrator/internal/app/digest"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// まとめ送信(tb digest は app/digest の digest.run)と、まとめの一覧(tb digests)。

// strRows は sql -separator の行(NULL は空文字)。読み終えてから返す(1 本の接続のため)。
func strRows(db *store.DB, q string, args ...any) [][]string {
	var out [][]string
	_ = qrows(db.DB, q, args, func(_ []string, vals []any) error {
		r := make([]string, len(vals))
		for i, v := range vals {
			r[i] = sqlText(v)
		}
		out = append(out, r)
		return nil
	})
	return out
}

func firstJSONObj(out []byte) map[string]any {
	var v any
	if json.NewDecoder(strings.NewReader(string(out))).Decode(&v) != nil {
		return nil
	}
	m, _ := v.(map[string]any)
	return m
}

// cmdDigest は tb digest [--dry-run](digest.run)。
func cmdDigest(args []string, w, stderr io.Writer) error {
	dry := false
	for _, a := range args {
		if a != "--dry-run" {
			return usage("digest: 不明な引数 %s", a)
		}
		dry = true
	}
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	var r appdigest.RunResult
	if err := callAppNoHerdr(db, "tb digest", stderr, "digest.run", appdigest.RunReq{DryRun: dry}, &r); err != nil {
		return err
	}
	for _, l := range r.Lines {
		fmt.Fprintln(w, l)
	}
	if r.Code != 0 {
		return &exit{r.Code, ""}
	}
	return nil
}

// cmdDigests は tb digests [D-<n>] [--json]。
func cmdDigests(args []string, w, stderr io.Writer) error {
	did := ""
	if len(args) > 0 && strings.HasPrefix(args[0], "D-") {
		did = args[0][2:]
		args = args[1:]
		if !reDigitsOnly.MatchString(did) {
			return usage("digest ID は D-<数字>")
		}
	}
	json := false
	for _, a := range args {
		if a != "--json" {
			return usage("digests: 不明な引数 %s", a)
		}
		json = true
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	withDid := func(o jv.Obj) jv.Obj { return o.Set("id", "D-"+jv.Scalar(o.Get("id"))) }
	if did != "" {
		rows, _ := sqlObjs(db.DB, nil, `SELECT * FROM digests WHERE id=?`, did)
		if len(rows) == 0 {
			return die("digest が見つかりません: D-%s", did)
		}
		o := rows[0]
		if json {
			fmt.Fprintln(w, jv.Compact(withDid(o)))
		} else {
			fmt.Fprintf(w, "D-%s → %s (%s 件, %s)\n%s\n", jqStr(o.Get("id")), jqStr(o.Get("target")), jqStr(o.Get("item_count")), jqStr(o.Get("created_at")), jqStr(o.Get("body")))
		}
		return nil
	}
	if json {
		rows, err := sqlObjs(db.DB, nil, `SELECT * FROM digests ORDER BY id DESC LIMIT 50`)
		if err != nil {
			return err
		}
		for _, o := range rows {
			fmt.Fprintln(w, jv.Compact(withDid(o)))
		}
		return nil
	}
	for _, r := range strRows(db, `SELECT 'D-'||id, target, item_count, created_at FROM digests ORDER BY id DESC LIMIT 50`) {
		fmt.Fprintln(w, strings.Join(r, "\t"))
	}
	return nil
}
