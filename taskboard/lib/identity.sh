# shellcheck shell=bash
# 土台(ADR 0004): 呼び出し元が誰か(人か、どの agent か)を herdr から引く。bin/tb から source される。単体では実行しない。
# 自己申告は使わない。どの機能からも呼んでよい。ここから機能のファイルの関数を呼ばない(使うのは transport の herdr_call と core だけ)。

LABEL_NAME=""; LABEL_PANE=""; LABEL_SESSION=""

# 報告者ラベルは herdr から引く(自己申告にしない)。引けなければ 1 を返す(理由は stderr)
resolve_label() {
  local pane=${HERDR_PANE_ID:-} out row
  [[ -n $pane ]] || { msg_err identity.no-pane 'HERDR_PANE_ID がありません。報告者を特定できないため保存しません'; return 1; }
  out=$(herdr_call agent list 2>/dev/null) || { msg_err identity.herdr-failed 'herdr agent list に失敗。報告者を特定できないため保存しません'; return 1; }
  row=$(jq -c --arg p "$pane" '[.result.agents[]? | select(.pane_id==$p)][0] // empty' <<<"$out" 2>/dev/null) || row=""
  [[ -n $row ]] || { echo "tb: pane $pane に該当する agent がありません。保存しません" >&2; return 1; }
  LABEL_NAME=$(jq -r '.name // ""' <<<"$row"); LABEL_SESSION=$(jq -r '.agent_session.value // ""' <<<"$row"); LABEL_PANE=$pane
  [[ -n $LABEL_NAME ]] || { echo "tb: pane $pane の agent に名前がありません(herdr agent rename)。保存しません" >&2; return 1; }
  [[ -n $LABEL_SESSION ]] || { echo "tb: pane $pane の agent に session がありません。保存しません" >&2; return 1; }
  [[ $LABEL_NAME =~ ^[A-Za-z0-9._-]{1,64}$ && $LABEL_PANE =~ ^[A-Za-z0-9._:-]{1,64}$ ]] \
    || { echo "tb: 報告者名/pane に使えない文字があります。保存しません" >&2; return 1; }
}

# 人か agent かの判定用: 一覧を取れなければ 1(「名前が無い」= 人の pane と区別する)
pane_owner_name() {  # $1=pane → 名前(無ければ空)
  local out
  out=$(herdr_call agent list 2>/dev/null) || return 1
  jq -r --arg p "$1" '[.result.agents[]? | select(.pane_id==$p)][0].name // empty' <<<"$out" 2>/dev/null || return 1
}

agent_name_by_pane() {  # $1=pane → 名前(無ければ空)
  herdr_call agent list 2>/dev/null | jq -r --arg p "$1" '[.result.agents[]? | select(.pane_id==$p)][0].name // empty' 2>/dev/null
}

# 呼び出し元: 人なら REQ_BY=human。agent の pane なら REQ_BY=<agent 名>
REQ_BY=""
requester() {
  local n=""
  if [[ -n ${HERDR_PANE_ID:-} ]]; then
    n=$(pane_owner_name "$HERDR_PANE_ID") || die "herdr agent list に失敗したため、実行者を特定できません"
  fi
  [[ $n != human ]] || die "agent 名 human は予約名です(人と区別できないため、この agent からの操作は受けません)"
  REQ_BY=${n:-human}
}
# require_human(人の端末からの実行か)は lib/perms.sh(ADR 0006 の権限段階の表で決まる)

# ---------- 呼び出し元の確かめ(ADR 0007 段 1a)----------
# HERDR_PANE_ID は自己申告なので、そのまま信じない。tb のプロセスが本当にその pane の中で動いているかを確かめる:
#   herdr の pane process-info が返す pane の最初のプロセス(shell_pid)が、tb の祖先にいるか、同じ端末(tty)を使っているか。
# HERDR_PANE_ID が無くても、herdr の中(祖先か同じ端末の持ち主に herdr のプロセスがいる)なら、agent の pane を探して HERDR_PANE_ID を付け直す
# (HERDR_PANE_ID を消して人のふりをする、を止める)。herdr の外(人の端末・scheduler・Web UI)は今までどおり。
# 確かめられないときは、呼び出し元が分からない(unknown)として何もしない(終了コード 3)。
# 限界: 同じ OS ユーザーの agent が、わざと親子関係と端末を切って(setsid と新しい端末)実行したり、tb を書き換えて実行したりするのは止められない(ADR 0007)。
AUTH_STATE=""   # pane(pane を確かめた)/ found(pane を探して付け直した)/ outside(herdr の外)
declare -A _AU_PPID=() _AU_TTY=() _AU_COMM=()
_auth_snapshot() {
  local pid ppid tty comm
  _AU_PPID=(); _AU_TTY=(); _AU_COMM=()
  while read -r pid ppid tty comm; do
    [[ $pid =~ ^[0-9]+$ ]] || continue
    _AU_PPID[$pid]=$ppid; _AU_TTY[$pid]=$tty; _AU_COMM[$pid]=${comm##*/}
  done < <(ps -A -o pid=,ppid=,tty=,comm= 2>/dev/null)
  ((${#_AU_PPID[@]})) && [[ -n ${_AU_PPID[$$]:-} ]]
}
_auth_tty_ok() { [[ -n $1 && $1 != '?' && $1 != '??' && $1 != - ]]; }
_auth_ancestors() {  # → _AU_ANC(自分から上の pid。空白区切り)
  local p=$$ n=0; _AU_ANC=" "
  while [[ -n $p && $p != 0 && $n -lt 64 ]]; do _AU_ANC+="$p "; p=${_AU_PPID[$p]:-}; n=$((n + 1)); done
}
_auth_inside_herdr() {  # herdr のプロセスが祖先にいるか、自分の端末の持ち主の親が herdr か
  local p mytty=${_AU_TTY[$$]:-}
  for p in $_AU_ANC; do [[ ${_AU_COMM[$p]:-} == herdr ]] && return 0; done
  _auth_tty_ok "$mytty" || return 1
  for p in "${!_AU_TTY[@]}"; do
    [[ ${_AU_TTY[$p]} == "$mytty" && ${_AU_COMM[${_AU_PPID[$p]:-0}]:-} == herdr ]] && return 0
  done
  return 1
}
_auth_in_pane() {  # $1=pane → 0=この pane の中 1=違う 2=herdr に聞けない
  local out sp mytty=${_AU_TTY[$$]:-}
  out=$(herdr_call pane process-info "$1" 2>/dev/null) || return 2
  sp=$(jq -r '.result.process_info.shell_pid // empty' <<<"$out" 2>/dev/null) || return 2
  [[ $sp =~ ^[0-9]+$ ]] || return 2
  [[ $_AU_ANC == *" $sp "* ]] && return 0
  _auth_tty_ok "$mytty" && [[ ${_AU_TTY[$sp]:-} == "$mytty" ]] && return 0
  return 1
}
AUTH_ERR=""
authenticate() {
  local claim=${HERDR_PANE_ID:-} r out p
  AUTH_STATE=""; AUTH_ERR=""
  _auth_snapshot || { AUTH_ERR="プロセスの一覧(ps)を読めないため、呼び出し元を確かめられません"; return 1; }
  _auth_ancestors
  if [[ -n $claim ]]; then
    r=0; _auth_in_pane "$claim" || r=$?
    case $r in
      0) AUTH_STATE=pane; return 0;;
      2) AUTH_ERR="herdr に pane $claim を問い合わせられないため、呼び出し元を確かめられません"; return 1;;
      *) AUTH_ERR="HERDR_PANE_ID=$claim の pane の中から実行されていません(呼び出し元を確かめられないため、何もしません)"; return 1;;
    esac
  fi
  if ! _auth_inside_herdr; then AUTH_STATE=outside; return 0; fi
  # herdr の中なのに HERDR_PANE_ID が無い: agent の pane を探す。どれにも当たらなければ人の pane
  out=$(herdr_call agent list 2>/dev/null) || { AUTH_ERR="herdr agent list に失敗したため、呼び出し元を確かめられません"; return 1; }
  while IFS= read -r p; do
    [[ -n $p ]] || continue
    r=0; _auth_in_pane "$p" || r=$?
    if ((r == 0)); then export HERDR_PANE_ID=$p; AUTH_STATE=found; return 0; fi
    ((r != 2)) || { AUTH_ERR="herdr に pane $p を問い合わせられないため、呼び出し元を確かめられません"; return 1; }
  done < <(jq -r '.result.agents[]?.pane_id // empty' <<<"$out" 2>/dev/null)
  AUTH_STATE=outside; return 0
}
auth_or_die() {
  authenticate && return 0
  echo "tb: [identity.unverified] $AUTH_ERR" >&2; exit 3
}
