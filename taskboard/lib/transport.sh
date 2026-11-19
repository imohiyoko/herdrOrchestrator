# shellcheck shell=bash
# herdr との境界。tb の中で herdr を実行するのはこのファイルだけ(herdr_call)。
# 送ったもの・送れなかったものは messages に残す。後で MCP などの経路を足すときはここを差し替える
# bin/tb から source される。単体では実行しない。

# herdr を実行する唯一の場所。引数はそのまま herdr CLI に渡す
herdr_call() { "$HERDR_BIN" "$@"; }

# 起動直後の準備待ちと送信の確認(lib/herdr-ready.sh。ほかのスクリプトと共有)
# shellcheck source=../../lib/herdr-ready.sh
source "$TB_ROOT/../lib/herdr-ready.sh"

# 宛先の状態を見て、idle/done のときだけ送る。0=送信済み 10=宛先が使用中 11=状態取得/送信の失敗
herdr_send() {  # $1=宛先 $2=本文 $3=ログ用 ID [$4=種別(report|comment|digest|send)]
  local target=$1 msg=$2 id=$3 kind=${4:-report} st out raw ecode
  raw=$(herdr_call agent get "$target" 2>/dev/null) || true
  st=$(jq -r '.result.agent.agent_status // empty' <<<"$raw" 2>/dev/null) || st=""
  ecode=$(jq -r '.error.code // empty' <<<"$raw" 2>/dev/null) || ecode=""
  case $st in
    idle|done) ;;
    "") log_message "$kind" "$id" "$target" "$msg" failed
        if [[ $ecode == agent_not_found ]]; then
          echo "tb: 宛先 '$target' が herdr セッション '$SESS' に見つかりません(セッション違いの可能性。HERDR_SESSION=<名前> で指定)。未送信: $id" >&2
        else
          echo "tb: 宛先 '$target' の状態を取得できません(herdr セッション '$SESS')。未送信(pending に残す): $id" >&2
        fi
        return 11;;
    *)  log_message "$kind" "$id" "$target" "$msg" busy
        echo "tb: 宛先 '$target' が $st のため送りません。未送信(pending に残す): $id" >&2; return 10;;
  esac
  if ! out=$(herdr_call agent prompt "$target" "$msg" 2>&1) || jq -e 'type=="object" and has("error")' <<<"$out" >/dev/null 2>&1; then
    log_message "$kind" "$id" "$target" "$msg" failed
    echo "tb: 送信に失敗しました。未送信(pending に残す): $id" >&2
    echo "$out" >&2
    return 11
  fi
  log_message "$kind" "$id" "$target" "$msg" sent
}

# 送信の記録はアプリ(tb)が書く。agent の自己申告に頼らない
log_message() {  # $1=kind $2=ref $3=target $4=body $5=sent|busy|failed
  sql "INSERT INTO main.messages (herdr_session, kind, ref, target, body, result, created_at)
    VALUES ($SQS, $(q "$1"), $(q "$2"), $(q "$3"), $(q "$4"), $(q "$5"), $(q "$(now)"));" >/dev/null || true
}

# scheduler(tick.sh)からのプロンプト送信。agent の pane からは使えない。記録は messages(kind=scheduler)
cmd_system-send() {
  [[ $# -ge 3 && $2 == --text ]] || usage "system-send <宛先> --text T --job J"
  local target=$1 text=$3 job=""; shift 3
  [[ ${1:-} == --job ]] && job=${2:-}
  [[ $job =~ ^[A-Za-z0-9][A-Za-z0-9_-]*$ ]] || usage "system-send: --job が必要です"
  require_human "tb system-send"
  herdr_send "$target" "$text" "job:$job" scheduler
}

# agent を 1 体起動する: 専用の workspace(label = 名前)を作り、その root pane で kind の agent(既定 claude)を起動する。
# 成功時 SPAWN_PANE / SPAWN_WS を埋めて 0。失敗時 SPAWN_ERR に理由を入れて 11(作った workspace は閉じる)
# 起動直後はシェル初期化(mise 等)が遅いと start が失敗するので数回やり直す(herdr-up.sh と同じ)
# SPAWN_ERR には、最後の agent start のエラーコードと短いメッセージ(SPAWN_ERR_MAX 文字まで)を入れる。全文と引数は入れない。
# 失敗した回ごとに 1 行(時刻・名前・エラーコード)を SPAWN_LOG に足す(書けなくても起動の結果は変えない)。
# agent が人の操作待ち(初回の確認画面など。エラーコードに not_ready / blocked を含む)のときは、やり直さず workspace も閉じない:
#   SPAWN_KEPT=1 にして 11 を返す(呼び出し側は pane を台帳に残し、人に attach を促す)
SPAWN_PANE=""; SPAWN_WS=""; SPAWN_ERR=""; SPAWN_KEPT=0
SPAWN_ERR_MAX=${TB_SPAWN_ERR_MAX:-200}
SPAWN_LOG=${TB_SPAWN_LOG:-$(dirname "$DB")/spawn-errors.log}
# herdr の出力からエラーコードと短いメッセージを取る。JSON の .error.code / .error.message、無ければ最初の行
spawn_error_of() {  # $1=出力 $2=終了コード。SPAWN_ECODE / SPAWN_EMSG を埋める
  local raw=$1
  SPAWN_ECODE=$(jq -r '.error.code // empty' <<<"$raw" 2>/dev/null | head -n1) || SPAWN_ECODE=""
  SPAWN_EMSG=$(jq -r '.error.message // empty' <<<"$raw" 2>/dev/null | head -n1) || SPAWN_EMSG=""
  [[ -n $SPAWN_ECODE || -n $SPAWN_EMSG ]] || SPAWN_EMSG=$(head -n1 <<<"$raw")
  [[ -n $SPAWN_ECODE ]] || SPAWN_ECODE="exit $2"
  SPAWN_ECODE=$(tr -c 'A-Za-z0-9_. -' '_' <<<"${SPAWN_ECODE:0:64}" | sed 's/_$//')
  SPAWN_EMSG=$(tr '\t\r' '  ' <<<"$SPAWN_EMSG"); SPAWN_EMSG=${SPAWN_EMSG:0:$SPAWN_ERR_MAX}
}
spawn_log_failure() {  # $1=名前 $2=エラーコード。本文・引数は書かない
  { mkdir -p "$(dirname "$SPAWN_LOG")" && printf '%s\t%s\t%s\n' "$(now)" "$1" "$2" >>"$SPAWN_LOG"; } 2>/dev/null || true
}
herdr_spawn() {  # $1=kind $2=名前 $3=cwd [$4...=agent に渡す引数(kind_launch_args の出力)]
  local kind=$1 name=$2 cwd=$3 out i rc; shift 3
  local -a extra=(); [[ $# -gt 0 ]] && extra=(-- "$@")
  SPAWN_PANE=""; SPAWN_WS=""; SPAWN_ERR=""; SPAWN_KEPT=0
  if ! out=$(herdr_call workspace create --cwd "$cwd" --label "$name" --no-focus 2>&1) \
     || ! SPAWN_PANE=$(jq -er '.result.root_pane.pane_id' <<<"$out" 2>/dev/null); then
    SPAWN_PANE=""; spawn_error_of "$out" 1; spawn_log_failure "$name" "workspace:$SPAWN_ECODE"
    SPAWN_ERR="workspace を作れない($SPAWN_ECODE${SPAWN_EMSG:+: $SPAWN_EMSG})"; return 11
  fi
  SPAWN_WS=$(jq -r '.result.workspace.workspace_id // empty' <<<"$out" 2>/dev/null) || SPAWN_WS=""
  for i in 1 2 3; do
    out=$(herdr_call agent start "$name" --kind "$(kind_herdr_kind "$kind")" --pane "$SPAWN_PANE" ${extra[@]+"${extra[@]}"} 2>&1); rc=$?
    # 終了コードが 0 でも JSON の error があれば失敗として扱う
    if ((rc == 0)) && ! jq -e 'type=="object" and has("error")' <<<"$out" >/dev/null 2>&1; then return 0; fi
    ((rc != 0)) || rc=1
    spawn_error_of "$out" "$rc"; spawn_log_failure "$name" "$SPAWN_ECODE"
    case $SPAWN_ECODE in
      *not_ready*|*blocked*)
        SPAWN_KEPT=1
        SPAWN_ERR="agent が人の操作を待っています($SPAWN_ECODE${SPAWN_EMSG:+: $SPAWN_EMSG})。herdr で pane $SPAWN_PANE を開き、確認画面などに答えてください(workspace は閉じていません)"
        return 11 ;;
    esac
    ((i < 3)) && sleep "${TB_SPAWN_RETRY_SEC:-3}"
  done
  SPAWN_ERR="agent を起動できない($SPAWN_ECODE${SPAWN_EMSG:+: $SPAWN_EMSG})"
  if [[ -n $SPAWN_WS ]]; then herdr_call workspace close "$SPAWN_WS" >/dev/null 2>&1 || true
  else herdr_call pane close "$SPAWN_PANE" >/dev/null 2>&1 || true; fi
  return 11
}

# agent を止める = その pane を閉じる(herdr に agent 単体の停止コマンドは無い)
herdr_close_pane() { herdr_call pane close "$1" >/dev/null 2>&1; }
