# shellcheck shell=bash
# 共通処理。bin/*.sh から source される。中身は Go 版(herdrctl sched <サブコマンド>)で、ここは環境をそろえて渡すだけ。
# herdrctl: SCHED_HERDRCTL > TB_HERDRCTL > PATH。下で PATH を置き換える前に探す(dispatch.sh のサブシェルでは引き継ぐ)
[ -n "${_SCHED_HC+x}" ] || { _SCHED_HC=${SCHED_HERDRCTL:-${TB_HERDRCTL:-}}; [ -n "$_SCHED_HC" ] || _SCHED_HC=$(command -v herdrctl 2>/dev/null) || _SCHED_HC=""; }
# tb の中身(herdrctl)も同じく PATH を置き換える前に探して tb に渡す(TB_HERDRCTL > PATH。tick が呼ぶ tb が見つけられるように)
# コマンド名だけの TB_HERDRCTL も、置き換える前の PATH で実行ファイルの場所にする。未設定なら scheduler の herdrctl(_SCHED_HC)> PATH
_tbhc=${TB_HERDRCTL:-${_SCHED_HC:-herdrctl}}
case $_tbhc in */*) ;; *) _tbhc=$(command -v "$_tbhc" 2>/dev/null) || _tbhc="";; esac
[ -z "$_tbhc" ] || export TB_HERDRCTL=$_tbhc
unset _tbhc
# launchd から動くときは PATH が決まった値なので、go install(~/go/bin)や ~/.local/bin に入れた herdrctl も最後に探す
export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin${HOME:+:$HOME/.local/bin:$HOME/go/bin}"

SCHED_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
JOBS_DIR="${JOBS_DIR:-$SCHED_ROOT/jobs}"
LOG_DIR="${LOG_DIR:-$SCHED_ROOT/logs}"
# Web UI などが書く上書き設定(<job>.settings)の置き場
SETTINGS_DIR="${SCHED_SETTINGS_DIR:-$SCHED_ROOT/settings}"
# shellcheck source=../../lib/herdr-env.sh
source "$SCHED_ROOT/../lib/herdr-env.sh"
herdr_session_apply   # checkout の herdr-session ファイルでセッションを固定していれば、herdr と tb にも渡す
HERDR="${HERDR_BIN:-$HERDR_DEFAULT_BIN}"   # 上の PATH で /opt/homebrew/bin/herdr が先に見つかる
export HERDR_BIN_PATH="${HERDR_BIN_PATH:-$HERDR}"
# ジョブ定義・上書き設定の DB(taskboard と同じ DB の config 表)
SCHED_DB="${TB_DB:-$HERDR_DEFAULT_TB_DB}"

die() { echo "error: $*" >&2; exit 2; }

# herdrctl sched <サブコマンド> に置き換わって戻らない。herdrctl が無い・古い(sched --features にそのサブコマンドが無い)ならエラー(exit 2)
# Go 版は conf を bash として実行せず、決まった書き方(KEY=値)だけを読む(README の「Go 版」)
sched_go() {
  local f
  [ -n "$_SCHED_HC" ] && command -v "$_SCHED_HC" >/dev/null 2>&1 \
    || die "herdrctl が見つかりません(scheduler は herdrctl sched で動く。go install ./cmd/herdrctl するか SCHED_HERDRCTL で場所を渡す)"
  f=$("$_SCHED_HC" sched --features 2>/dev/null) && [[ " $f " == *" $1 "* ]] \
    || die "herdrctl が古く、sched $1 がありません(go install ./cmd/herdrctl で入れ直す)"
  SCHED_ROOT=$SCHED_ROOT JOBS_DIR=$JOBS_DIR SCHED_SETTINGS_DIR=$SETTINGS_DIR TB_DB=$SCHED_DB LOG_DIR=$LOG_DIR HERDR_BIN=$HERDR \
    TB_BIN="${TB_BIN:-$SCHED_ROOT/../taskboard/bin/tb}" exec "$_SCHED_HC" sched "$@"
}
