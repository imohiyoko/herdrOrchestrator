# shellcheck shell=bash
# agent の kind(claude / codex / gemini)ごとの違いを、1 か所に閉じ込める「kind アダプタ」
# bin/tb から source される。単体では実行しない。設計と対応表は taskboard/docs/agent-kinds.md
#
# 呼び出し側(agents.sh / compact.sh)は kind の名前で分岐せず、下の kind_* に聞く。
#   kind_valid K                  既知の kind か
#   kind_supports K 項目          項目に対応していれば 0(項目: launch model resume live-model compact slash)
#   kind_require K 項目 [補足]    対応していなければ KIND_ERR に理由を入れて 12(終了コード 12 = kind が対応していない項目がある)
#   kind_model_valid K M          モデル名の検査(0 / 1)
#   kind_launch_args K SID M      herdr agent start の -- 以降に渡す引数を 1 行 1 個で出す(SID / M は空でよい)
#   kind_herdr_kind K             herdr agent start --kind に渡す名前
#   kind_session_source K         session id の取り方(表示用)
# 対応していない項目は「黙って claude の引数を渡す」ことをせず、必ず kind_require で止める。

KIND_DEFAULT=claude
KIND_ALL="claude codex gemini"
KIND_ERR=""

kind_valid() { [[ " $KIND_ALL " == *" $1 "* ]]; }

# 項目ごとの対応。codex / gemini は起動(launch)だけ。--model と会話の再開は、公式のヘルプで
# herdr 経由の渡し方を確かめるまで「対応なし」とし、要求されたら kind_require が止める(docs/agent-kinds.md)
kind_supports() {  # $1=kind $2=項目
  case $1:$2 in
    claude:*) return 0;;
    codex:launch|gemini:launch) return 0;;
    *) return 1;;
  esac
}

kind_require() {  # $1=kind $2=項目 [$3=補足]。0 / 12
  KIND_ERR=""
  kind_valid "$1" || { KIND_ERR="kind=$1 は未知の kind です(使えるのは: $KIND_ALL) item=kind"; return 12; }
  kind_supports "$1" "$2" && return 0
  KIND_ERR="kind=$1 は $2 に対応していません item=$2${3:+ ($3)}。claude の引数を代わりに渡すことはしません(docs/agent-kinds.md)"
  return 12
}

kind_model_valid() {  # $1=kind $2=モデル名
  case $1 in
    claude) [[ $2 =~ $MODEL_RE ]];;
    *) return 1;;
  esac
}

kind_herdr_kind() { echo "$1"; }

# モデルの切り替え方(tb model-set)。kind -> 方法 の表。方法の無い kind は 1(呼び出し側は "not supported" で何も変えない)
#   claude: revive(止めて、同じ会話を --model 付きで復活。会話中の /model はユーザー単位の既定を書き換えるので使わない)
#   codex / gemini: まだ無い(引数での指定と会話の再開が確かめられていない。docs/model-switch.md)
kind_model_switch() {  # $1=kind
  case $1 in
    claude) echo revive;;
    *) return 1;;
  esac
}

kind_launch_into() {  # $1=配列名 $2=kind $3=session $4=モデル。kind_launch_args の出力を配列に入れる(bash 3.2 でも動くよう mapfile を使わない)
  local _l; eval "$1=()"
  while IFS= read -r _l; do eval "$1+=(\"\$_l\")"; done < <(kind_launch_args "$2" "$3" "$4")
  return 0
}

kind_launch_args() {  # $1=kind $2=session id(空なら新しい会話) $3=モデル(空なら無指定)。1 行 1 個
  case $1 in
    claude)
      [[ -z $2 ]] || printf '%s\n' --resume "$2"
      [[ -z $3 ]] || printf '%s\n' --model "$3";;
    *) :;;   # codex / gemini: 渡せる引数は無い(model / resume は呼び出し前に kind_require が止めている)
  esac
}

# session id は herdr agent list の agent_session.value(kind を問わず同じ取り方)。再開に使えるかは resume の項目で決まる
kind_session_source() { echo herdr; }

# kind の表: kind <TAB> launch <TAB> model <TAB> resume <TAB> live-model <TAB> session <TAB> compact
cmd_agent-kinds() {
  local k i row yn
  for k in $KIND_ALL; do
    row=$k
    for i in launch model resume live-model; do
      kind_supports "$k" "$i" && yn=yes || yn=no
      row+=$'\t'"$i=$yn"
    done
    row+=$'\t'"session=$(kind_session_source "$k")"
    kind_supports "$k" compact && yn=yes || yn=no
    echo "$row"$'\t'"compact=$yn"
  done
}
