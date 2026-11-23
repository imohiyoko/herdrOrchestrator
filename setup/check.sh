#!/usr/bin/env bash
# セットアップの診断(読み取りのみ)。何が入っていて何が足りないかを 1 項目 1 行で出す。
# usage: setup/check.sh [--project DIR]...
#   --project DIR  tb-hook を確認するプロジェクト(worker の cwd)。省略時は presets/*.conf の作業ディレクトリ
#                  (example.conf と setup.conf を除く)
# 出力: "<状態> <項目> <説明>" と、必要なら次の行に "      next: <次にやること>"。最後に summary 行。
#   状態: ok / warn(動くが要確認) / missing(未設定) / skip(この環境では確認できない) / info
# 終了コード: warn も missing もなければ 0、あれば 1。
#
# このスクリプトはファイルを作らず、書き換えず、launchctl は print だけを使う。
# 環境変数(テスト用の差し替え): SETUP_ROOT(リポジトリ) HOME HERDR_BIN TB_HERDRCTL LAUNCHCTL_BIN LAUNCH_AGENTS_DIR
#   SETUP_UNAME(uname -s の結果)
set -u

ROOT="${SETUP_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
HERDR="${HERDR_BIN:-herdr}"
HERDRCTL="${TB_HERDRCTL:-herdrctl}"   # bin/tb と同じ(TB_HERDRCTL があればそれ)
LAUNCHCTL="${LAUNCHCTL_BIN:-launchctl}"
LA_DIR="${LAUNCH_AGENTS_DIR:-$HOME/Library/LaunchAgents}"
LABEL_PREFIX="local.herdr-${TB_INSTANCE:+$TB_INSTANCE-}"   # lib/herdr-env.sh の herdr_label_prefix と同じ規則(未設定なら従来のラベル)
OS="${SETUP_UNAME:-$(uname -s)}"

projects=()
while [ $# -gt 0 ]; do
  case "$1" in
    --project) [ $# -ge 2 ] || { echo "usage: check.sh [--project DIR]..." >&2; exit 2; }
               projects+=("$2"); shift 2 ;;
    -h|--help) sed -n '2,12p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

n_ok=0 n_warn=0 n_missing=0 n_skip=0
# report <状態> <項目> <説明> [次にやること]
report() {
  printf '%-7s %-28s %s\n' "$1" "$2" "$3"
  [ -n "${4:-}" ] && printf '        next: %s\n' "$4"
  case "$1" in
    ok) n_ok=$((n_ok + 1)) ;; warn) n_warn=$((n_warn + 1)) ;;
    missing) n_missing=$((n_missing + 1)) ;; skip) n_skip=$((n_skip + 1)) ;;
  esac
}
have() { command -v "$1" >/dev/null 2>&1; }
# ファイルの権限(8 進数)。macOS と Linux の stat の違いを吸収する
perm_of() { stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1" 2>/dev/null; }
is_mac() { [ "$OS" = Darwin ]; }

# --- 1. コマンド ---------------------------------------------------------------
for c in bash jq sqlite3 python3 claude; do
  if have "$c"; then report ok "cmd:$c" "$(command -v "$c")"
  else
    case $c in
      claude) report missing "cmd:$c" "Claude Code が PATH にない" "Claude Code をインストールする" ;;
      *) report missing "cmd:$c" "PATH にない" "brew install $c" ;;
    esac
  fi
done
if have "$HERDR"; then
  report ok "cmd:herdr" "$(command -v "$HERDR")"
  if "$HERDR" workspace list >/dev/null 2>&1; then report ok "herdr:server" "応答あり"
  else report warn "herdr:server" "server が応答しない(止まっている)" "herdr-up.sh <preset> を実行すると headless で起動する"; fi
else
  report missing "cmd:herdr" "PATH にない" "herdr をインストールする"
  report skip "herdr:server" "herdr がないので確認できない"
fi
# herdrctl(tb の中身。ADR 0002)。認可と呼び出し元の確かめを持つ版でないと tb は動かない
HC_NEXT="このリポジトリで go build -o <PATH の通ったディレクトリ>/herdrctl ./cmd/herdrctl(Go が要る)、または GitHub の release から herdrctl を入れる"
if have "$HERDRCTL"; then
  hcf=$("$HERDRCTL" tb --features 2>/dev/null) || hcf=""
  if [[ " $hcf " == *" policy-ops "* && " $hcf " == *" auth "* ]]; then   # bin/tb と同じ条件
    report ok "cmd:herdrctl" "$(command -v "$HERDRCTL")"
  else
    report missing "cmd:herdrctl" "古い版(認可と呼び出し元の確かめが無いので、tb が動かない): $(command -v "$HERDRCTL")" "入れ直す: $HC_NEXT"
  fi
else
  report missing "cmd:herdrctl" "PATH にない(tb の中身なので、無いと tb が動かない)" "$HC_NEXT"
fi
if is_mac; then
  if have plutil; then report ok "cmd:plutil" "$(command -v plutil)"
  else report warn "cmd:plutil" "PATH にない(plist の lint に使う)"; fi
fi

# --- 2. tb-hook(Claude Code の settings) ----------------------------------------
HOOK="$ROOT/taskboard/bin/tb-hook"

# settings ファイル 1 つについて、event に tb-hook を呼ぶ command があれば 1 行ずつ出す
hook_cmds() { jq -r --arg e "$2" '.hooks[$e][]?.hooks[]? | select(.type == "command") | .command // empty | select(test("tb-hook"))' "$1" 2>/dev/null; }

# scan_settings <ファイル>: 登録を調べ、"found" または "<状態>|<説明>|<次>" を 1 行ずつ出す(report はしない)
scan_settings() {
  local f=$1 e cmds path n=0
  [ -f "$f" ] || return 0
  if ! jq -e . "$f" >/dev/null 2>&1; then echo "warn|JSON として読めない: $f|書式を直す"; return 0; fi
  for e in UserPromptSubmit Stop; do
    cmds=$(hook_cmds "$f" "$e")
    if [ -z "$cmds" ]; then continue; fi
    n=$((n + 1))
    if [ "$(printf '%s\n' "$cmds" | wc -l | tr -d ' ')" -gt 1 ]; then
      echo "warn|$e に tb-hook が 2 つ以上: $f|1 つにする(turn ごとに 2 回記録される)"
    fi
    path=$(printf '%s\n' "$cmds" | head -1 | sed -E 's/^(bash|sh)[[:space:]]+//; s/[[:space:]].*$//')
    if [ "$path" = "$HOOK" ]; then :
    elif [ -f "$path" ]; then echo "warn|$e の tb-hook が別の場所を指す: $path|このリポジトリの $HOOK に揃える"
    else echo "missing|$e の tb-hook が存在しないパス: $path|パスを $HOOK に直す"; fi
  done
  [ $n = 0 ] && return 0
  echo found
  [ $n = 1 ] && echo "warn|UserPromptSubmit と Stop の片方だけに登録: $f|両方に入れる"
  return 0
}

# report_scan <項目> <scan_settings の出力>: 問題を report し、登録があれば 0 を返す
report_scan() {
  local name=$1 line st msg nx rc=1
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    if [ "$line" = found ]; then rc=0; continue; fi
    st=${line%%|*}; line=${line#*|}; msg=${line%%|*}; nx=${line#*|}
    report "$st" "$name" "$msg" "$nx"
  done <<<"$2"
  return $rc
}

if ! have jq; then
  report skip "hook" "jq がないので settings を読めない"
else
  # グローバル(全セッションが対象になる。docs/hook-install.md のリスク参照)
  global=0
  if report_scan "hook:global" "$(scan_settings "$HOME/.claude/settings.json")"; then
    global=1
    report warn "hook:global" "$HOME/.claude/settings.json に登録がある(全プロジェクトが対象)" \
      "意図したものか確認する。project 単位を推奨(taskboard/docs/hook-install.md)"
  fi

  if [ ${#projects[@]} -eq 0 ]; then
    for p in "$ROOT"/presets/*.conf; do
      [ -f "$p" ] || continue
      case "$(basename "$p")" in example.conf|setup.conf) continue ;; esac
      while IFS='|' read -r label _ _ dir _; do
        [[ -z ${label// } || $label == \#* || -z ${dir// } ]] && continue
        projects+=("${dir/#\~/$HOME}")
      done <"$p"
    done
  fi
  if [ ${#projects[@]} -eq 0 ] && [ $global = 0 ]; then
    report missing "hook" "確認するプロジェクトがない(presets/*.conf に作業ディレクトリがない)" \
      "worker 用のプリセットを作るか、--project <dir> を付けて再実行する"
  fi
  seen=" "
  for d in ${projects[@]+"${projects[@]}"}; do
    case "$seen" in *" $d "*) continue ;; esac
    seen="$seen$d "
    name="hook:$(basename "$d")"
    if [ ! -d "$d" ]; then report missing "$name" "ディレクトリがない: $d" "プリセットの作業ディレクトリを直す"; continue; fi
    if report_scan "$name" "$(scan_settings "$d/.claude/settings.json"; scan_settings "$d/.claude/settings.local.json")"; then
      if [ $global = 1 ]; then report warn "$name" "グローバルとプロジェクトの両方に登録(二重記録)" "どちらか一方にする"
      else report ok "$name" "登録あり: $d/.claude/"; fi
    elif [ $global = 1 ]; then report ok "$name" "グローバルの登録で記録される"
    else report missing "$name" "tb-hook が未登録: $d" "$d/.claude/settings.local.json に hook を足す(taskboard/docs/hook-install.md)"; fi
  done
fi

# --- 3. taskboard ----------------------------------------------------------------
# DB は checkout をまたいでホームの 1 か所(lib/herdr-env.sh の HERDR_DEFAULT_TB_DB と同じ)。herdr のセッションは行で分ける
db="${TB_DB:-${XDG_STATE_HOME:-$HOME/.local/state}/herdr/tasks.db}"
legacy="$ROOT/taskboard/data/tasks.db"   # 旧: checkout ごとの DB
if [ -f "$db" ]; then
  # スキーマの版(taskboard/schema/NNN_*.sql の最大番号)と DB の PRAGMA user_version を比べる。読むだけ
  latest=0
  for f in "$ROOT"/taskboard/schema/[0-9][0-9][0-9]_*.sql; do [ -f "$f" ] && latest=$((10#$(basename "$f" | cut -c1-3))); done
  ver=""; have sqlite3 && ver=$(sqlite3 -readonly "$db" "PRAGMA user_version;" 2>/dev/null)
  if [[ $ver =~ ^[0-9]+$ ]] && [ "$latest" -gt 0 ] && [ "$ver" -lt "$latest" ]; then
    report info "taskboard:db" "$db(スキーマ v${ver}、最新は v${latest}。次に tb を実行したとき移行される)"
  else report ok "taskboard:db" "$db${ver:+(スキーマ v$ver)}"; fi
else report info "taskboard:db" "まだない(最初の tb add で作られる): $db"; fi
# 旧形式の DB が残っていて、まだ取り込んでいなければ知らせる(取り込んだ記録は共有の DB の tb_flags の db-import:<実体のパス>)
if [ -f "$legacy" ] && [ "$legacy" != "$db" ]; then
  lreal="$(cd "$(dirname "$legacy")" && pwd -P)/$(basename "$legacy")"
  done_imp=""
  if [ -f "$db" ] && have sqlite3; then
    sq="'"; done_imp=$(sqlite3 -readonly "$db" "SELECT 1 FROM tb_flags WHERE key='db-import:${lreal//$sq/$sq$sq}' LIMIT 1;" 2>/dev/null)
  fi
  if [ -z "$done_imp" ]; then
    report warn "taskboard:legacy-db" "旧形式の DB(checkout ごと)が残っている: $legacy" \
      "共有の DB へ取り込む: HERDR_SESSION=<この checkout のセッション> taskboard/bin/tb db-import $legacy(--dry-run で先に確かめられる)"
  else report ok "taskboard:legacy-db" "旧形式の DB は取り込み済み(不要なら消してよい): $legacy"; fi
fi
# 設定は DB の config 表に置く(schema/013)。checkout の config/*.conf があって、DB に共通の行(*)が無いキーを知らせる
cfg_left=""
for k in projects approvals grants routes limits notify compact sched-watch spawn-policy; do
  [ -f "$ROOT/taskboard/config/$k.conf" ] || continue
  hit=""
  if [ -f "$db" ] && have sqlite3; then hit=$(sqlite3 -readonly "$db" "SELECT 1 FROM config WHERE herdr_session='*' AND key='$k' LIMIT 1;" 2>/dev/null); fi
  [ -n "$hit" ] || cfg_left="$cfg_left $k"
done
if [ -n "$cfg_left" ]; then
  report warn "taskboard:config" "DB に取り込んでいない設定ファイルがある(今はファイルを読んでいる):$cfg_left" \
    "DB へ取り込む: taskboard/bin/tb config import(--dry-run で先に確かめられる。以後は tb config で編集)"
else report ok "taskboard:config" "設定は DB から引く(確かめる: tb config)"; fi

# --- 4. launchd の共通処理 ------------------------------------------------------
# launchd_check <項目> <ラベル> <生成元 plist>
launchd_check() {
  local name=$1 label=$2 gen=$3 dst="$LA_DIR/$2.plist"
  if ! is_mac; then report skip "$name" "macOS ではないので launchd は確認しない"; return; fi
  if [ ! -f "$dst" ]; then
    if [ -f "$gen" ]; then report missing "$name" "plist は生成済みだが未登録: $label" "登録する(人の承認が要る。setup/CLAUDE.md の手順)"
    else report missing "$name" "plist が未生成・未登録: $label" "plist を生成して登録する(setup/CLAUDE.md の手順)"; fi
    return
  fi
  if ! have "$LAUNCHCTL"; then report skip "$name" "launchctl がない"; return; fi
  if "$LAUNCHCTL" print "gui/$(id -u)/$label" >/dev/null 2>&1; then
    if grep -q '<key>Hour</key>' "$dst" 2>/dev/null; then
      report warn "$name" "古い形式の plist(時刻を焼き込んでいる): $dst" "gen-plist.sh で作り直して再登録する"
    else report ok "$name" "登録済み・読み込み済み: $label"; fi
  else
    report warn "$name" "$dst はあるが launchd に読み込まれていない" "launchctl bootstrap し直す(人の承認が要る)"
  fi
}

# --- 5. scheduler のジョブ ------------------------------------------------------
JOBS="$ROOT/scheduler/jobs"
# まとめて動かす 1 本(gen-plist.sh --dispatch。scheduler/bin/dispatch.sh)。登録済みなら、その引数のジョブはそこで動く
DISPATCH_LABEL="${LABEL_PREFIX}sched-dispatch"
DISPATCH_JOBS=" "
if [ -f "$LA_DIR/$DISPATCH_LABEL.plist" ]; then
  _after=0
  while IFS= read -r _s; do   # dispatch.sh の後ろの <string> がジョブ名
    if [ $_after = 1 ]; then [[ "$_s" =~ ^[A-Za-z0-9][A-Za-z0-9_-]*$ ]] && DISPATCH_JOBS="$DISPATCH_JOBS$_s "
    else case "$_s" in */dispatch.sh) _after=1 ;; esac; fi
  done < <(sed -n 's:.*<string>\(.*\)</string>.*:\1:p' "$LA_DIR/$DISPATCH_LABEL.plist")
  launchd_check "launchd:dispatch" "$DISPATCH_LABEL" "$ROOT/scheduler/generated/$DISPATCH_LABEL.plist"
fi
job_launchd_check() {  # $1=job
  local label="${LABEL_PREFIX}sched-$1"
  case "$DISPATCH_JOBS" in
    *" $1 "*)
      if [ -f "$LA_DIR/$label.plist" ]; then
        report warn "launchd:$1" "$1 が dispatch と個別の plist の両方に登録されている(二重に動く)" \
          "launchctl bootout gui/\$(id -u)/$label && rm $LA_DIR/$label.plist(人の承認が要る)"
      else report ok "launchd:$1" "$DISPATCH_LABEL でまとめて実行"; fi ;;
    *) launchd_check "launchd:$1" "$label" "$ROOT/scheduler/generated/$label.plist" ;;
  esac
}
# ジョブの定義は DB の config 表(sched-job:<job>。このセッションの行か共通の行 *)だけから読む(scheduler/bin/lib.sh)。
# jobs/<job>.conf は取り込む前の置き場所で、DB に無ければ動かない(tb job import で取り込む)
SJ_SESS=${HERDR_SESSION:-}
if [ -z "$SJ_SESS" ] && [ -f "$ROOT/herdr-session" ]; then SJ_SESS=$(sed -n '1{s/#.*//;s/[[:space:]]//g;p;}' "$ROOT/herdr-session"); fi
[[ "$SJ_SESS" =~ ^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$ ]] || SJ_SESS=default
job_db_body() {  # $1=job。DB の定義の本文(無ければ空)
  [ -f "$db" ] && have sqlite3 || return 0
  sqlite3 -readonly "$db" "SELECT body FROM config WHERE key='sched-job:$1' AND herdr_session IN ('$SJ_SESS', '*') ORDER BY herdr_session='*' LIMIT 1;" 2>/dev/null
}
db_jobs=""
if [ -f "$db" ] && have sqlite3; then
  db_jobs=$(sqlite3 -readonly "$db" "SELECT DISTINCT substr(key, 11) FROM config WHERE key LIKE 'sched-job:%' AND herdr_session IN ('$SJ_SESS', '*');" 2>/dev/null \
    | grep -E '^[A-Za-z0-9][A-Za-z0-9_-]*$')
fi
# scheduler(herdrctl sched)が読めるか。読めなければ理由を出す(herdrctl が無い・古いときは cmd:herdrctl が知らせる)
# herdrctl は scheduler(scheduler/bin/lib.sh)と同じく SCHED_HERDRCTL > TB_HERDRCTL > PATH
SCHED_HC="${SCHED_HERDRCTL:-$HERDRCTL}"
SCHED_LINT=0
if have "$SCHED_HC" && [[ " $("$SCHED_HC" sched --features 2>/dev/null) " == *" lint "* ]]; then SCHED_LINT=1; fi
job_go_err() {  # $1=job $2=本文
  [ "$SCHED_LINT" = 1 ] || return 0
  local out
  out=$(printf '%s\n' "$2" | SCHED_ROOT="$ROOT/scheduler" TB_DB="$db" HERDR_SESSION="$SJ_SESS" "$SCHED_HC" sched lint "$1" 2>&1 >/dev/null) || printf '%s' "${out#error: }"
}
# job_status <job> <必須なら 1>: 定義の有無と /path/to/ の残りを report する。DB に定義があれば 0
job_status() {
  local job=$1 required=$2 conf="$JOBS/$1.conf" body
  body=$(job_db_body "$job")
  if [ -n "$body" ]; then
    if grep -q '/path/to/' <<<"$body"; then
      report missing "job:$job" "DB の定義に /path/to/ が残っている" "実際のパスに直す: taskboard/bin/tb job edit $job"
    else
      local gerr; gerr=$(job_go_err "$job" "$body")
      if [[ "$gerr" == *": settings "* ]]; then   # 上書き設定(sched-settings:<job>)の誤り。本文を直しても消えない
        report warn "job:$job" "上書き設定を scheduler が読めない(このままでは動かない): $gerr" \
          "上書き設定を直す: taskboard/bin/tb job edit $job --settings(Web UI のスケジュール画面でも直せる)"
      elif [ -n "$gerr" ]; then
        report warn "job:$job" "DB の定義を scheduler が読めない(このままでは動かない): $gerr" \
          "書き直す: taskboard/bin/tb job edit $job(KEY=値 だけ。\$ や \` の展開は使わない。scheduler/README.md の「Go 版」)"
      else report ok "job:$job" "DB に定義あり(sched-job:$job)"; fi
    fi
    return 0
  fi
  if [ -f "$conf" ]; then
    report warn "job:$job" "$job.conf が DB に取り込まれていない(定期実行は DB の定義だけで動くので、このジョブは動かない)" \
      "取り込む: taskboard/bin/tb job import $job(--dry-run で先に確かめられる)$(grep -q '/path/to/' "$conf" && echo "。先に $conf の /path/to/ を実際のパスに直す")"
    return 1
  fi
  if [ "$required" = 1 ]; then report missing "job:$job" "定義がない(DB に sched-job:$job が無い)" \
    "$job.conf.example を元にパスを直した本文を DB に置く: taskboard/bin/tb job set $job < <本文のファイル>"
  else report info "job:$job" "未使用(任意。使うなら $job.conf.example を元に tb job set $job で DB に置く)"; fi
  return 1
}
for ex in "$JOBS"/*.conf.example; do
  [ -f "$ex" ] || continue
  job=$(basename "$ex" .conf.example)
  # taskboard-digest は worker の報告を指揮官に届けるのに要る。ほかは任意
  required=0; [ "$job" = taskboard-digest ] && required=1
  job_status "$job" "$required" || [ -f "$JOBS/$job.conf" ] || continue
  job_launchd_check "$job"
done
# .example のない独自ジョブ(DB とファイルの和)
seen=" "
for job in $db_jobs $(for conf in "$JOBS"/*.conf; do [ -f "$conf" ] && basename "$conf" .conf; done); do
  [[ "$job" =~ ^[A-Za-z0-9][A-Za-z0-9_-]*$ ]] || continue
  case "$seen" in *" $job "*) continue ;; esac; seen="$seen$job "
  [ -f "$JOBS/$job.conf.example" ] && continue
  if [ -n "$(job_db_body "$job")" ]; then report info "job:$job" "独自のジョブ(DB に定義あり)"; else job_status "$job" 0 || true; fi
  job_launchd_check "$job"
done

# --- 6. Web UI --------------------------------------------------------------------
# ログイン情報と待ち受けたポートは checkout をまたいでホームの 1 か所(internal/webui の StateDir と同じ)
WSTATE="${XDG_STATE_HOME:-$HOME/.local/state}/herdr-webui"
if [ -f "$WSTATE/auth.json" ]; then
  mode=$(perm_of "$WSTATE/auth.json")
  if [ "$mode" = 600 ]; then report ok "webui:auth" "ログイン設定済み(全セッション共通。内容は表示しない)"
  else report warn "webui:auth" "$WSTATE/auth.json の権限が $mode(600 であるべき)" "chmod 600 $WSTATE/auth.json"; fi
else
  report missing "webui:auth" "ログイン未設定($WSTATE/auth.json がない)" "Web UI を起動し、本人がブラウザで http://127.0.0.1:8800/ を開き(初回設定の /s/<セッション名>/setup へ移る)、ユーザー名とパスワードを作る"
fi
RUN="$ROOT/webui/run"
[ -e "$RUN/auth.json" ] && report warn "webui:old-auth" "旧い置き場のログイン情報 webui/run/auth.json が残っている(もう使わない)" "rm webui/run/auth.json webui/run/sessions.json(人の承認が要る)"
[ -e "$RUN/token" ] && report warn "webui:token" "廃止された run/token が残っている" "rm webui/run/token(もう使わない)"
if [ -f "$WSTATE/gateway.json" ]; then
  gp=$(tr -cd '0-9:,"a-z' <"$WSTATE/gateway.json" | sed -n 's/.*"port":\([0-9]*\).*/\1/p')
  [ -n "$gp" ] && report info "webui:port" "Web UI が動いている可能性: http://127.0.0.1:$gp/"
fi
# Web UI は 1 プロセスで全セッションを返す(ラベルは TB_INSTANCE によらず固定)。旧版の入口・セッションごとの plist は残すと二重起動を繰り返す
launchd_check "launchd:webui" "local.herdr-webui" "$ROOT/webui/generated/local.herdr-webui.plist"
if is_mac; then
  for f in "$LA_DIR"/local.herdr-webui-gateway.plist "$LA_DIR"/local.herdr-*-webui.plist; do
    [ -f "$f" ] || continue
    l=$(basename "$f" .plist)
    report warn "webui:old-plist" "旧版の plist が残っている: $l(Web UI は 1 プロセスになった)" \
      "launchctl bootout gui/\$(id -u)/$l && rm $f(人の承認が要る)"
  done
  # Python 版(webui/server.py)を動かす plist は、server.py を消したので起動できない
  if grep -q '/server\.py<' "$LA_DIR/local.herdr-webui.plist" 2>/dev/null; then
    report warn "webui:old-plist" "登録済みの plist が消した Python 版(webui/server.py)を動かす: local.herdr-webui" \
      "bash webui/bin/gen-plist.sh で作り直し、launchctl bootout gui/\$(id -u)/local.herdr-webui のあと cp して bootstrap し直す(人の承認が要る)"
  fi
fi

# --- 7. プリセット ---------------------------------------------------------------
n_presets=0
for p in "$ROOT"/presets/*.conf; do
  [ -f "$p" ] || continue
  case "$(basename "$p")" in example.conf|setup.conf) continue ;; esac
  n_presets=$((n_presets + 1))
done
if [ $n_presets -gt 0 ]; then report ok "presets" "$n_presets 個(example/setup 以外)"
else report missing "presets" "自分用のプリセットがない" "presets/example.conf を元に presets/<名前>.conf を作る"; fi

echo "summary: ok=$n_ok warn=$n_warn missing=$n_missing skip=$n_skip"
[ $n_warn -eq 0 ] && [ $n_missing -eq 0 ]
