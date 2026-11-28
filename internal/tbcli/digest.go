package tbcli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	appdigest "github.com/imohiyoko/herdrorchestrator/internal/app/digest"
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
	in := appdigest.ListReq{JSON: json}
	if did != "" {
		n, err := strconv.ParseInt(did, 10, 64)
		if err != nil || n == 0 { // 0 や桁あふれは無い番号(op の 0 は一覧)
			return die("digest が見つかりません: D-%s", did)
		}
		in.ID = n
	}
	var r appdigest.ListResult
	if err := callAppW(db, app.Human(), io.Discard, "digest.list", in, &r); err != nil { // 主体で中身の変わらない読み取り
		return err
	}
	_, err = fmt.Fprint(w, r.Text)
	return err
}
