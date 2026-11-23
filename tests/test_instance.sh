#!/usr/bin/env bash
# 複数 herdr セッションの切り分け: セッション名の導出、launchd ラベルの instance、plist への HERDR_SESSION
# plist は一時ディレクトリに書き出して中身を見るだけ(launchctl は呼ばない)
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$HERE/..
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
eq() { if [[ "$2" == "$3" ]]; then pass=$((pass+1)); echo "ok   $1"; else fail=$((fail+1)); echo "FAIL $1"; echo "   expected: $3"; echo "   actual:   $2"; fi; }
clean() { env -u HERDR_SESSION -u HERDR_SESSION_FROM_FILE -u HERDR_SOCKET_PATH -u TB_INSTANCE -u TB_UI_PORT "$@"; }
unset HERDR_SESSION HERDR_SOCKET_PATH TB_INSTANCE TB_UI_PORT HERDR_SESSION_FROM_FILE   # 呼び出し側の環境に左右されない
export HERDR_SESSION_FILE=$TMP/herdr-session   # 手元の checkout のファイルは見ない(下のテストで書く)
fn() { bash -c "source '$ROOT/lib/herdr-env.sh'; $1"; }

# --- herdr_session_name: HERDR_SOCKET_PATH > HERDR_SESSION > default
eq "session: nothing set -> default" "$(fn herdr_session_name)" "default"
eq "session: HERDR_SESSION only" "$(HERDR_SESSION=staging fn herdr_session_name)" "staging"
eq "session: socket of a named session" "$(HERDR_SOCKET_PATH=/tmp/h/.config/herdr/sessions/training/herdr.sock fn herdr_session_name)" "training"
eq "session: socket wins over HERDR_SESSION" "$(HERDR_SESSION=staging HERDR_SOCKET_PATH=/tmp/h/.config/herdr/sessions/training/herdr.sock fn herdr_session_name)" "training"
eq "session: default socket" "$(HERDR_SESSION=staging HERDR_SOCKET_PATH=/tmp/h/.config/herdr/herdr.sock fn herdr_session_name)" "default"
eq "session: unknown socket layout falls back to HERDR_SESSION" "$(HERDR_SESSION=staging HERDR_SOCKET_PATH=/tmp/x/other.sock fn herdr_session_name)" "staging"
eq "session: unknown socket layout, nothing else -> default" "$(HERDR_SOCKET_PATH=/tmp/x/other.sock fn herdr_session_name)" "default"
eq "session: unsafe name in socket path falls back" "$(HERDR_SESSION=staging HERDR_SOCKET_PATH='/h/sessions/a b/herdr.sock' fn herdr_session_name)" "staging"

# --- herdr_session_source: 名前の決まり方(socket / env / default)。判定は herdr_session_name と同じ
S=/tmp/h/.config/herdr/sessions/training/herdr.sock
eq "source: nothing set" "$(fn herdr_session_source)" "default"
eq "source: HERDR_SESSION only" "$(HERDR_SESSION=staging fn herdr_session_source)" "env"
eq "source: empty HERDR_SESSION" "$(HERDR_SESSION= fn herdr_session_source)" "default"
eq "source: named socket wins" "$(HERDR_SESSION=staging HERDR_SOCKET_PATH=$S fn herdr_session_source)" "socket"
eq "source: default socket wins" "$(HERDR_SESSION=staging HERDR_SOCKET_PATH=/tmp/h/.config/herdr/herdr.sock fn herdr_session_source)" "socket"
eq "source: unknown layout falls back to env" "$(HERDR_SESSION=staging HERDR_SOCKET_PATH=/tmp/x/other.sock fn herdr_session_source)" "env"
eq "source: unknown layout, nothing else" "$(HERDR_SOCKET_PATH=/tmp/x/other.sock fn herdr_session_source)" "default"
eq "source: unsafe name in the socket path falls back" "$(HERDR_SESSION=staging HERDR_SOCKET_PATH='/h/sessions/a b/herdr.sock' fn herdr_session_source)" "env"

# --- herdr-session ファイル: HERDR_SOCKET_PATH > HERDR_SESSION > ファイル > default
F=$HERDR_SESSION_FILE
printf 'staging  # この checkout は staging\n' >"$F"
eq "file: used when nothing else is set" "$(fn herdr_session_name) $(fn herdr_session_source)" "staging file"
eq "file: HERDR_SESSION wins" "$(HERDR_SESSION=training fn herdr_session_name) $(HERDR_SESSION=training fn herdr_session_source)" "training env"
eq "file: socket wins" "$(HERDR_SOCKET_PATH=$S fn herdr_session_name)" "training"
eq "file: apply exports it for herdr and keeps the source" "$(fn 'herdr_session_apply; echo "$HERDR_SESSION $(herdr_session_source)"')" "staging file"
eq "file: apply does nothing when HERDR_SESSION is set" "$(HERDR_SESSION=training fn 'herdr_session_apply; echo "$HERDR_SESSION ${HERDR_SESSION_FROM_FILE:-none}"')" "training none"
printf 'a b\n' >"$F"
eq "file: an unsafe name is ignored" "$(fn herdr_session_name) $(fn herdr_session_source)" "default default"
rm -f "$F"
eq "file: missing file -> default" "$(fn herdr_session_name)" "default"
eq "whoami: shows the file as the source" "$(printf 'staging\n' >"$F"; TB_DB=$TMP/w.db bash "$ROOT/taskboard/bin/tb" whoami | awk '$1=="session"{print $2, $3}')" "staging [ファイル"
rm -f "$F"

# --- herdr_label_prefix: TB_INSTANCE 未設定なら従来のまま
eq "prefix: unset -> legacy" "$(fn herdr_label_prefix)" "local.herdr-"
eq "prefix: empty -> legacy" "$(TB_INSTANCE= fn herdr_label_prefix)" "local.herdr-"
eq "prefix: instance" "$(TB_INSTANCE=staging fn herdr_label_prefix)" "local.herdr-staging-"
eq "prefix: allowed chars" "$(TB_INSTANCE=a_b-c.1 fn herdr_label_prefix)" "local.herdr-a_b-c.1-"
for bad in 'a b' 'a/b' 'a;b' '-x' '.x' 'a"b' 'a<b' "$(printf 'a\nb')"; do
  eq "prefix: rejected [$(printf %q "$bad")]" "$(TB_INSTANCE=$bad fn herdr_label_prefix >/dev/null 2>&1; echo $?)" "2"
done

# --- plist
# ジョブを conf ファイルで置いて試す(既定の scheduler は DB だけを読むので SCHED_FILE_FALLBACK=1)
JOBS=$TMP/jobs; mkdir -p "$JOBS"
printf "TARGET=dummy\nPROMPT='x'\nWINDOW=09:00-21:00\nINTERVAL_MIN=5\n" >"$JOBS/t.conf"
sched() { clean env SCHED_FILE_FALLBACK=1 JOBS_DIR="$JOBS" LOG_DIR="$TMP/logs" OUT_DIR="$TMP/gen-$1" "${@:2}" bash "$ROOT/scheduler/bin/gen-plist.sh" t >/dev/null 2>&1; }
web() { clean env OUT_DIR="$TMP/genw-$1" "${@:2}" bash "$ROOT/webui/bin/gen-plist.sh" >/dev/null 2>&1; }
label_of() { grep -A1 '<key>Label</key>' "$1" | sed -n 's/.*<string>\(.*\)<\/string>.*/\1/p'; }

sched a; eq "sched plist: legacy file name" "$(ls "$TMP/gen-a")" "local.herdr-sched-t.plist"
eq "sched plist: legacy label" "$(label_of "$TMP/gen-a/local.herdr-sched-t.plist")" "local.herdr-sched-t"
eq "sched plist: no HERDR_SESSION by default" "$(grep -c HERDR_SESSION "$TMP/gen-a"/*.plist)" "0"
sched b TB_INSTANCE=staging HERDR_SESSION=staging
eq "sched plist: instance file name" "$(ls "$TMP/gen-b")" "local.herdr-staging-sched-t.plist"
eq "sched plist: instance label" "$(label_of "$TMP/gen-b/local.herdr-staging-sched-t.plist")" "local.herdr-staging-sched-t"
eq "sched plist: HERDR_SESSION passed" "$(grep -A1 '<key>HERDR_SESSION</key>' "$TMP/gen-b"/*.plist | grep -c '<string>staging</string>')" "1"
eq "sched plist: PATH kept" "$(grep -c '<key>PATH</key>' "$TMP/gen-b"/*.plist)" "1"
sched c TB_INSTANCE='a b'; eq "sched plist: bad instance writes nothing" "$(ls "$TMP/gen-c" 2>/dev/null | wc -l | tr -d ' ')" "0"
sched d HERDR_SESSION='a<b'; eq "sched plist: bad HERDR_SESSION writes nothing" "$(ls "$TMP/gen-d" 2>/dev/null | wc -l | tr -d ' ')" "0"
clean env SCHED_FILE_FALLBACK=1 JOBS_DIR="$JOBS" LOG_DIR="$TMP/logs" OUT_DIR="$TMP/gen-e" TB_INSTANCE=staging HERDR_SESSION=staging bash "$ROOT/scheduler/bin/gen-plist.sh" --dispatch t >/dev/null 2>&1
eq "sched plist --dispatch: instance label" "$(label_of "$TMP/gen-e/local.herdr-staging-sched-dispatch.plist")" "local.herdr-staging-sched-dispatch"
eq "sched plist --dispatch: HERDR_SESSION passed" "$(grep -A1 '<key>HERDR_SESSION</key>' "$TMP/gen-e"/*.plist | grep -c '<string>staging</string>')" "1"

web a TB_INSTANCE=training HERDR_SESSION=training
eq "webui plist: 1 つだけ(ラベルは TB_INSTANCE によらず固定)" "$(ls "$TMP/genw-a" 2>/dev/null | tr '\n' ' ')" "local.herdr-webui.plist "
eq "webui plist: label" "$(label_of "$TMP/genw-a/local.herdr-webui.plist")" "local.herdr-webui"
eq "webui plist: セッションは渡さない(全セッションを返す)" "$(grep -c -e HERDR_SESSION -e TB_INSTANCE -e TB_UI_GATEWAY_PORT "$TMP/genw-a/local.herdr-webui.plist")" "0"
eq "webui plist: server.py を動かす" "$(grep -c '/server.py<' "$TMP/genw-a/local.herdr-webui.plist")" "1"
web b TB_UI_GATEWAY_PORT=08811
eq "webui plist: ポートの起点を渡す" "$(grep -A1 '<key>TB_UI_GATEWAY_PORT</key>' "$TMP/genw-b/local.herdr-webui.plist" | grep -c '<string>8811</string>')" "1"
web c TB_UI_GATEWAY_PORT=80x; eq "webui plist: 数字でないポートは書かない" "$(ls "$TMP/genw-c" 2>/dev/null | wc -l | tr -d ' ')" "0"
eq "旧版の入口(gateway.py)は無い" "$([[ -e $ROOT/webui/gateway.py || -e $ROOT/webui/bin/gen-gateway-plist.sh ]] && echo left || echo gone)" "gone"

# --- tb のセッション(DB の行の分け方)は socket パスを見る(bin/tb が herdrctl に渡す HERDR_SESSION は herdr_session_name)
eq "tb SESS follows the socket" "$(clean env HERDR_SESSION=staging HERDR_SOCKET_PATH=/h/.config/herdr/sessions/training/herdr.sock bash -c "source '$ROOT/lib/herdr-env.sh'; herdr_session_name")" "training"
eq "bin/tb passes herdr_session_name to herdrctl" "$(grep -c 'HERDR_SESSION=$(herdr_session_name)' "$ROOT/taskboard/bin/tb")" "1"

echo "pass=$pass fail=$fail"; [[ $fail -eq 0 ]]
