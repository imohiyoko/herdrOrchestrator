# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- 設定 messages: agent に返す文面の差し替え(msg / msg_err)と、tb config set messages の検査
# project は msg だけを使う
_saved_pane=${HERDR_PANE_ID-}; _saved_db=$TB_DB
_had_msgs=${TB_MESSAGES+1}; _saved_msgs=${TB_MESSAGES-}; _had_fake=${FAKE_AGENTS+1}; _saved_fake=${FAKE_AGENTS-}
unset HERDR_PANE_ID TB_MESSAGES
export TB_DB=$TMP/messages.db
mt=$($TB add -p msg -t "messages の確認")

# 書かなければ組み込みの文面。識別子は stderr の先頭に付く
eq "messages: built-in text with the code when nothing is set" "$(HERDR_PANE_ID= $TB comment "$mt" --kind progress --text x 2>&1 >/dev/null | head -1)" "tb: [identity.no-pane] HERDR_PANE_ID がありません。報告者を特定できないため保存しません"
eq "messages: the exit code is unchanged" "$(HERDR_PANE_ID= rc $TB comment "$mt" --kind progress --text x)" "3"

# 検査: 知らない識別子・使えない {名前}・空の文面・形の誤りは書かない
eq "config set messages: unknown code refused" "$(printf 'no.such = x\n' | rc $TB config set messages)" "1"
eq "config set messages: unknown {name} for the code refused" "$(printf 'nudge.multi = {title}\n' | rc $TB config set messages)" "1"
eq "config set messages: empty text refused" "$(printf 'spawn.denied =\n' | rc $TB config set messages)" "1"
eq "config set messages: a line without = refused" "$(printf 'spawn.denied x\n' | rc $TB config set messages)" "1"
eq "config set messages: nothing written by refusals" "$($TB config list | grep '^messages' | grep -c 'db:')" "0"

# 差し替え: 後の行が勝つ。{名前} が値になる。# の行は注釈
printf '# 注釈\nidentity.no-pane = 一つ目\nidentity.no-pane = pane が分かりません。tb は人の端末か agent の pane から\nsend.denied.default = {sender} からは送れません。チケットにしてください\n' | $TB config set messages >/dev/null
eq "config set messages: a valid body is stored in the DB" "$($TB config list | grep '^messages' | grep -c 'db:')" "1"
eq "messages: the configured text replaces the built-in one (last line wins)" "$(HERDR_PANE_ID= $TB comment "$mt" --kind progress --text x 2>&1 >/dev/null | head -1)" "tb: [identity.no-pane] pane が分かりません。tb は人の端末か agent の pane から"
ms_ag() { printf '{"pane_id":"w%s:p1","name":"%s","agent_status":"idle","agent_session":{"value":"s-%s"}}' "$1" "$1" "$1"; }
export FAKE_AGENTS="[$(ms_ag msgw1),$(ms_ag msgw2)]"
out=$(HERDR_PANE_ID=wmsgw1:p1 $TB send msgw2 --kind info --text hi 2>/dev/null | tail -1)
oid=${out%% *}
eq "messages: {sender} is filled in the delivery reason" "$(sqlite3 "$TB_DB" "SELECT reason LIKE 'default: msgw1 からは送れません。チケットにしてください%' FROM outbox WHERE id=${oid#O-};")" "1"

# 環境変数で明示したファイルが DB より先(ほかの設定と同じ)
printf 'identity.no-pane = file の文面\n' >"$TMP/messages.conf"; export TB_MESSAGES=$TMP/messages.conf
eq "messages: TB_MESSAGES file wins over the DB" "$(HERDR_PANE_ID= $TB comment "$mt" --kind progress --text x 2>&1 >/dev/null | head -1)" "tb: [identity.no-pane] file の文面"
unset TB_MESSAGES
$TB config unset messages >/dev/null

if [[ -n $_had_msgs ]]; then export TB_MESSAGES=$_saved_msgs; else unset TB_MESSAGES; fi
if [[ -n $_had_fake ]]; then export FAKE_AGENTS=$_saved_fake; else unset FAKE_AGENTS; fi
export TB_DB=$_saved_db
if [[ -n $_saved_pane ]]; then export HERDR_PANE_ID=$_saved_pane; else unset HERDR_PANE_ID; fi
