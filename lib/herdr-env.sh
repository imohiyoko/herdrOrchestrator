# shellcheck shell=bash
# 部品(taskboard / scheduler / setup)で共通の既定値。source して使う。何も export しない。
# 環境変数で上書きされていればそちらが優先(各部品が ${TB_DB:-$HERDR_DEFAULT_TB_DB} のように使う)。
# Python 側の同じ定義は lib/herdr_env.py。片方を変えたらもう片方も変える(tests/test_env.sh が突き合わせる)。
HERDR_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
HERDR_DEFAULT_BIN=herdr                                   # PATH から探す(scheduler は PATH に /opt/homebrew/bin を足す)
# tb / webui / setup が読み書きする DB。checkout をまたいでホームの 1 か所に置き、herdr のセッションは行の herdr_session 列で分ける
HERDR_STATE_DIR=${XDG_STATE_HOME:-$HOME/.local/state}/herdr
HERDR_DEFAULT_TB_DB=$HERDR_STATE_DIR/tasks.db
HERDR_LEGACY_TB_DB=$HERDR_ROOT/taskboard/data/tasks.db    # 旧: checkout ごとの DB(tb db-import で取り込む)
HERDR_SCHEMA_DIR=$HERDR_ROOT/taskboard/schema             # NNN_*.sql。PRAGMA user_version で順に流す

# herdr のセッション名。herdr の CLI と同じ優先順で、HERDR_SOCKET_PATH > HERDR_SESSION > herdr-session ファイル > default。
# socket の配置は <config>/herdr/sessions/<名前>/herdr.sock(default は <config>/herdr/herdr.sock)。
# パスから名前を導けない(配置が違う・使えない文字を含む)ときは HERDR_SESSION に落とす。herdr は呼ばない。
herdr_session_name() {
  local p=${HERDR_SOCKET_PATH:-} named='/sessions/([A-Za-z0-9_][A-Za-z0-9._-]*)/herdr\.sock$' dflt='/herdr/herdr\.sock$'
  if [[ $p =~ $named ]]; then printf '%s\n' "${BASH_REMATCH[1]}"; return 0; fi
  if [[ $p =~ $dflt ]]; then echo default; return 0; fi
  if [[ -n ${HERDR_SESSION:-} ]]; then echo "$HERDR_SESSION"; return 0; fi
  herdr_session_file_value || echo default
}

# herdr_session_name の答えがどこから決まったか: socket(HERDR_SOCKET_PATH から名前を導けた) / env(HERDR_SESSION) / default。
# 判定は herdr_session_name と同じ(片方を変えたらもう片方も変える。tests/test_instance.sh が突き合わせる)。
herdr_session_source() {
  local p=${HERDR_SOCKET_PATH:-} named='/sessions/([A-Za-z0-9_][A-Za-z0-9._-]*)/herdr\.sock$' dflt='/herdr/herdr\.sock$'
  if [[ $p =~ $named || $p =~ $dflt ]]; then echo socket; return 0; fi
  # herdr_session_apply がファイルの値を HERDR_SESSION に入れたものは、ファイルで決まったものとして扱う
  if [[ -n ${HERDR_SESSION:-} && ${HERDR_SESSION_FROM_FILE:-} == "$HERDR_SESSION" ]]; then echo file
  elif [[ -n ${HERDR_SESSION:-} ]]; then echo env; elif herdr_session_file_value >/dev/null; then echo file; else echo default; fi
}

# checkout ごとにセッション名を固定するファイル(既定は checkout 直下の herdr-session。git 管理外)。
# 1 行目の名前だけを読む(# 以降と前後の空白は無視)。名前に使えない文字があれば無いものとして扱う。
# 優先順は HERDR_SOCKET_PATH > HERDR_SESSION > このファイル > default(環境変数を付けたときだけ上書きされる)
herdr_session_file() { echo "${HERDR_SESSION_FILE:-$HERDR_ROOT/herdr-session}"; }
herdr_session_file_value() {
  local f v=""; f=$(herdr_session_file)
  [[ -f $f && -r $f ]] || return 1
  IFS= read -r v <"$f" || [[ -n $v ]] || return 1
  v=${v%%#*}; v=${v#"${v%%[![:space:]]*}"}; v=${v%"${v##*[![:space:]]}"}   # 前後の空白だけ落とす
  [[ $v =~ ^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$ ]] || return 1
  printf '%s\n' "$v"
}
# herdr の CLI は herdr-session ファイルを知らないので、ファイルで決まったときだけ HERDR_SESSION に入れて渡す。
# 各部品(tb / scheduler / herdr-up)が起動時に 1 回呼ぶ(このファイル自体は source されても何も export しない)
herdr_session_apply() {
  [[ $(herdr_session_source) == file ]] || return 0
  HERDR_SESSION=$(herdr_session_file_value); HERDR_SESSION_FROM_FILE=$HERDR_SESSION; export HERDR_SESSION HERDR_SESSION_FROM_FILE
}

# launchd ラベルの接頭辞。TB_INSTANCE 未設定(空)なら従来の "local.herdr-"、設定すれば "local.herdr-<instance>-"。
# instance は英数字で始まり、英数字と . _ - だけ(64 字以内)。違反は何も出さず 2 を返す。
herdr_label_prefix() {
  local inst=${TB_INSTANCE:-} re='^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$'
  if [[ -z $inst ]]; then echo "local.herdr-"; return 0; fi
  [[ $inst =~ $re ]] || { echo "error: TB_INSTANCE は英数字で始まり、英数字と . _ - の 64 字以内: $inst" >&2; return 2; }
  echo "local.herdr-$inst-"
}

# plist の環境変数に渡す値(HERDR_SESSION など)の検査。XML や launchd にそのまま入れてよい文字だけを通す
herdr_plist_value_ok() { [[ $1 =~ ^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$ ]]; }
