# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- 設定を DB に(schema/013): 環境変数で明示したファイル > このセッションの行 > 共通の行(*) > checkout のファイル > .example
_saved_pane=${HERDR_PANE_ID-}; unset HERDR_PANE_ID
_saved_hs=${HERDR_SESSION-}; unset HERDR_SESSION
_saved_tc=${TB_CONFIG-}; _saved_tl=${TB_LIMITS-}
unset TB_CONFIG TB_LIMITS TB_APPROVALS TB_ROUTES TB_GRANTS
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
eq "config: 既定の置き場では checkout の例を読む" "$(src "$l" projects) $(src "$l" approvals)" "example:$CR/projects.conf.example example:$CR/approvals.conf.example"
printf 'pj = from-file\n' >"$TB_CONFIG_DIR/projects.conf"
eq "config: checkout のファイルがあればそれ" "$(src "$(SA config list)" projects)" "file:$TB_CONFIG_DIR/projects.conf"

printf 'a -> b\n' | SA config set routes >/dev/null
eq "config set: このセッションの行に書く" "$(src "$(SA config list)" routes) $(src "$(SB config list)" routes)" "db:sa none"
eq "config show: 本文" "$(SA config show routes 2>/dev/null)" "a -> b"
printf '! x -> y\n' | SB config set routes --shared >/dev/null
eq "config set --shared: 共通の行。このセッションの行があればそちらが先" "$(src "$(SA config list)" routes) $(src "$(SB config list)" routes) $(SB config show routes 2>/dev/null)" "db:sa db:* ! x -> y"

# 承認ルールも DB から引く。tb allow はこのセッションの行に書く(今効いている本文を元にする)
printf '# 共通\nsend:* = allow\n* = ask\n' | SA config set approvals --shared >/dev/null
eq "approvals: 共通の行で判定" "$(SA approval send:task:w | head -1) $(SB approval send:task:w | head -1)" "allow allow"
SA allow 'agent:stop:*' >/dev/null
eq "tb allow: このセッションの行に書き、ほかのセッションは変わらない" "$(SA approval agent:stop:w | head -1) $(SB approval agent:stop:w | head -1) $(src "$(SA config list)" approvals) $(src "$(SB config list)" approvals)" \
  "allow ask db:sa db:*"
eq "tb allow: 元の本文(共通の行)を引き継ぎ、新しい規則は最初の規則の前" "$(SA config show approvals 2>/dev/null | grep -v '^#' | sed 's/ *#.*//' | tr '\n' '|')" "agent:stop:* = allow|send:* = allow|* = ask|"
SA ask 'agent:stop:*' >/dev/null
eq "tb ask: 同じ行を書き換える" "$(SA approval agent:stop:w | head -1) $(SA config show approvals 2>/dev/null | grep -c 'agent:stop')" "ask 1"
eq "config history: 変更が残る" "$(SA config history approvals | cut -f2,3 | tr '\t\n' ': ')" "sa:ask sa:allow *:set "

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
printf 'x = allow\n' >"$TB_CONFIG_DIR/approvals.conf"
out=$(SA config import --dry-run)
eq "config import --dry-run: 書かず、退かさない" "$(grep -c 'approvals:.*飛ばします' <<<"$out") $(grep -c 'projects:.*取り込みます' <<<"$out") $(sqlite3 "$TB_DB" "SELECT COUNT(*) FROM config WHERE herdr_session='*';") $(ls "$TB_CONFIG_DIR" | tr '\n' ' ')" \
  "1 1 1 approvals.conf projects.conf "
SA config import >/dev/null
eq "config import: 共通の行が無いキーだけ、ファイルの本文のまま取り込む" "$(sqlite3 "$TB_DB" "SELECT key||'='||body FROM config WHERE herdr_session='*' ORDER BY key;" | tr '\n' ' ')" \
  "approvals=# 共通 send:* = allow * = ask projects=pj = from-file "
eq "config import: 取り込んだファイルは checkout から退かし、取り込まなかったものは残す" "$(ls "$TB_CONFIG_DIR" | tr '\n' ' ')/$(ls "$XDG_STATE_HOME"/herdr/config-imported/*/ | tr '\n' ' ')" "approvals.conf /projects.conf "
eq "config import: 2 回目は何もしない" "$(SA config import | grep -c '取り込み、')" "0"

if [[ -n $_saved_hs ]]; then export HERDR_SESSION=$_saved_hs; fi
export HERDR_PANE_ID=$_saved_pane TB_CONFIG=$_saved_tc TB_LIMITS=$_saved_tl
unset TB_CONFIG_DIR; if [[ -n $_saved_xs ]]; then export XDG_STATE_HOME=$_saved_xs; else unset XDG_STATE_HOME; fi
