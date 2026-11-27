package tickets

// 状態の定義(設定 statuses)を足す・外す中身(tb ticket status add / rm)。

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// StatusLineName は設定 statuses の行の状態の名前(コメントと空行は空)。
func StatusLineName(line string) string {
	f := strings.Fields(config.StripComment(line))
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

// StatusBody は今の本文(設定が無ければ組み込みの 6 つを書式どおりに)。末尾の改行は付けない。
func StatusBody(db *store.DB) string {
	if body, _, ok := config.Lookup(db.DB, db.Session, "statuses"); ok {
		return strings.TrimRight(body, "\n")
	}
	var b strings.Builder
	for i, k := range Statuses {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%-12s %s", k, BuiltinCat(k))
	}
	return b.String()
}

// StatusSource は設定 statuses を読んだところ(db:<セッション> / db:* / ファイル)。
func StatusSource(db *store.DB) string {
	_, src, _ := config.Lookup(db.DB, db.Session, "statuses")
	return src
}

// BadStatuses は、書こうとした本文が設定 statuses の検査に通らなかった(Detail は検査の文言。tb は標準エラーに出す)。
type BadStatuses struct{ Detail string }

func (e *BadStatuses) Error() string { return "statuses の本文に問題があるため書きませんでした" }

// StatusWrite は今読んでいる行(このセッション / 共通 / 明示したファイル)へ書く。設定が無ければ共通の行。how は記録に残す理由。
func StatusWrite(db *store.DB, body, how string) error {
	if msg := ValidateStatuses(body); msg != "" {
		return &BadStatuses{Detail: msg}
	}
	info, _ := config.Info("statuses")
	if f := os.Getenv(info.Env); f != "" {
		return config.WriteFile(f, body)
	}
	return config.DBPut(db.DB, db.Session, "statuses", body, how, StatusSource(db) != "db:"+db.Session, "")
}

// StatusAddBody は状態 name(分類 cat、説明 desc)を足した本文(after があればその直後に)。
func StatusAddBody(db *store.DB, st StatusSet, name, cat, desc, after string) (string, error) {
	if !slices.Contains(Categories, cat) {
		return "", invalid("ticket status add: 分類は %s のどれか: %s", strings.Join(Categories, " "), cat)
	}
	if strings.ContainsAny(desc, "\n#") {
		return "", invalid("ticket status add: 説明に改行と # は使えません")
	}
	if slices.Contains(st.Names, name) {
		return "", refuse("状態 %s はすでにあります(分類 %s)", name, st.Cat(name))
	}
	if after != "" && !slices.Contains(st.Names, after) {
		return "", invalid("ticket status add: --after の状態がありません: %s", after)
	}
	line := name + " " + cat
	if desc != "" {
		line += " = " + desc
	}
	cur := StatusBody(db)
	if after == "" {
		return cur + "\n" + line, nil
	}
	// その状態の行の直後に入れる(書いた順が選択肢と列の順)
	var out []string
	done := false
	for _, l := range strings.Split(cur, "\n") {
		out = append(out, l)
		if !done && StatusLineName(l) == after {
			out = append(out, line)
			done = true
		}
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n"), nil
}

// StatusRmPlan は状態 name を外す前の確かめ: 外したあとの本文と、移すチケット(アラート・提案のチケットを除く)を返す。
// その状態のチケットが残っていて to が無ければ断る。
func StatusRmPlan(db *store.DB, st StatusSet, name, to string) (body string, move []string, err error) {
	if !slices.Contains(st.Names, name) {
		return "", nil, refuse("状態 %s は設定にありません(tb ticket status で一覧)", name)
	}
	if to != "" && to == name {
		return "", nil, invalid("ticket status rm: --to に外す状態そのものは指定できません")
	}
	if to != "" && !slices.Contains(st.Names, to) {
		return "", nil, invalid("status は %s のどれか: %s", strings.Join(st.Names, " "), to)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE status=?`, name).Scan(&n); err != nil {
		return "", nil, err
	}
	// アラート・提案のチケットは DB のトリガーが組み込みの名前で書くので、組み込みの名前を外してもチケットは移さない
	if n > 0 && to == "" {
		ex := st.Main(st.Cat(name))
		if ex == name {
			ex = st.Main("todo")
		}
		return "", nil, refuse("状態 %s のチケットが %d 件あります。--to <状態> で移してから外します(例: tb ticket status rm %s --to %s)", name, n, name, ex)
	}
	var keep []string
	for _, l := range strings.Split(StatusBody(db), "\n") {
		if StatusLineName(l) != name {
			keep = append(keep, l)
		}
	}
	body = strings.TrimRight(strings.Join(keep, "\n"), "\n")
	if msg := ValidateStatuses(body); msg != "" {
		return "", nil, &BadStatuses{Detail: msg}
	}
	if n == 0 {
		return body, nil, nil
	}
	rows, err := db.Query(`SELECT id FROM tasks WHERE status=? ORDER BY id`, name)
	if err != nil {
		return "", nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return "", nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		var x int
		if db.QueryRow(`SELECT 1 FROM alert_details WHERE task_id=?1 UNION ALL SELECT 1 FROM proposal_details WHERE task_id=?1 LIMIT 1`, id).Scan(&x) == nil {
			continue
		}
		move = append(move, id)
	}
	if move == nil {
		move = []string{}
	}
	return body, move, nil
}
