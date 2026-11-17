# shellcheck shell=bash
# run.sh から番号順に source される
# --- whoami: 今の向き先と決まり方を出す。DB を開かない・何も書かない・herdr を呼ばない
W() { env -u HERDR_SESSION -u HERDR_SOCKET_PATH -u HERDR_PANE_ID -u TB_INSTANCE -u TB_DB "$@"; }   # 呼び出し元の環境に左右されない
wf() { grep -E "^$2 " <<<"$1" | head -1; }                                                           # 行の取り出し(awk を使わない)
SOCK_STG=/h/.config/herdr/sessions/stg/herdr.sock
ROOT_REAL=$(cd "$HERE/../.." && pwd -P)

# 既定: 何も設定しない
out=$(W $TB whoami); r=$?
eq "whoami: exits 0" "$r" "0"
eq "whoami: checkout is the physical repo root" "$(wf "$out" checkout | grep -c "^checkout  *$ROOT_REAL   \[checkout\]$")" "1"
eq "whoami: default db is the shared one" "$(W XDG_STATE_HOME=/xs $TB whoami | grep -E '^db ' | grep -c "/xs/herdr/tasks.db   \[既定")" "1"
eq "whoami: session defaults" "$(wf "$out" session | tr -s ' ')" "session default [既定]"
eq "whoami: no TB_INSTANCE -> legacy label prefix" "$(wf "$out" label-prefix | tr -s ' ')" "label-prefix local.herdr- [既定]"
eq "whoami: instance unset" "$(wf "$out" instance | tr -s ' ')" "instance (未設定) [既定]"
eq "whoami: human shell" "$(wf "$out" pane | grep -c 'なし')" "1"
eq "whoami: default session, no instance -> no note" "$(grep -c '^note:' <<<"$out")" "0"

# DB: TB_DB は env。実体(symlink)も出す
ln -sf "$TMP/real.db" "$TMP/alias.db"
out=$(W TB_DB="$TMP/alias.db" $TB whoami)
eq "whoami: TB_DB from env" "$(wf "$out" db | grep -c "$TMP/alias.db   \[env TB_DB\]")" "1"
eq "whoami: symlinked DB shows its real path" "$(grep -c "実体: $(cd "$TMP" && pwd -P)/real.db" <<<"$out")" "1"

# セッション: socket > HERDR_SESSION > 既定(herdr_session_name と同じ決め方)
eq "whoami: HERDR_SESSION" "$(W HERDR_SESSION=stg $TB whoami | grep '^session' | tr -s ' ')" "session stg [env HERDR_SESSION]"
eq "whoami: socket of a named session" "$(W HERDR_SOCKET_PATH=$SOCK_STG $TB whoami | grep '^session' | tr -s ' ')" "session stg [env HERDR_SOCKET_PATH]"
out=$(W HERDR_SOCKET_PATH=$SOCK_STG HERDR_SESSION=other $TB whoami)
eq "whoami: socket wins; the losing HERDR_SESSION is noted" "$(wf "$out" session | tr -s ' ') $(grep -c '^note: HERDR_SESSION=other .*負けて' <<<"$out")" "session stg [env HERDR_SOCKET_PATH] 1"
eq "whoami: default socket" "$(W HERDR_SOCKET_PATH=/h/.config/herdr/herdr.sock HERDR_SESSION=other $TB whoami | grep '^session' | tr -s ' ')" "session default [env HERDR_SOCKET_PATH]"
out=$(W HERDR_SOCKET_PATH=/x/other.sock HERDR_SESSION=stg $TB whoami)
eq "whoami: unknown socket layout falls back to HERDR_SESSION and says so" "$(wf "$out" session | tr -s ' ') $(grep -c '^note: HERDR_SOCKET_PATH.*導けない' <<<"$out")" "session stg [env HERDR_SESSION] 1"
eq "whoami: unknown socket layout alone -> default" "$(W HERDR_SOCKET_PATH=/x/other.sock $TB whoami | grep '^session' | tr -s ' ')" "session default [既定]"
eq "whoami: --short is the same name as herdr_session_name" "$(W HERDR_SOCKET_PATH=$SOCK_STG $TB whoami --short)" "stg"
eq "whoami: --short default" "$(W $TB whoami --short)" "default"

# TB_INSTANCE とラベル
out=$(W HERDR_SESSION=stg TB_INSTANCE=stg $TB whoami)
eq "whoami: instance and label prefix" "$(wf "$out" instance | tr -s ' ') | $(wf "$out" label-prefix | tr -s ' ')" "instance stg [env TB_INSTANCE] | label-prefix local.herdr-stg- [env TB_INSTANCE]"
eq "whoami: instance matching the session -> no note" "$(grep -c '^note:' <<<"$out")" "0"
out=$(W HERDR_SESSION=stg $TB whoami)
eq "whoami: non-default session without TB_INSTANCE keeps the legacy label and says so" "$(wf "$out" label-prefix | tr -s ' ') $(grep -c '^note: セッション stg ですが TB_INSTANCE が未設定' <<<"$out")" "label-prefix local.herdr- [既定] 1"
out=$(W TB_INSTANCE=stg $TB whoami)
eq "whoami: default session with TB_INSTANCE set is noted" "$(grep -c '^note: TB_INSTANCE=stg がセッション default と対応していません' <<<"$out")" "1"
out=$(W TB_INSTANCE='a b' $TB whoami); r=$?
eq "whoami: invalid TB_INSTANCE is reported, still exits 0" "$r $(grep -c '^label-prefix .*不正' <<<"$out") $(grep -c '^note: TB_INSTANCE は' <<<"$out")" "0 1 1"

# agent のペインか
eq "whoami: agent pane" "$(W HERDR_PANE_ID=w9:p2 $TB whoami | grep '^pane' | grep -c 'あり: w9:p2')" "1"
eq "whoami: empty HERDR_PANE_ID is not a pane" "$(W HERDR_PANE_ID= $TB whoami | grep '^pane' | grep -c 'なし')" "1"

# 何も開かず・書かず・呼ばない
: >"$FAKE_LOG"; mkdir "$TMP/ro"
eq "whoami: does not create the DB or its directory" "$(W TB_DB="$TMP/nodir/sub/t.db" $TB whoami >/dev/null 2>&1; echo $?; [[ -e $TMP/nodir ]] && echo created)" "0"
printf 'this is not a sqlite file' >"$TMP/broken.db"; sum=$(cksum <"$TMP/broken.db")
eq "whoami: works on a broken DB and leaves it untouched" "$(W TB_DB="$TMP/broken.db" $TB whoami >/dev/null 2>&1; echo $?) $([[ $(cksum <"$TMP/broken.db") == "$sum" ]] && echo same)" "0 same"
mkdir -p "$TMP/shim"; for t in sqlite3 jq; do printf '#!/bin/sh\necho called >>"%s/shim-called"\nexit 1\n' "$TMP" >"$TMP/shim/$t"; chmod +x "$TMP/shim/$t"; done
eq "whoami: never runs sqlite3 or jq" "$(W PATH="$TMP/shim:$PATH" TB_DB="$TMP/nodir/t.db" $TB whoami >/dev/null 2>&1; echo $?; [[ -e $TMP/shim-called ]] && echo called)" "0"
eq "whoami: never calls herdr" "$(wc -c <"$FAKE_LOG" | tr -d ' ')" "0"
eq "whoami: bad arguments -> 2" "$(rc $TB whoami --nope) $(rc $TB whoami --short extra)" "2 2"
