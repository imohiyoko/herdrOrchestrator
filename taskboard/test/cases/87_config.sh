# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- 設定を DB に(schema/013): 環境変数で明示したファイル > このセッションの行 > 共通の行(*) > checkout のファイル > .example
_saved_pane=${HERDR_PANE_ID-}; unset HERDR_PANE_ID
_saved_hs=${HERDR_SESSION-}; unset HERDR_SESSION
_saved_tc=${TB_CONFIG-}; _saved_tl=${TB_LIMITS-}
unset TB_CONFIG TB_LIMITS TB_POLICY_A TB_ROUTES TB_POLICY_G
export TB_DB=$TMP/config.db; rm -f "$TB_DB"
CR=$(cd "$HERE/../config" && pwd)
export TB_CONFIG_DIR=$TMP/cfgdir; mkdir -p "$TB_CONFIG_DIR"   # checkout の設定ファイルの置き場(取り込むと退かすので一時の場所)
_saved_xs=${XDG_STATE_HOME-}; export XDG_STATE_HOME=$TMP/xs
SA() { HERDR_SESSION=sa "$TB" "$@"; }
SB() { HERDR_SESSION=sb "$TB" "$@"; }
src() { awk -v k="$2" '$1==k {print $2}' <<<"$1"; }

l=$(SA config list)
eq "config: 置き場に何も無ければ無し" "$(src "$l" projects) $(src "$l" approvals) $(src "$l" routes)" \
  "none none none"
unset TB_CONFIG_DIR; l=$(SA config list); export TB_CONFIG_DIR=$TMP/cfgdir
eq "config: 既定の置き場では checkout の例を読む(policy の例は既定値にしない)" "$(src "$l" projects) $(src "$l" policy)" "example:$CR/projects.conf.example none"
printf 'pj = from-file\n' >"$TB_CONFIG_DIR/projects.conf"
eq "config: checkout のファイルがあればそれ" "$(src "$(SA config list)" projects)" "file:$TB_CONFIG_DIR/projects.conf"

printf 'a -> b\n' | SA config set routes >/dev/null
eq "config set: このセッションの行に書く" "$(src "$(SA config list)" routes) $(src "$(SB config list)" routes)" "db:sa none"
eq "config show: 本文" "$(SA config show routes 2>/dev/null)" "a -> b"
printf '! x -> y\n' | SB config set routes --shared >/dev/null
eq "config set --shared: 共通の行。このセッションの行があればそちらが先" "$(src "$(SA config list)" routes) $(src "$(SB config list)" routes) $(SB config show routes 2>/dev/null)" "db:sa db:* ! x -> y"

# 認可(policy)も DB から引く。tb allow はこのセッションの行に書く(今効いている本文を元にする)
printf '# 共通\n* send:* * = allow\n' | SA config set policy --shared >/dev/null
eq "policy: 共通の行で判定" "$(SA approval send:task:w | head -1) $(SB approval send:task:w | head -1)" "allow allow"
SA allow 'agent:stop:*' >/dev/null
eq "tb allow: このセッションの行に書き、ほかのセッションは変わらない" "$(SA approval agent:stop:w | head -1) $(SB approval agent:stop:w | head -1) $(src "$(SA config list)" policy) $(src "$(SB config list)" policy)" \
  "allow ask db:sa db:*"
eq "tb allow: 元の本文(共通の行)を引き継ぎ、新しい行は末尾" "$(SA config show policy 2>/dev/null | grep -v '^#' | sed 's/ *#.*//' | tr '\n' '|')" "* send:* * = allow|* agent:stop * = allow|"
SA ask 'agent:stop:*' >/dev/null
eq "tb ask: 同じ行を書き換える" "$(SA approval agent:stop:w | head -1) $(SA config show policy 2>/dev/null | grep -c 'agent:stop')" "ask 1"
eq "config history: 変更が残る" "$(SA config history policy | cut -f2,3 | tr '\t\n' ': ')" "sa:ask sa:allow *:set "

# 送り先(projects)も DB から
printf 'pj = cond-shared\n' | SA config set projects --shared >/dev/null
printf 'pj = cond-sb\n' | SB config set projects >/dev/null
tid=$(SA add -p pj -t 'x'); tid2=$(SB add -p pj -t 'y')
SA claim "$tid" --owner w >/dev/null; SA done "$tid" -d a.md -m s >/dev/null
SB claim "$tid2" --owner w >/dev/null; SB done "$tid2" -d a.md -m s >/dev/null
eq "projects: 報告の宛先はセッションの設定で決まる" "$(SA pending | cut -f3) $(SB pending | cut -f3)" "cond-shared cond-sb"

SB config unset routes --shared >/dev/null
eq "config unset: 消すと次の出どころに戻る" "$(src "$(SB config list)" routes) $(src "$(SA config list)" routes)" "none db:sa"

# 環境変数でファイルを明示しているときはファイルを書き、DB は使わない
printf 'p -> q\n' | TB_ROUTES=$TMP/cfg-routes.conf SA config set routes >/dev/null
eq "config set: 明示したファイルを書く" "$(cat "$TMP/cfg-routes.conf") $(src "$(TB_ROUTES=$TMP/cfg-routes.conf SA config list)" routes) $(SA config show routes 2>/dev/null)" \
  "p -> q file:$TMP/cfg-routes.conf a -> b"
eq "config unset --shared: 明示したファイルとは併用できない" "$(TB_ROUTES=$TMP/cfg-routes.conf rc SA config unset routes --shared)" "1"

# 人の端末だけ
eq "config set: agent の pane からは断る" "$(printf 'x\n' | FAKE_AGENTS='[{"pane_id":"wK:p1","name":"wk"}]' HERDR_PANE_ID=wK:p1 rc SA config set routes)" "1"
eq "config: 不明なキー" "$(rc SA config show nope)" "2"

# 取り込み: checkout のファイルを共通の行にし、checkout から退かす(既にある共通の行は飛ばす)。dry-run は書かない
SA config unset projects --shared >/dev/null
printf '* send:* * = deny\n' >"$TB_CONFIG_DIR/policy.conf"
out=$(SA config import --dry-run)
eq "config import --dry-run: 書かず、退かさない" "$(grep -c 'policy:.*飛ばします' <<<"$out") $(grep -c 'projects:.*取り込みます' <<<"$out") $(sqlite3 "$TB_DB" "SELECT COUNT(*) FROM config WHERE herdr_session='*';") $(ls "$TB_CONFIG_DIR" | tr '\n' ' ')" \
  "1 1 1 policy.conf projects.conf "
SA config import >/dev/null
eq "config import: 共通の行が無いキーだけ、ファイルの本文のまま取り込む" "$(sqlite3 "$TB_DB" "SELECT key||'='||body FROM config WHERE herdr_session='*' ORDER BY key;" | tr '\n' ' ')" \
  "policy=# 共通 * send:* * = allow projects=pj = from-file "
eq "config import: 取り込んだファイルは checkout から退かし、取り込まなかったものは残す" "$(ls "$TB_CONFIG_DIR" | tr '\n' ' ')/$(ls "$XDG_STATE_HOME"/herdr/config-imported/*/ | tr '\n' ' ')" "policy.conf /projects.conf "
eq "config import: 2 回目は何もしない" "$(SA config import | grep -c '取り込み、')" "0"


# 検査の文面(bash と Go で同じ。キーごとに不正な本文を書き、標準エラーの全文を比べる)
cfgbad() { printf '%b' "$2" | SA config set "$1" 2>&1 >/dev/null; echo "rc=$?"; }
eq $'config set messages: validation message (1)' "$(cfgbad messages $'nudge.text = hi {bogus}\\nno-equals\\nunknown.code = x\\nalert.analyze =\\nidentity.no-pane = {id}')" $'1 行目: nudge.text では {bogus} は使えません(使えるのは: task title minutes status active)\n2 行目: <識別子> = <文面> の形ではありません\n3 行目: 知らない識別子です: unknown.code(使えるのは: alert.analyze identity.herdr-failed identity.no-pane nudge.multi nudge.pool nudge.start nudge.text role.change.needs-approval send.blocked.notice send.denied.default spawn.denied)\n4 行目: 文面が空です: alert.analyze\n5 行目: identity.no-pane では {id} は使えません(使えるのは: なし)\ntb: messages の本文に問題があるため書きませんでした\nrc=1'
eq $'config set statuses: validation message (2)' "$(cfgbad statuses $'todo todo\\nin_progress done\\nfoo\\nbad,name active\\nx weird\\ntodo todo\\n')" $'2 行目: 組み込みの状態 in_progress の分類は active だけです(別の分類にするなら別の名前にする): done\n3 行目: <状態> <分類> [= 説明] の形ではありません: foo\n4 行目: 状態は 1〜30 文字で、カンマ・引用符・* ? [ ] を含まない: bad,name\n5 行目: 分類は todo active review blocked done cancelled のどれか: weird\n6 行目: 状態が重複しています: todo\ntb: statuses の本文に問題があるため書きませんでした\nrc=1'
eq $'config set statuses: validation message (3)' "$(cfgbad statuses $'# only comments\\n')" $'状態が 1 つもありません(設定を消すなら tb config unset statuses)\ntb: statuses の本文に問題があるため書きませんでした\nrc=1'
eq $'config set statuses: validation message (4)' "$(cfgbad statuses $'todo todo\\nreview review')" $'分類 active の状態が 1 つもありません\n分類 done の状態が 1 つもありません\ntb: statuses の本文に問題があるため書きませんでした\nrc=1'
eq $'config set labels: validation message (5)' "$(cfgbad labels $'ok\\nok\\na b\\naaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\\nあああああああああああああああああああああああああああああああああああああああ')" $'2 行目: ラベルが重複しています: ok\n3 行目: ラベルは 1〜40 文字で、空白・カンマ・引用符を含まない: a b\n4 行目: ラベルは 1〜40 文字で、空白・カンマ・引用符を含まない: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\ntb: labels の本文に問題があるため書きませんでした\nrc=1'
eq $'config set system-labels: validation message (6)' "$(cfgbad system-labels $'deadline = d\\nteam = t\\nnope = x\\ndeadline = dd\\nbad')" $'2 行目: team のラベルには {team} を含める: t\n3 行目: 知らない用途です: nope(使えるのは: deadline team)\n4 行目: 用途が重複しています: deadline\n5 行目: <用途> = <ラベル> の形ではありません: bad\ntb: system-labels の本文に問題があるため書きませんでした\nrc=1'
eq $'config set notify-rules: validation message (7)' "$(cfgbad notify-rules $'created -> file\\nfoo -> file\\ncreated -> a -> b\\ncreated type=nope -> file\\ncreated priority>=high owner>=x -> file\\ncreated project=a-b -> file\\ncreated -> nowhere\\nstatus x -> file\\ncreated label=a,b -> file\\ncomment -> \\nno arrow\\ncreated new=a#7 -> file # c')" $'notify-rules: 2 行目: 不明なイベントの種類 foo(* か created status title type priority owner due parent description label_added label_removed ref_added ref_removed link unlink comment overdue due_soon pr_opened pr_approved pr_changes pr_merged)\n3 行目: -> が 2 つあります\n4 行目: type は epic story task bug subtask のどれか: nope\n5 行目: >= は priority だけ: owner>=x\n6 行目: project は英数字と _: a-b\n7 行目: 使えない届け先 nowhere(file stdout slack webui conductor)\n8 行目: 条件は <キー>=<値>: x\n9 行目: 値が不正(空白・引用符・記号は使えない): label=a,b\n10 行目: 届け先がありません\n11 行目: -> がありません\ntb: notify-rules の本文に問題があるため書きませんでした\nrc=1'
eq $'config set nudge: validation message (8)' "$(cfgbad nudge $'ignore @hr\\nwake done\\nwake bogus\\nwake a*b\\nfoo bar')" $'2 行目: 閉じた状態(done / cancelled の分類)は催促できません: done\n4 行目: wake の状態の名前に使えない文字があります: a*b\n5 行目: ignore <@役割の glob | agent 名の glob> か wake <分類 | 状態> ... の形ではありません: foo bar\ntb: nudge の本文に問題があるため書きませんでした\nrc=1'
eq $'config set nudge: validation message (9)' "$(cfgbad nudge $'wake nosuchstate')" $'wake: 分類にも状態(設定 statuses)にもない名前です: nosuchstate\ntb: nudge の本文に問題があるため書きませんでした\nrc=1'
eq $'config set protect: validation message (10)' "$(cfgbad protect $'ok\\nrole:ok\\nbad name\\n')" $'3 行目: <名前の glob> か role:<役割の glob> の形ではありません: bad name\ntb: protect の本文に問題があるため書きませんでした\nrc=1'
eq $'config set role-models: validation message (11)' "$(cfgbad role-models $'worker = sonnet\\nbad\\nw r = m\\nx = bad model')" $'2 行目: <役割の glob> = <モデル>(または name:<agent 名の glob> = <モデル>)の形ではありません\n3 行目: 役割(または name:<agent 名>)の glob に使えない文字があります: w r\n4 行目: モデル名に使えない文字があります: bad model\ntb: role-models の本文に問題があるため書きませんでした\nrc=1'
eq $'config set org: validation message (12)' "$(cfgbad org $'a -> b\\nnoarrow\\n@x y -> B\\nc -> ~parent')" $'2 行目: <部下のパターン> -> <上司> の形ではありません: noarrow\n3 行目: 部下は agent 名の glob か @<役割>: @x y\n3 行目: 上司は agent 名か ~parent(glob・@役割は書けない): B\ntb: org の本文に問題があるため書きませんでした\nrc=1'


if [[ -n $_saved_hs ]]; then export HERDR_SESSION=$_saved_hs; fi
export HERDR_PANE_ID=$_saved_pane TB_CONFIG=$_saved_tc TB_LIMITS=$_saved_tl
unset TB_CONFIG_DIR; if [[ -n $_saved_xs ]]; then export XDG_STATE_HOME=$_saved_xs; else unset XDG_STATE_HOME; fi
