# shellcheck shell=bash
# 会話チームの学習(#97): 下書きと人が実際に送った文の組を記録し、人の手直しを「候補のパターン」にする。
# bin/tb から source される。単体では実行しない。設計は taskboard/docs/conversation-learning.md
#
# 置き場は DB でも checkout でもなく、この機械だけの $TB_CONV_STATE_DIR(既定 $HERDR_STATE_DIR/conv、権限 700):
#   pairs/<kind>/<pair-id>/{draft.txt,final.txt,meta.json}   組(人が「送った」と記録したものだけ)
#   ledger/<kind>.tsv            候補の台帳(種別ごとに 1 表。状態・許す方向か・パターンの各列)
#   ledger/<kind>.evidence.tsv   候補の証拠(証拠の種類・組か出どころ・状況のキー)。件数はここから数える
# 下書きを作る側(lib/conv.sh)はこの置き場を読まない。読むのは conversation/kinds/<kind>/patterns.conf の adopted だけ。
# なので、この置き場を丸ごと消しても下書きは今まで採用したパターンで続く(tb conv-learn-purge)。
#
# 決まり:
# - 学ぶのは人が最後に送った文だけ。agent の手直しの下書きや声の役割の指摘は証拠にならない(agent-generated は記録するが数えない)
# - 1 回の手直しは候補止まり。同じ向きの変更が「別々の状況」で threshold 回(人が決める。既定 3、最小 2)以上そろって proposed
# - 採用(adopted)は人だけ。許す方向の変更(--permissive)は採用のときに --approve-permissive が要る
# - 台帳とパターンに残す文は、名前・番号・URL・組織名・件数・正確な日付を除く(convtext.py sanitize / scan)

CONV_STATE=${TB_CONV_STATE_DIR:-$HERDR_STATE_DIR/conv}
CONV_KIND_RE='^[a-z][a-z0-9-]{0,39}$'
CONV_DIRECTION_RE='^[a-z][a-z0-9-]{1,40}$'
CONV_EVIDENCE="human-edit human-written other-source agent-generated"
CONV_PAIR_RE='^pair-[0-9]{14}-[0-9a-f]{4}$'

conv_py() {
  command -v python3 >/dev/null || die "python3 が必要です(会話の学習の文字列処理)"
  python3 -I "$TB_ROOT/lib/convtext.py" "$@"
}

# conversation/ の置き場(lib/conv.sh と同じ既定。TB_CONV_DIR で差し替え)
conv_kinds_dir() { printf '%s/kinds' "${TB_CONV_DIR:-$HERDR_ROOT/conversation}"; }
conv_patterns_file() { printf '%s/%s/patterns.conf' "$(conv_kinds_dir)" "$1"; }

# 種別の検査。conversation/kinds/ があれば、そこにある種別だけ(無ければ名前の規則だけ)
valid_conv_kind() {
  [[ $1 =~ $CONV_KIND_RE ]] || usage "種別は英小文字で始まる 40 字までの英小文字・数字・-: $1"
  local d; d=$(conv_kinds_dir)
  [[ ! -d $d || -d $d/$1 ]] || usage "未登録の種別です(conversation/kinds/$1 がありません): $1"
}

# 置き場を用意する。checkout の中(版の管理の下)には作らない
conv_state_init() {
  local d=$CONV_STATE root real
  [[ ! -L $d ]] || die "会話の記録の置き場がシンボリックリンクです: $d"
  ( umask 077; mkdir -p "$d/pairs" "$d/ledger" ) || die "会話の記録の置き場を作れません: $d"
  chmod 700 "$d" "$d/pairs" "$d/ledger"
  real=$(cd "$d" && pwd -P); root=$(cd "$HERDR_ROOT" && pwd -P)
  [[ $real != "$root" && $real != "$root"/* ]] || die "会話の記録は checkout の外に置きます(今の置き場: $d)"
}

# conv-learn の設定: threshold = N(既定 3、2 未満は 2)/ deny = 語, 語(名前や組織名など、残さない語)
conv_learn_get() {  # $1=キー → 値
  local line k v
  while IFS= read -r line || [[ -n $line ]]; do
    line=${line%%#*}; [[ $line == *=* ]] || continue
    k=$(trim "${line%%=*}"); v=$(trim "${line#*=}")
    [[ $k == "$1" ]] && { printf '%s' "$v"; return 0; }
  done < <(config_text conv-learn 2>/dev/null || true)
  return 1
}
conv_threshold() {
  local t; t=$(conv_learn_get threshold) || t=3
  [[ $t =~ ^[0-9]+$ ]] || t=3
  ((t >= 2)) || t=2   # 1 回の手直しで proposed にはしない
  printf '%s' "$t"
}
conv_deny() { local d; d=$(conv_learn_get deny) || d=""; d=${d//, /,}; printf '%s' "$d"; }

conv_clean() { conv_py sanitize --deny "$(conv_deny)" <<<"$1"; }   # 台帳とパターンに残す文
conv_clean_month() { conv_py sanitize --keep-month --deny "$(conv_deny)" <<<"$1"; }   # 組の状況メモ(年月までは残す)

conv_pair_dir() { printf '%s/pairs/%s/%s' "$CONV_STATE" "$1" "$2"; }
conv_pair_find() {  # $1=pair-id → meta.json のパス(無ければ 1)
  [[ $1 =~ $CONV_PAIR_RE ]] || usage "組の ID は pair-<日時 14 桁>-<16 進 4 桁>: $1"
  local f
  for f in "$CONV_STATE"/pairs/*/"$1"/meta.json; do [[ -f $f ]] && { printf '%s' "$f"; return 0; }; done
  return 1
}

# ---------- 組の記録 ----------
# tb conv-pair-add --kind K --draft-file F --final-file F --situation S --intent I --posted [--points-offered N --points-accepted M]
# 人が「送った」ことを --posted で明示したときだけ記録する(人の端末だけ)。下書きが空なら人が一から書いたもの
cmd_conv-pair-add() {
  local kind="" df="" ff="" sit="" intent="" posted=0 po="" pa=""
  while [[ $# -gt 0 ]]; do
    case $1 in
      --kind) need_arg "$@"; kind=$2; shift 2;;
      --draft-file) need_arg "$@"; df=$2; shift 2;;
      --final-file) need_arg "$@"; ff=$2; shift 2;;
      --situation) need_arg "$@"; sit=$2; shift 2;;
      --intent) need_arg "$@"; intent=$2; shift 2;;
      --points-offered) need_arg "$@"; po=$2; shift 2;;
      --points-accepted) need_arg "$@"; pa=$2; shift 2;;
      --posted) posted=1; shift;;
      *) usage "conv-pair-add: 不明な引数 $1";;
    esac
  done
  [[ -n $kind && -n $ff && -n $df ]] || usage "conv-pair-add --kind K --draft-file F --final-file F --situation S --intent I --posted"
  valid_conv_kind "$kind"
  ((posted)) || usage "conv-pair-add: 人が送ったあとにだけ記録します(--posted を付ける)"
  ! blank "$sit" && ! blank "$intent" || usage "conv-pair-add: --situation と --intent(一般化した状況と意図)が要ります"
  [[ -f $df && -r $df ]] || usage "conv-pair-add: 下書きのファイルを読めません: $df"
  [[ -f $ff && -r $ff && -s $ff ]] || usage "conv-pair-add: 送った文のファイルが無いか空です: $ff"
  if [[ -n $po || -n $pa ]]; then
    [[ $po =~ ^[0-9]+$ && $pa =~ ^[0-9]+$ ]] && ((pa <= po)) || usage "conv-pair-add: --points-offered と --points-accepted は 0 以上の整数で、受け入れた数 <= 示された数"
  fi
  require_human "tb conv-pair-add"
  conv_state_init
  local id d ratio dl fl
  id="pair-$(date -u +%Y%m%d%H%M%S)-$(od -An -N2 -tx1 /dev/urandom | tr -d ' \n')"
  d=$(conv_pair_dir "$kind" "$id")
  ( umask 077; mkdir -p "$d" ) || die "組の置き場を作れません: $d"
  ( umask 077; cp "$df" "$d/draft.txt" && cp "$ff" "$d/final.txt" ) || die "組を書けません: $d"
  ratio=$(conv_py ratio "$d/draft.txt" "$d/final.txt") || die "編集の割合を測れません"
  dl=$(LC_ALL=C.UTF-8 wc -m <"$d/draft.txt" | tr -d ' '); fl=$(LC_ALL=C.UTF-8 wc -m <"$d/final.txt" | tr -d ' ')
  ( umask 077; jq -n --arg id "$id" --arg kind "$kind" --arg sit "$(conv_clean_month "$sit")" --arg intent "$(conv_clean_month "$intent")" \
      --arg at "$(now)" --argjson ratio "$ratio" --argjson dl "$dl" --argjson fl "$fl" \
      --arg po "$po" --arg pa "$pa" \
      '{id:$id, kind:$kind, situation:$sit, intent:$intent, recorded_at:$at, posted_by:"human",
        edit_ratio:$ratio, draft_chars:$dl, final_chars:$fl, human_written:($dl==0),
        points_offered:(if $po=="" then null else ($po|tonumber) end), points_accepted:(if $pa=="" then null else ($pa|tonumber) end)}' >"$d/meta.json" ) \
    || die "組の記録を書けません: $d"
  printf '%s\t%s\n' "$id" "$ratio"
}

# tb conv-pairs [--kind K] [--json]: 組の一覧(本文は出さない。ID 種別 記録日時 編集の割合 状況)
cmd_conv-pairs() {
  local kind="" json=0
  while [[ $# -gt 0 ]]; do
    case $1 in --kind) need_arg "$@"; kind=$2; shift 2;; --json) json=1; shift;; *) usage "conv-pairs [--kind K] [--json]";; esac
  done
  [[ -z $kind ]] || valid_conv_kind "$kind"
  local out; out=$(conv_pairs_json "$kind")
  if ((json)); then printf '%s\n' "$out"; else jq -r '.[] | [.id, .kind, .recorded_at, (.edit_ratio|tostring), .situation] | @tsv' <<<"$out"; fi
}
conv_pairs_json() {  # [$1=kind] → 組の meta の配列(記録の古い順)
  local glob="$CONV_STATE/pairs/${1:-*}/pair-*/meta.json" files=()
  # shellcheck disable=SC2206  # glob を展開する
  files=($glob)
  [[ -f ${files[0]:-} ]] || { echo '[]'; return 0; }
  jq -s 'sort_by(.recorded_at, .id)' "${files[@]}"
}

# tb conv-pair-show <pair-id>: 記録と本文(この機械の中だけで読む)
cmd_conv-pair-show() {
  [[ $# -eq 1 ]] || usage "conv-pair-show <pair-id>"
  local m d; m=$(conv_pair_find "$1") || die "組が見つかりません: $1"; d=$(dirname "$m")
  jq . "$m"; echo "--- 下書き"; cat "$d/draft.txt"; echo; echo "--- 送った文"; cat "$d/final.txt"; echo
}

# ---------- 候補の台帳 ----------
# 台帳の列(タブ区切り): direction state permissive situation intent ending conclusion avoid desc created updated pattern_id
# 証拠の列: direction evidence source(pair-id か ref:<説明>) situation_key added_at added_by
# タブ区切りの 1 行を配列 F に分ける(read は続くタブを 1 つにまとめて空の列を落とすので使わない)
conv_split() { mapfile -t F < <(printf '%s\n' "$1" | tr '\t' '\n'); }
conv_ledger_file() { printf '%s/ledger/%s.tsv' "$CONV_STATE" "$1"; }
conv_evidence_file() { printf '%s/ledger/%s.evidence.tsv' "$CONV_STATE" "$1"; }
conv_ledger_row() {  # $1=kind $2=direction → 行(無ければ空)
  local f; f=$(conv_ledger_file "$1"); [[ -f $f ]] || return 0
  awk -F'\t' -v d="$2" '$1==d {print; exit}' "$f"
}
conv_ledger_put() {  # $1=kind $2=行(1 列目が direction)。同じ direction の行を置き換え、無ければ足す
  local f tmp; f=$(conv_ledger_file "$1"); tmp=$(mktemp "$f.XXXXXX")
  { [[ -f $f ]] && awk -F'\t' -v d="${2%%$'\t'*}" '$1!=d' "$f"; printf '%s\n' "$2"; } >"$tmp"
  chmod 600 "$tmp"; mv "$tmp" "$f"
}
# 数える: 「別々の状況」の数。agent-generated は数えない。同じ状況のキーは 1 回
conv_counts() {  # $1=kind $2=direction → "counted human-edit human-written other-source agent-generated"
  local f; f=$(conv_evidence_file "$1")
  [[ -f $f ]] || { echo "0 0 0 0 0"; return 0; }
  awk -F'\t' -v d="$2" '$1==d {
      n[$2]++
      if ($2 != "agent-generated" && !($4 in seen)) { seen[$4]=1; c++ }
    } END { printf "%d %d %d %d %d\n", c, n["human-edit"], n["human-written"], n["other-source"], n["agent-generated"] }' "$f"
}
# 状態を数え直す: candidate で件数がしきい値以上なら proposed。adopted / rejected は人が決めたので変えない
conv_refresh() {  # $1=kind $2=direction
  local row st c th; row=$(conv_ledger_row "$1" "$2"); [[ -n $row ]] || return 0
  IFS=$'\t' read -r _ st _ <<<"$row"
  read -r c _ <<<"$(conv_counts "$1" "$2")"; th=$(conv_threshold)
  if [[ $st == candidate ]] && ((c >= th)); then conv_ledger_put "$1" "$(awk -F'\t' -v OFS='\t' -v t="$(now)" '{ $2="proposed"; $11=t; print }' <<<"$row")"
  elif [[ $st == proposed ]] && ((c < th)); then conv_ledger_put "$1" "$(awk -F'\t' -v OFS='\t' -v t="$(now)" '{ $2="candidate"; $11=t; print }' <<<"$row")"; fi
}

# tb conv-candidate-add --kind K --direction D --desc 説明 --evidence E (--pair <pair-id> | --ref 出どころの説明)
#     [--situation S --intent I --ending E --conclusion first|last --avoid A] [--permissive]
# 1 回の手直し = 候補 1 件の証拠 1 つ。説明は「何をどう変えたか」で、本文は写さない(残す前に sanitize する)
cmd_conv-candidate-add() {
  local kind="" dir="" desc="" ev="" pair="" ref="" sit="" intent="" ending="" concl="" avoid="" perm=0
  while [[ $# -gt 0 ]]; do
    case $1 in
      --kind) need_arg "$@"; kind=$2; shift 2;;
      --direction) need_arg "$@"; dir=$2; shift 2;;
      --desc) need_arg "$@"; desc=$2; shift 2;;
      --evidence) need_arg "$@"; ev=$2; shift 2;;
      --pair) need_arg "$@"; pair=$2; shift 2;;
      --ref) need_arg "$@"; ref=$2; shift 2;;
      --situation) need_arg "$@"; sit=$2; shift 2;;
      --intent) need_arg "$@"; intent=$2; shift 2;;
      --ending) need_arg "$@"; ending=$2; shift 2;;
      --conclusion) need_arg "$@"; concl=$2; shift 2;;
      --avoid) need_arg "$@"; avoid=$2; shift 2;;
      --permissive) perm=1; shift;;
      *) usage "conv-candidate-add: 不明な引数 $1";;
    esac
  done
  [[ -n $kind && -n $dir && -n $ev ]] && ! blank "$desc" || usage "conv-candidate-add --kind K --direction D --desc 説明 --evidence E (--pair ID | --ref 説明)"
  valid_conv_kind "$kind"
  [[ $dir =~ $CONV_DIRECTION_RE ]] || usage "--direction は変更の向きの短い名前(英小文字・数字・-、例 conclusion-first): $dir"
  [[ " $CONV_EVIDENCE " == *" $ev "* ]] || usage "--evidence は $CONV_EVIDENCE のどれか: $ev"
  [[ -z $concl || $concl == first || $concl == last ]] || usage "--conclusion は first か last: $concl"
  local src skey m
  case $ev in
    human-edit|human-written)
      [[ -n $pair && -z $ref ]] || usage "conv-candidate-add: --evidence $ev には --pair(人が送った組)が要ります"
      m=$(conv_pair_find "$pair") || die "組が見つかりません: $pair"
      [[ $(jq -r .kind "$m") == "$kind" ]] || die "組 $pair は種別 $(jq -r .kind "$m") のものです"
      [[ $(jq -r .posted_by "$m") == human ]] || die "組 $pair は人が送った記録ではありません"
      if [[ $ev == human-edit ]]; then
        [[ $(jq -r '.edit_ratio > 0 and (.human_written|not)' "$m") == true ]] || die "組 $pair には人の手直しがありません(下書きのまま、または一から書いたもの。一から書いたものは human-written)"
      else
        [[ $(jq -r .human_written "$m") == true ]] || die "組 $pair は下書きを元にしています(human-written は人が一から書いた組だけ)"
      fi
      src=$pair; skey=$(jq -r .situation "$m");;
    other-source)
      [[ -n $ref && -z $pair ]] || usage "conv-candidate-add: --evidence other-source には --ref(出どころの一般化した説明)が要ります"
      src="ref:$(conv_clean "$ref")"; skey=$src;;
    agent-generated)
      # agent の下書きや声の役割の指摘。残すが件数には数えない
      src=${pair:+$pair}; src=${src:-"ref:$(conv_clean "${ref:-agent}")"}; skey=$src;;
  esac
  conv_state_init
  local row ef by=human cnt
  [[ -z ${HERDR_PANE_ID:-} ]] || by=$(pane_owner_name "$HERDR_PANE_ID" 2>/dev/null || true); by=${by:-human}
  row=$(conv_ledger_row "$kind" "$dir")
  if [[ -z $row ]]; then
    row=$(printf '%s\t' "$dir" candidate "$perm" "$(conv_clean "$sit")" "$(conv_clean "$intent")" "$(conv_clean "$ending")" "$concl" "$(conv_clean "$avoid")" "$(conv_clean "$desc")" "$(now)" "$(now)"); row+="-"
    conv_ledger_put "$kind" "$row"
  elif ((perm)); then   # 一度でも許す方向と言われた候補は、ずっと許す方向として扱う
    conv_ledger_put "$kind" "$(awk -F'\t' -v OFS='\t' -v t="$(now)" '{ $3=1; $11=t; print }' <<<"$row")"
  fi
  ef=$(conv_evidence_file "$kind")
  if [[ -f $ef ]] && awk -F'\t' -v d="$dir" -v s="$src" '$1==d && $3==s {f=1} END{exit !f}' "$ef"; then
    echo "同じ証拠は記録済みです($dir $src)" >&2
  else
    ( umask 077; printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$dir" "$ev" "$src" "$skey" "$(now)" "$by" >>"$ef" )
  fi
  conv_refresh "$kind" "$dir"
  cnt=$(conv_counts "$kind" "$dir")
  printf '%s\t%s\t%s/%s\n' "$dir" "$(conv_ledger_row "$kind" "$dir" | cut -f2)" "${cnt%% *}" "$(conv_threshold)"
}

# tb conv-ledger [--kind K] [--json]: 台帳(種別ごと)。列: 種別 向き 状態 数えた件数/しきい値 human-edit human-written other-source agent-generated 許す方向 説明
cmd_conv-ledger() {
  local kind="" json=0 f k line dir st perm sit intent ending concl avoid desc upd pid c he hw os ag
  while [[ $# -gt 0 ]]; do
    case $1 in --kind) need_arg "$@"; kind=$2; shift 2;; --json) json=1; shift;; *) usage "conv-ledger [--kind K] [--json]";; esac
  done
  [[ -z $kind ]] || valid_conv_kind "$kind"
  local th; th=$(conv_threshold)
  {
    for f in "$CONV_STATE"/ledger/*.tsv; do
      [[ -f $f && $f != *.evidence.tsv ]] || continue
      k=$(basename "$f" .tsv); [[ -z $kind || $k == "$kind" ]] || continue
      while IFS= read -r line; do
        conv_split "$line"; dir=${F[0]} st=${F[1]} perm=${F[2]} sit=${F[3]} intent=${F[4]} ending=${F[5]} concl=${F[6]} avoid=${F[7]} desc=${F[8]} upd=${F[10]} pid=${F[11]}
        read -r c he hw os ag <<<"$(conv_counts "$k" "$dir")"
        jq -nc --arg kind "$k" --arg d "$dir" --arg st "$st" --argjson perm "${perm:-0}" --argjson c "$c" --argjson th "$th" \
          --argjson he "$he" --argjson hw "$hw" --argjson os "$os" --argjson ag "$ag" \
          --arg sit "$sit" --arg intent "$intent" --arg ending "$ending" --arg concl "$concl" --arg avoid "$avoid" --arg desc "$desc" --arg upd "$upd" --arg pid "$pid" \
          '{kind:$kind, direction:$d, state:$st, permissive:($perm==1), counted:$c, threshold:$th,
            evidence:{"human-edit":$he, "human-written":$hw, "other-source":$os, "agent-generated":$ag},
            situation:$sit, intent:$intent, ending:$ending, conclusion:$concl, avoid:$avoid, desc:$desc, updated_at:$upd,
            pattern_id:(if $pid=="-" then null else $pid end)}'
      done <"$f"
    done
  } | if ((json)); then jq -s .; else jq -r '[.kind, .direction, .state, "\(.counted)/\(.threshold)", .evidence["human-edit"], .evidence["human-written"], .evidence["other-source"], .evidence["agent-generated"], (if .permissive then "permissive" else "-" end), .desc] | @tsv'; fi
}

# tb conv-candidate-adopt <kind> <direction> [--approve-permissive] [--situation S --intent I --ending E --conclusion first|last --avoid A]
# 人だけ。proposed(別々の状況でしきい値以上)だけを採用できる。書く前に名前・番号・URL などが無いか確かめ、
# conversation/kinds/<kind>/patterns.conf に adopted の行を足す
cmd_conv-candidate-adopt() {
  [[ $# -ge 2 ]] || usage "conv-candidate-adopt <kind> <direction> [--approve-permissive] [--situation S --intent I --ending E --conclusion first|last --avoid A]"
  local kind=$1 dir=$2 ap=0 sit="" intent="" ending="" concl="" avoid="" set_sit=0 set_int=0 set_end=0 set_con=0 set_av=0; shift 2
  while [[ $# -gt 0 ]]; do
    case $1 in
      --approve-permissive) ap=1; shift;;
      --situation) need_arg "$@"; sit=$2; set_sit=1; shift 2;;
      --intent) need_arg "$@"; intent=$2; set_int=1; shift 2;;
      --ending) need_arg "$@"; ending=$2; set_end=1; shift 2;;
      --conclusion) need_arg "$@"; concl=$2; set_con=1; shift 2;;
      --avoid) need_arg "$@"; avoid=$2; set_av=1; shift 2;;
      *) usage "conv-candidate-adopt: 不明な引数 $1";;
    esac
  done
  valid_conv_kind "$kind"
  require_human "tb conv-candidate-adopt"
  conv_refresh "$kind" "$dir"
  local row st perm s0 i0 e0 c0 a0 c th pf
  row=$(conv_ledger_row "$kind" "$dir"); [[ -n $row ]] || die "候補がありません: $kind $dir"
  conv_split "$row"; st=${F[1]} perm=${F[2]} s0=${F[3]} i0=${F[4]} e0=${F[5]} c0=${F[6]} a0=${F[7]}
  read -r c _ <<<"$(conv_counts "$kind" "$dir")"; th=$(conv_threshold)
  case $st in
    proposed) ;;
    candidate) die "まだ候補です: 別々の状況での人の手直しが $c 件(しきい値 $th)。1 回の手直しでは採用しません";;
    adopted) die "採用済みです: $kind $dir";;
    *) die "状態が $st の候補は採用できません: $kind $dir";;
  esac
  ((perm == 0 || ap)) || die "許す方向の変更です。採用するなら --approve-permissive を付けて明示してください"
  ((set_sit)) && s0=$(conv_clean "$sit"); ((set_int)) && i0=$(conv_clean "$intent"); ((set_end)) && e0=$(conv_clean "$ending")
  ((set_con)) && c0=$concl; ((set_av)) && a0=$(conv_clean "$avoid")
  [[ $c0 == first || $c0 == last ]] || die "conclusion(first か last)が決まっていません。--conclusion で渡してください"
  blank "$s0" && die "situation が空です。--situation で渡してください"
  blank "$i0" && die "intent が空です。--intent で渡してください"
  # 名前・番号・URL・組織名・件数が残っていないか(置き換えの印 <n> などもパターンには残さない)
  local all bad; all="$s0 | $i0 | $e0 | $a0"
  [[ $all != *'<'*'>'* ]] || die "置き換えた印(<name> <n> など)が残っています。一般化した言い方に直して --situation などで渡してください: $all"
  if ! bad=$(conv_py scan --deny "$(conv_deny)" <<<"$all"); then die "パターンに残せないものがあります: $(tr '\n' ' ' <<<"$bad")"; fi
  pf=$(conv_patterns_file "$kind")
  [[ -d $(dirname "$pf") ]] || die "種別の置き場がありません: $(dirname "$pf")"
  [[ ! -L $pf ]] || die "patterns.conf がシンボリックリンクです: $pf"
  # evidence 列は数えた証拠のうち最も強いもの(人の手直し > 人が書いた > ほかの出どころ)
  local he hw ev pid line
  read -r _ he hw _ <<<"$(conv_counts "$kind" "$dir")"
  if ((he > 0)); then ev=human-edit; elif ((hw > 0)); then ev=human-written; else ev=other-source; fi
  pid="P-${dir//-/}"
  if [[ -f $pf ]] && grep -Eq "^[[:space:]]*$pid[[:space:]]*\|" "$pf"; then pid="$pid$(od -An -N2 -tx1 /dev/urandom | tr -d ' \n')"; fi
  line="$pid | adopted | $ev | $s0 | $i0 | $e0 | $c0 | $a0"
  if declare -F cmd_conv-lint >/dev/null; then   # 下書き側の lint(#95)があれば、足したあとの本文で確かめる
    local tmp; tmp=$(mktemp); { [[ -f $pf ]] && cat "$pf"; printf '%s\n' "$line"; } >"$tmp"
    conv_lint_file "$tmp" >/dev/null 2>&1 || { rm -f "$tmp"; die "patterns.conf の検査(tb conv-lint)を通りません: $line"; }
    rm -f "$tmp"
  fi
  [[ ! -f $pf || -z $(tail -c1 "$pf") ]] || echo >>"$pf"
  printf '%s\n' "$line" >>"$pf"
  conv_ledger_put "$kind" "$(awk -F'\t' -v OFS='\t' -v t="$(now)" -v p="$pid" -v s="$s0" -v i="$i0" -v e="$e0" -v c="$c0" -v a="$a0" \
    '{ $2="adopted"; $4=s; $5=i; $6=e; $7=c; $8=a; $11=t; $12=p; print }' <<<"$row")"
  echo "$pid adopted → $pf"
}

# tb conv-candidate-reject <kind> <direction>: 人だけ。証拠は残し、状態だけ rejected にする
cmd_conv-candidate-reject() {
  [[ $# -eq 2 ]] || usage "conv-candidate-reject <kind> <direction>"
  valid_conv_kind "$1"; require_human "tb conv-candidate-reject"
  local row; row=$(conv_ledger_row "$1" "$2"); [[ -n $row ]] || die "候補がありません: $1 $2"
  [[ $(cut -f2 <<<"$row") != adopted ]] || die "採用済みです。外すときは patterns.conf の行を人が消してください"
  conv_ledger_put "$1" "$(awk -F'\t' -v OFS='\t' -v t="$(now)" '{ $2="rejected"; $11=t; print }' <<<"$row")"
  echo "$2 rejected"
}

# tb conv-scan: 標準入力の文に、パターンに残せないもの(名前・番号・URL など)が無いか。0=無い 1=ある(種類<TAB>該当)
cmd_conv-scan() {
  [[ $# -eq 0 ]] || usage "conv-scan < 文"
  conv_py scan --deny "$(conv_deny)"
}

# tb conv-learn-purge [--kind K] --yes: 組と台帳を消す(人だけ)。patterns.conf の採用済みの行と段階の記録は消さない
cmd_conv-learn-purge() {
  local kind="" yes=0
  while [[ $# -gt 0 ]]; do
    case $1 in --kind) need_arg "$@"; kind=$2; shift 2;; --yes) yes=1; shift;; *) usage "conv-learn-purge [--kind K] --yes";; esac
  done
  ((yes)) || usage "conv-learn-purge: 組と台帳を消します。よければ --yes を付ける"
  [[ -z $kind ]] || [[ $kind =~ $CONV_KIND_RE ]] || usage "種別の名前が不正です: $kind"
  require_human "tb conv-learn-purge"
  if [[ -n $kind ]]; then
    rm -rf "${CONV_STATE:?}/pairs/$kind" "$CONV_STATE/ledger/$kind.tsv" "$CONV_STATE/ledger/$kind.evidence.tsv"
  else
    rm -rf "${CONV_STATE:?}/pairs" "$CONV_STATE/ledger"
  fi
  echo "消しました: ${kind:-全種別}(採用済みのパターンと段階の記録は残っています)"
}
