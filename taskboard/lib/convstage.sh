# shellcheck shell=bash
# 会話チームの段階的な自動化(#98)。bin/tb から source される。単体では実行しない。設計は taskboard/docs/conversation-learning.md
#
#   段階 0  下書きだけ。人が読み、直し、送る
#   段階 1  声の役割が下書きを読んで短い指摘を返す
#   段階 2  人が印を付けた種別は、メッセージの道具に「下書きとして保存」する(送らない)
#   段階 3  人が決めた種別 × 相手の区分の一覧だけ、1 通ごとに人が 1 回承認すれば送れる
#
# 決まり:
# - 段階を変えるのは人だけ(tb conv-stage-set)。変えるたびに、その時の測った値と一緒に記録する(stage-log.tsv)
# - 下げるのはいつでも 1 回の操作でできる。記録したデータは消さない(tb conv-stage-set <kind> 1 / tb conv-stage-reset)
# - 問題を指摘する・断る・組織の外へ出る内容は、設定で段階 1 より上に進めない(high = の種別、--severity high、--dest-vis external)。
#   model の判断ではなく設定で決める
# - 送る・承認する・状態を変えるのは、どの段階でも人の承認が要る。段階 3 でも 1 通ごと(tb conv-gate は post に allow を返さない)
# - 保存・送信の前に情報の流れを確かめる(元の情報を見られる範囲 >= 送り先を見られる範囲)
# - メッセージの道具に「下書き保存」があると人が確かめるまで(draft-save = available)、段階 2 には上げない
#
# 段階の今の値は stage-log.tsv の最後の記録(記録が無い種別は 0)。設定(しきい値・上限・一覧)は tb config の conv-stages。

CONV_VIS="private team org external"   # 見られる範囲の狭い順

conv_stage_log() { printf '%s/stage-log.tsv' "$CONV_STATE"; }
conv_incident_log() { printf '%s/incidents.tsv' "$CONV_STATE"; }

# conv-stages の設定。「キー = 値」の行(# 以降は注釈)。キーは下の既定値の一覧を見る
conv_stage_get() {  # $1=キー [$2=既定値] → 値
  local line k v
  while IFS= read -r line || [[ -n $line ]]; do
    line=${line%%#*}; [[ $line == *=* ]] || continue
    k=$(trim "${line%%=*}"); v=$(trim "${line#*=}")
    [[ $k == "$1" ]] && { printf '%s' "$v"; return 0; }
  done < <(config_text conv-stages 2>/dev/null || true)
  [[ $# -ge 2 ]] && { printf '%s' "$2"; return 0; }
  return 1
}
conv_stage_num() {  # $1=キー $2=既定値。数として読めなければ既定値
  local v; v=$(conv_stage_get "$1" "$2")
  [[ $v =~ ^[0-9]+(\.[0-9]+)?$ ]] || v=$2
  printf '%s' "$v"
}
conv_in_list() {  # $1=キー(カンマ区切りの一覧) $2=値
  local v; v=$(conv_stage_get "$1" "") || return 1
  [[ ",${v// /}," == *",$2,"* ]]
}
conv_is_high() { conv_in_list high "$1"; }
conv_stage3_ok() {  # $1=kind [$2=相手の区分]。$2 が空なら種別の行が 1 つでもあれば 0
  local v e; v=$(conv_stage_get stage3 "") || return 1
  for e in ${v//,/ }; do
    [[ -z $2 && ${e%%:*} == "$1" ]] && return 0
    [[ $e == "$1:$2" ]] && return 0
  done
  return 1
}

conv_stage_of() {  # $1=kind → 今の段階(記録が無ければ 0)
  local f; f=$(conv_stage_log); [[ -f $f ]] || { echo 0; return 0; }
  awk -F'\t' -v k="$1" '$2==k {s=$4} END {print (s=="" ? 0 : s)}' "$f"
}
conv_stage_since() {  # $1=kind → 今の段階になった日時(無ければ空)
  local f; f=$(conv_stage_log); [[ -f $f ]] || return 0
  awk -F'\t' -v k="$1" '$2==k {t=$1} END {print t}' "$f"
}

# 測った値(JSON 1 行)。組の meta と台帳と事故の記録から。本文は読まない
conv_metrics() {  # $1=kind
  local kind=$1 win adopted inc since
  win=$(conv_stage_num window-days 28)
  adopted=$(cmd_conv-ledger --kind "$kind" --json 2>/dev/null | jq '[.[] | select(.state=="adopted")] | length' 2>/dev/null || echo 0)
  inc=""; [[ -f $(conv_incident_log) ]] && inc=$(awk -F'\t' -v k="$kind" '$2==k {t=$1} END {print t}' "$(conv_incident_log)")
  since=$(conv_stage_since "$kind")
  conv_pairs_json "$kind" | jq -c --argjson win "$win" --argjson adopted "${adopted:-0}" --arg inc "$inc" --arg since "$since" --arg stage "$(conv_stage_of "$kind")" '
    def days(t): ((now - (t | fromdateiso8601)) / 86400 | floor);
    (map(select(days(.recorded_at) < $win))) as $w
    | {stage: ($stage|tonumber),
       pairs: length,
       window_days: $win,
       window_samples: ($w | length),
       window_edit_ratio: (if ($w|length) == 0 then null else (($w | map(.edit_ratio) | add) / ($w|length) * 1000 | round / 1000) end),
       role_points_offered: (map(.points_offered // 0) | add // 0),
       role_points_accepted: (map(.points_accepted // 0) | add // 0),
       adopted_patterns: $adopted,
       last_incident: (if $inc == "" then null else $inc end),
       quiet_days: (if $inc != "" then days($inc) elif length > 0 then days(.[0].recorded_at) else 0 end),
       days_at_stage: (if $since == "" then null else days($since) end)}
    | .role_acceptance = (if .role_points_offered == 0 then null else (.role_points_accepted / .role_points_offered * 1000 | round / 1000) end)'
}

# 段階 $2 に上げる条件。満たさないものを 1 行ずつ CONV_UNMET に入れる。$3=測った値。満たせば 0
conv_stage_check() {  # $1=kind $2=段階 $3=metrics
  local kind=$1 to=$2 m=$3 v
  CONV_UNMET=""
  _unmet() { CONV_UNMET+="$1"$'\n'; }
  _jq() { jq -r "$1" <<<"$m"; }
  case $to in
    1) v=$(conv_stage_num min-pairs.1 20)
       (( $(_jq .pairs) >= v )) || _unmet "記録した組が $(_jq .pairs) 件(min-pairs.1 = $v)";;
    2) [[ $(conv_stage_get draft-save unverified) == available ]] || _unmet "下書き保存がメッセージの道具にあると確かめていません(draft-save = $(conv_stage_get draft-save unverified))"
       v=$(conv_stage_num min-samples.2 20)
       (( $(_jq .window_samples) >= v )) || _unmet "直近 $(_jq .window_days) 日の組が $(_jq .window_samples) 件(min-samples.2 = $v)"
       v=$(conv_stage_num max-edit-ratio.2 0.15)
       [[ $(jq --argjson v "$v" '.window_edit_ratio != null and .window_edit_ratio <= $v' <<<"$m") == true ]] || _unmet "直近の編集の割合が $(_jq .window_edit_ratio)(max-edit-ratio.2 = $v)"
       v=$(conv_stage_num min-adopted.2 3)
       (( $(_jq .adopted_patterns) >= v )) || _unmet "採用したパターンが $(_jq .adopted_patterns) 件(min-adopted.2 = $v)"
       v=$(conv_stage_num quiet-days.2 21)
       (( $(_jq .quiet_days) >= v )) || _unmet "事故の無い日数が $(_jq .quiet_days)(quiet-days.2 = $v)";;
    3) conv_stage3_ok "$kind" "" || _unmet "stage3 = の一覧にこの種別がありません"
       v=$(conv_stage_num quiet-days.3 42)
       [[ $(jq --argjson v "$v" '.stage == 2 and (.days_at_stage // 0) >= $v' <<<"$m") == true ]] || _unmet "段階 2 の日数が $(_jq '.days_at_stage // 0')(段階 $(_jq .stage)。quiet-days.3 = $v)"
       (( $(_jq .quiet_days) >= v )) || _unmet "事故の無い日数が $(_jq .quiet_days)(quiet-days.3 = $v)";;
  esac
  unset -f _unmet _jq
  [[ -z $CONV_UNMET ]]
}

conv_stage_record() {  # $1=kind $2=from $3=to $4=how(raise|lower|override|reset) $5=理由 $6=metrics
  conv_state_init
  ( umask 077; printf '%s\t%s\t%s\t%s\t%s\thuman\t%s\t%s\n' "$(now)" "$1" "$2" "$3" "$4" "$(conv_clean "$5")" "$6" >>"$(conv_stage_log)" )
}

# tb conv-stage [<kind>] [--json]: 種別ごとの段階と測った値、次の段階の条件
cmd_conv-stage() {
  local kind="" json=0 k kinds=() d
  while [[ $# -gt 0 ]]; do
    case $1 in --json) json=1; shift;; -*) usage "conv-stage [<kind>] [--json]";; *) kind=$1; shift;; esac
  done
  if [[ -n $kind ]]; then valid_conv_kind "$kind"; kinds=("$kind")
  else
    d=$(conv_kinds_dir)
    if [[ -d $d ]]; then for k in "$d"/*/; do [[ -d $k ]] && kinds+=("$(basename "$k")"); done; fi
    # 段階の記録や組だけある種別も出す
    for k in "$CONV_STATE"/pairs/*/; do [[ -d $k ]] && kinds+=("$(basename "$k")"); done
    [[ -f $(conv_stage_log) ]] && while IFS= read -r k; do kinds+=("$k"); done < <(cut -f2 "$(conv_stage_log)")
    local all; all=$(printf '%s\n' ${kinds[@]+"${kinds[@]}"} | grep -E "$CONV_KIND_RE" | sort -u || true)
    kinds=(); while IFS= read -r k; do [[ -n $k ]] && kinds+=("$k"); done <<<"$all"
  fi
  local m s next cap out=()
  for k in ${kinds[@]+"${kinds[@]}"}; do
    m=$(conv_metrics "$k"); s=$(jq .stage <<<"$m"); cap=3; conv_is_high "$k" && cap=1
    next=null
    if ((s < cap)); then
      if conv_stage_check "$k" $((s + 1)) "$m"; then next='{"ok":true,"unmet":[]}'
      else next=$(jq -Rn --arg u "${CONV_UNMET%$'\n'}" '{ok:false, unmet:($u | split("\n"))}'); fi
    fi
    out+=("$(jq -c --arg k "$k" --argjson cap "$cap" --argjson next "$next" '{kind:$k, max_stage:$cap} + . + {next:$next}' <<<"$m")")
  done
  ((${#out[@]})) || { ((json)) && echo '[]'; return 0; }
  if ((json)); then printf '%s\n' "${out[@]}" | jq -s .
  else
    printf '%s\n' "${out[@]}" | jq -r '[.kind, "stage \(.stage)/\(.max_stage)", "pairs \(.pairs)", "edit \(.window_edit_ratio // "-")", "adopted \(.adopted_patterns)", "quiet \(.quiet_days)d",
      (if .next == null then "next -" elif .next.ok then "next ok" else "next: " + (.next.unmet | join("; ")) end)] | @tsv'
  fi
}

# tb conv-stage-set <kind> <0-3> --reason R [--override]: 人だけ。上げるのは 1 段ずつ、条件を満たすときだけ
# (--override で条件を満たさずに上げたことも記録する)。上限(high、stage3 の一覧、下書き保存の有無)は --override でも越えない
cmd_conv-stage-set() {
  [[ $# -ge 2 ]] || usage "conv-stage-set <kind> <0-3> --reason R [--override]"
  local kind=$1 to=$2 reason="" ov=0; shift 2
  while [[ $# -gt 0 ]]; do
    case $1 in --reason) need_arg "$@"; reason=$2; shift 2;; --override) ov=1; shift;; *) usage "conv-stage-set: 不明な引数 $1";; esac
  done
  valid_conv_kind "$kind"
  [[ $to =~ ^[0-3]$ ]] || usage "段階は 0〜3: $to"
  ! blank "$reason" || usage "conv-stage-set: --reason(人が決めた理由)が要ります"
  require_human "tb conv-stage-set"
  local from m how
  from=$(conv_stage_of "$kind"); m=$(conv_metrics "$kind")
  if ((to == from)); then echo "$kind は既に段階 $to です"; return 0; fi
  if ((to < from)); then
    how=lower
  else
    ((to == from + 1)) || die "上げるのは 1 段ずつです(今 $from)"
    # 設定で決まる上限: ここは --override でも越えない
    if ((to >= 2)) && conv_is_high "$kind"; then echo "tb: $kind は high(問題の指摘・断り・組織の外)の種別なので、段階 1 より上にしません(conv-stages の high =)" >&2; return 4; fi
    if ((to == 2)) && [[ $(conv_stage_get draft-save unverified) != available ]]; then echo "tb: 下書き保存がメッセージの道具にあると確かめるまで段階 2 にしません(conv-stages の draft-save = available)" >&2; return 4; fi
    if ((to == 3)) && ! conv_stage3_ok "$kind" ""; then echo "tb: stage3 = の一覧にこの種別がありません(種別:相手の区分 を人が書く)" >&2; return 4; fi
    if conv_stage_check "$kind" "$to" "$m"; then how=raise
    elif ((ov)); then how=override
    else printf 'tb: 段階 %s に上げる条件を満たしていません(人が判断して上げるなら --override):\n%s' "$to" "$CONV_UNMET" >&2; return 4; fi
  fi
  conv_stage_record "$kind" "$from" "$to" "$how" "$reason" "$m"
  echo "$kind: 段階 $from → $to ($how)"
}

# tb conv-stage-reset [--to 0|1] --reason R: すべての種別を段階 0 か 1(既定 1)以下に戻す。データは消さない
cmd_conv-stage-reset() {
  local to=1 reason="" k s n=0
  while [[ $# -gt 0 ]]; do
    case $1 in --to) need_arg "$@"; to=$2; shift 2;; --reason) need_arg "$@"; reason=$2; shift 2;; *) usage "conv-stage-reset [--to 0|1] --reason R";; esac
  done
  [[ $to == 0 || $to == 1 ]] || usage "conv-stage-reset: --to は 0 か 1"
  ! blank "$reason" || usage "conv-stage-reset: --reason が要ります"
  require_human "tb conv-stage-reset"
  [[ -f $(conv_stage_log) ]] || { echo "段階を上げた種別はありません"; return 0; }
  while IFS= read -r k; do
    s=$(conv_stage_of "$k"); ((s > to)) || continue
    conv_stage_record "$k" "$s" "$to" reset "$reason" "$(conv_metrics "$k")"; n=$((n + 1))
    echo "$k: 段階 $s → $to"
  done < <(cut -f2 "$(conv_stage_log)" | sort -u)
  ((n)) || echo "段階 $to より上の種別はありません"
}

# tb conv-stage-log [<kind>] [--json]: 段階の変更の記録(日時 種別 前 後 how 誰 理由 測った値)
cmd_conv-stage-log() {
  local kind="" json=0
  while [[ $# -gt 0 ]]; do case $1 in --json) json=1; shift;; -*) usage "conv-stage-log [<kind>] [--json]";; *) kind=$1; shift;; esac; done
  local f; f=$(conv_stage_log); [[ -f $f ]] || { ((json)) && echo '[]'; return 0; }
  if ((json)); then
    awk -F'\t' -v k="$kind" 'k=="" || $2==k' "$f" | jq -Rn '[inputs | split("\t") | {at:.[0], kind:.[1], from:(.[2]|tonumber), to:(.[3]|tonumber), how:.[4], by:.[5], reason:.[6], metrics:(.[7]|fromjson)}]'
  else
    awk -F'\t' -v OFS='\t' -v k="$kind" 'k=="" || $2==k {print $1, $2, $3 "→" $4, $5, $6, $7}' "$f"
  fi
}

# tb conv-incident <kind> --note N: 事故(送ってはいけないものが出た、誤って保存された 等)を記録する。人だけ。
# 記録するだけで段階は変えない(下げるのは人が conv-stage-set で)。事故の無い日数はここから数え直す
cmd_conv-incident() {
  [[ $# -ge 1 ]] || usage "conv-incident <kind> --note N"
  local kind=$1 note=""; shift
  while [[ $# -gt 0 ]]; do case $1 in --note) need_arg "$@"; note=$2; shift 2;; *) usage "conv-incident <kind> --note N";; esac; done
  valid_conv_kind "$kind"; ! blank "$note" || usage "conv-incident: --note が要ります"
  require_human "tb conv-incident"
  conv_state_init
  ( umask 077; printf '%s\t%s\t%s\n' "$(now)" "$kind" "$(conv_clean "$note")" >>"$(conv_incident_log)" )
  echo "$kind: 事故を記録しました(段階は $(conv_stage_of "$kind") のまま。下げるなら tb conv-stage-set $kind 1 --reason ...)"
}

conv_vis_rank() { local i=0 v; for v in $CONV_VIS; do i=$((i + 1)); [[ $v == "$1" ]] && { echo $i; return 0; }; done; return 1; }

# tb conv-gate <kind> --action draft|review|save-draft|post [--counterpart C] [--severity low|normal|high]
#              [--source-vis V --dest-vis V]   (V: private|team|org|external)
# 決まった表だけで判定する(読み取りのみ)。1 行目 allow|ask|deny、続けて reason: と stage:。終了コード 0=allow 20=ask(1 通ごとに人の承認) 4=deny
# post は段階 3 の一覧に載っていても ask(allow は返さない)。一覧に無ければ deny(人が自分で送る)
cmd_conv-gate() {
  [[ $# -ge 1 ]] || usage "conv-gate <kind> --action draft|review|save-draft|post [--counterpart C] [--severity S] [--source-vis V --dest-vis V]"
  local kind=$1 act="" cp="" sev=normal sv="" dv=""; shift
  while [[ $# -gt 0 ]]; do
    case $1 in
      --action) need_arg "$@"; act=$2; shift 2;;
      --counterpart) need_arg "$@"; cp=$2; shift 2;;
      --severity) need_arg "$@"; sev=$2; shift 2;;
      --source-vis) need_arg "$@"; sv=$2; shift 2;;
      --dest-vis) need_arg "$@"; dv=$2; shift 2;;
      *) usage "conv-gate: 不明な引数 $1";;
    esac
  done
  valid_conv_kind "$kind"
  [[ $act =~ ^(draft|review|save-draft|post)$ ]] || usage "conv-gate: --action は draft|review|save-draft|post"
  [[ $sev =~ ^(low|normal|high)$ ]] || usage "conv-gate: --severity は low|normal|high"
  [[ -z $cp || $cp =~ ^[a-z][a-z0-9-]{0,39}$ ]] || usage "conv-gate: --counterpart は相手の区分の名前(英小文字・数字・-)"
  [[ -z $sv ]] || conv_vis_rank "$sv" >/dev/null || usage "conv-gate: --source-vis は $CONV_VIS のどれか"
  [[ -z $dv ]] || conv_vis_rank "$dv" >/dev/null || usage "conv-gate: --dest-vis は $CONV_VIS のどれか"
  local st eff cap=3 why=""
  st=$(conv_stage_of "$kind")
  if conv_is_high "$kind"; then cap=1; why="high の種別"; fi
  if [[ $sev == high ]]; then cap=1; why="severity high"; fi
  if [[ $dv == external ]]; then cap=1; why="組織の外へ出る"; fi
  eff=$((st < cap ? st : cap))
  _gate() { printf '%s\nreason: %s\nstage: %s (設定 %s, 上限 %s%s)\n' "$1" "$2" "$eff" "$st" "$cap" "${why:+, $why}"; }
  # 保存・送信の前の情報の流れ: 送り先を見られる範囲が、元の情報を見られる範囲より広ければ止める
  _flow() {
    [[ -n $sv && -n $dv ]] || { _gate deny "情報の流れを確かめられません(--source-vis と --dest-vis が要る)"; return 4; }
    (( $(conv_vis_rank "$dv") <= $(conv_vis_rank "$sv") )) || { _gate deny "元の情報($sv)より広い範囲($dv)へ出すことになります"; return 4; }
  }
  case $act in
    draft) _gate allow "下書きはどの段階でも作れます(送るのは人)";;
    review)
      if ((eff >= 1)); then _gate allow "声の役割の指摘(段階 1 以上)"; else _gate deny "段階 0 では声の役割を使いません"; return 4; fi;;
    save-draft)
      ((eff >= 2)) || { _gate deny "下書き保存は段階 2 以上の種別だけ"; return 4; }
      [[ $(conv_stage_get draft-save unverified) == available ]] || { _gate deny "下書き保存がメッセージの道具にあると確かめていません(draft-save)"; return 4; }
      _flow || return 4
      _gate allow "下書きとして保存するだけ(送らない)";;
    post)
      ((eff >= 3)) || { _gate deny "送るのは人です(段階 3 の種別ではない)"; return 4; }
      [[ -n $cp ]] && conv_stage3_ok "$kind" "$cp" || { _gate deny "stage3 = の一覧に $kind:${cp:-?} がありません。送るのは人です"; return 4; }
      _flow || return 4
      _gate ask "段階 3: この 1 通を人が承認すれば送れます(承認なしには送らない)"; return 20;;
  esac
}
