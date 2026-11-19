# shellcheck shell=bash
# 土台(ADR 0004): 呼び出し元が誰か(人か、どの agent か)を herdr から引く。bin/tb から source される。単体では実行しない。
# 自己申告は使わない。どの機能からも呼んでよい。ここから機能のファイルの関数を呼ばない(使うのは transport の herdr_call と core だけ)。

LABEL_NAME=""; LABEL_PANE=""; LABEL_SESSION=""

# 報告者ラベルは herdr から引く(自己申告にしない)。引けなければ 1 を返す(理由は stderr)
resolve_label() {
  local pane=${HERDR_PANE_ID:-} out row
  [[ -n $pane ]] || { echo "tb: HERDR_PANE_ID がありません。報告者を特定できないため保存しません" >&2; return 1; }
  out=$(herdr_call agent list 2>/dev/null) || { echo "tb: herdr agent list に失敗。報告者を特定できないため保存しません" >&2; return 1; }
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

# 人の端末からの実行か(agent の pane からは承認・設定変更をさせない)
require_human() {
  local pane=${HERDR_PANE_ID:-} n
  [[ -n $pane ]] || return 0
  n=$(pane_owner_name "$pane") || die "$1: herdr agent list に失敗したため、人の端末からの実行か確かめられません"
  [[ -z $n ]] || die "$1 は人が自分の端末で実行します(agent '$n' の pane からは実行できません)"
}
