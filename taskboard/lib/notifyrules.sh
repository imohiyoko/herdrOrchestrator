# shellcheck shell=bash
# 通知の規則(設定 notify-rules)。チケットの履歴(task_events)のイベントに規則を当て、当たったものを notify のキューに積む(ADR 0003 の段 2)。
# bin/tb から source される。単体では実行しない。
#
# 規則は 1 行 1 つ: <イベントの種類|*> [条件 ...] -> <届け先 ...>   (# 以降はコメント)
#   条件: type= priority= priority>= label= project= owner= new= old= actor=   すべて満たした行が当たる
#         label は、label_added / label_removed では付け外したラベルそのもの、ほかのイベントでは「今そのラベルを持っている」。priority>= は優先度の順(highest が一番上)。
#         actor は変えた者の名前、または human(人の端末)/ system(定期のジョブ)/ agent(それ以外 = agent の pane)
#   届け先: file stdout slack(notify と同じ)/ webui(Web UI の緊急通知)/ conductor(そのプロジェクトの指揮役への次のまとめ)
# 当たった行の届け先を合わせて、イベント 1 件につき通知 1 件(種別 ticket)。本文は ID・題・優先度・種別・担当・イベントだけ(説明・コメントの本文は載せない)。
# 秘密の検出・伏せ字・頻度の上限・重複の抑えは notify のまま。notify が off のあいだは何も積まない(on にしたとき、作られてから 60 分を過ぎたイベントには当てない)。
# 規則を足す前のイベントには当てない(notify が on のあいだは、規則が無くても読み位置を進めている)。
# 規則が無ければ何もしない。今の決め打ちの通知(質問・ブロック・完了・アラート)はそのまま残る(規則は足すだけ)。
# 書くのは人だけ(tb config set notify-rules)。誰が付けたラベルでも当たる(人の決定 2026-11-19。絞りたい規則だけ actor=human)。

NRULE_SINKS="file stdout slack webui conductor"
NRULE_KEYS="type priority label project owner new old actor"
NRULE_KINDS="created status title type priority owner due parent description label_added label_removed ref_added ref_removed link unlink comment overdue due_soon"
NRULE_VAL_RE='^[^[:space:],"'"'"'<>;|&$`\\]{1,64}$'

# 本文 → 規則の行(タブ区切り: 行番号 種類 条件(空白区切り) 届け先(空白区切り))。誤りは NRULE_ERR に 1 行ずつ。誤りがあれば 1
nrule_parse() {  # $1=本文
  local body=$1 line n=0 lhs rhs kind c sinks s k v op tok conds err=0
  NRULE_ERR=""; NRULE_OUT=""
  while IFS= read -r line || [[ -n $line ]]; do
    n=$((n + 1))
    line=${line%%#*}; line=$(trim "$line")
    [[ -n $line ]] || continue
    if [[ $line != *"->"* ]]; then NRULE_ERR+="$n 行目: -> がありません"$'\n'; err=1; continue; fi
    lhs=$(trim "${line%%->*}"); rhs=$(trim "${line#*->}")
    [[ $rhs != *"->"* ]] || { NRULE_ERR+="$n 行目: -> が 2 つあります"$'\n'; err=1; continue; }
    read -r kind c <<<"$lhs"
    if [[ $kind != '*' && " $NRULE_KINDS " != *" $kind "* ]]; then NRULE_ERR+="$n 行目: 不明なイベントの種類 $kind(* か $NRULE_KINDS)"$'\n'; err=1; continue; fi
    conds=""; local bad=0
    for tok in $c; do
      if [[ $tok =~ ^([a-z]+)(=|\>=)(.*)$ ]]; then k=${BASH_REMATCH[1]}; op=${BASH_REMATCH[2]}; v=${BASH_REMATCH[3]}
      else NRULE_ERR+="$n 行目: 条件は <キー>=<値>: $tok"$'\n'; bad=1; continue; fi
      [[ " $NRULE_KEYS " == *" $k "* ]] || { NRULE_ERR+="$n 行目: 不明な条件 $k($NRULE_KEYS)"$'\n'; bad=1; continue; }
      [[ $op == "=" || $k == priority ]] || { NRULE_ERR+="$n 行目: >= は priority だけ: $tok"$'\n'; bad=1; continue; }
      [[ $v =~ $NRULE_VAL_RE ]] || { NRULE_ERR+="$n 行目: 値が不正(空白・引用符・記号は使えない): $tok"$'\n'; bad=1; continue; }
      case $k in
        type) [[ " $TYPES " == *" $v "* ]] || { NRULE_ERR+="$n 行目: type は $TYPES のどれか: $v"$'\n'; bad=1; continue; };;
        priority) [[ " $PRIORITIES " == *" $v "* ]] || { NRULE_ERR+="$n 行目: priority は $PRIORITIES のどれか: $v"$'\n'; bad=1; continue; };;
        project) [[ $v =~ ^[A-Za-z0-9_]+$ ]] || { NRULE_ERR+="$n 行目: project は英数字と _: $v"$'\n'; bad=1; continue; };;
      esac
      conds+="$k$op$v "
    done
    ((bad)) && { err=1; continue; }
    sinks=""
    for s in $rhs; do
      [[ " $NRULE_SINKS " == *" $s "* ]] || { NRULE_ERR+="$n 行目: 使えない届け先 $s($NRULE_SINKS)"$'\n'; bad=1; continue; }
      [[ " $sinks " == *" $s "* ]] || sinks+="$s "
    done
    ((bad)) && { err=1; continue; }
    [[ -n $sinks ]] || { NRULE_ERR+="$n 行目: 届け先がありません"$'\n'; err=1; continue; }
    NRULE_OUT+="$n"$'\t'"$kind"$'\t'"${conds% }"$'\t'"${sinks% }"$'\n'
  done <<<"$body"
  return $err
}

# tb config set notify-rules の検査(誤りがあれば何も書かない)
config_validate_notify_rules() {  # $1=本文
  nrule_parse "$1" && return 0
  printf 'notify-rules: %s' "$NRULE_ERR" >&2; return 1
}

# 効いている規則(誤りのある行は飛ばす。ファイルを手で書いた場合など)。NRULE_OUT に入れる
nrule_load() {
  local body=""
  if config_lookup notify-rules; then body=$CFG_BODY; fi
  nrule_parse "$body" || true
}

# 規則 1 行の条件 → SQL(e = task_events、t = tasks)
nrule_where() {  # $1=種類 $2=条件
  local w="1=1" tok k op v rank="CASE %s WHEN 'highest' THEN 1 WHEN 'high' THEN 2 WHEN 'medium' THEN 3 WHEN 'low' THEN 4 ELSE 5 END"
  [[ $1 == '*' ]] || w+=" AND e.kind=$(q "$1")"
  for tok in $2; do
    [[ $tok =~ ^([a-z]+)(=|\>=)(.*)$ ]] || continue
    k=${BASH_REMATCH[1]}; op=${BASH_REMATCH[2]}; v=${BASH_REMATCH[3]}
    case $k in
      type) w+=" AND t.type=$(q "$v")";;
      priority) if [[ $op == ">=" ]]; then
                  # shellcheck disable=SC2059
                  w+=" AND $(printf "$rank" t.priority) <= $(printf "$rank" "$(q "$v")")"
                else w+=" AND t.priority=$(q "$v")"; fi;;
      label) case $1 in   # ラベルの付け外しのイベントでは、付け外したラベルそのもの。ほかは今そのラベルを持っているか
               label_added) w+=" AND e.new=$(q "$v")";;
               label_removed) w+=" AND e.old=$(q "$v")";;
               *) w+=" AND EXISTS (SELECT 1 FROM json_each(t.labels) WHERE value=$(q "$v"))";;
             esac;;
      project) w+=" AND t.project=$(q "$v")";;
      owner) w+=" AND t.owner_agent=$(q "$v")";;
      new) w+=" AND e.new=$(q "$v")";;
      old) w+=" AND e.old=$(q "$v")";;
      actor) case $v in
               agent) w+=" AND e.actor NOT IN ('human','system')";;
               *) w+=" AND e.actor=$(q "$v")";;
             esac;;
    esac
  done
  printf '%s' "$w"
}

# イベントの説明(本文に載せる 1 句)
nrule_describe() {  # $1=種類 $2=前 $3=後
  local o=${2:-} n=${3:-}
  case $1 in
    created) echo "作成(${n})";;
    status) echo "状態 ${o} → ${n}";;
    label_added) echo "ラベル +${n}";;
    label_removed) echo "ラベル -${o}";;
    priority) echo "優先度 ${o} → ${n}";;
    type) echo "種別 ${o} → ${n}";;
    owner) echo "担当 ${o:--} → ${n:--}";;
    due) echo "期日 ${o:--} → ${n:--}";;
    comment) echo "報告(${n})";;
    link) echo "リンク ${n}";;
    unlink) echo "リンク解除 ${o}";;
    ref_added) echo "外部リンク追加";;
    ref_removed) echo "外部リンク削除";;
    title) echo "題の変更";;
    description) echo "説明の変更";;
    overdue) echo "期限切れ(期日 ${n})";;
    due_soon) echo "期限間近(期日 ${n})";;
    *) echo "$1";;
  esac
}

# 範囲 (from, to] のイベントに規則を当てる。$3=1 なら積まずに当たりを出す(テスト用)
# 出力(試すとき): E-<n>\t<届け先>\t<当たった行番号>
nrule_apply() {  # $1=from $2=to $3=dry
  local from=$1 to=$2 dry=${3:-0} ln kind conds sinks qs="" rows
  # 規則ごとの当たりを 1 つの問い合わせにまとめ(行: id 行番号 届け先)、イベントごとに届け先を合わせる(bash 3.2 でも動くよう連想配列は使わない)
  while IFS=$'\t' read -r ln kind conds sinks; do
    [[ -n $ln ]] || continue
    qs+="${qs:+ UNION ALL }SELECT e.id, $ln, $(q "$sinks") FROM main.task_events e JOIN tasks t ON t.id=e.task_id
           WHERE e.herdr_session=$SQS AND e.id > $from AND e.id <= $to AND $(nrule_where "$kind" "$conds")"
  done <<<"$NRULE_OUT"
  [[ -n $qs ]] || return 0
  rows=$(sql -separator $'\t' "SELECT * FROM ($qs) ORDER BY 1, 2;" | awk -F'\t' '
    { if ($1 != cur) { if (cur != "") print cur "\t" s "\t" l; cur = $1; s = ""; l = ""; delete seen }
      n = split($3, a, " "); for (i = 1; i <= n; i++) if (!(a[i] in seen)) { seen[a[i]] = 1; s = s (s == "" ? "" : " ") a[i] }
      l = l (l == "" ? "" : ",") $2 }
    END { if (cur != "") print cur "\t" s "\t" l }')
  local id hs hl r tid title ty pr ow kd o n
  while IFS=$'\t' read -r id hs hl; do
    [[ -n $id ]] || continue
    if ((dry)); then printf 'E-%s\t%s\t%s\n' "$id" "$hs" "$hl"; continue; fi
    r=$(sql -json "SELECT e.task_id, t.title, t.type, t.priority, COALESCE(t.owner_agent,'-') AS owner, e.kind, e.old, e.new
                   FROM main.task_events e JOIN tasks t ON t.id=e.task_id WHERE e.herdr_session=$SQS AND e.id=$id;" | jq -c '.[0] // empty')
    [[ -n $r ]] || continue
    tid=$(jq -r .task_id <<<"$r"); title=$(jq -r .title <<<"$r"); ty=$(jq -r .type <<<"$r"); pr=$(jq -r .priority <<<"$r")
    ow=$(jq -r .owner <<<"$r"); kd=$(jq -r .kind <<<"$r"); o=$(jq -r '.old // ""' <<<"$r"); n=$(jq -r '.new // ""' <<<"$r")
    NOTIFY_SINKS=$hs notify_enqueue ticket "E-$id" "" "$tid $title" "$(nrule_describe "$kd" "$o" "$n") / $pr / $ty / 担当 $ow" || true
  done <<<"$rows"
}

# 新しいイベントに規則を当てる。読み位置は notify_seen の rules:last(このセッション)。
# notify が off なら何も読まずに戻る(チケットを書くたびに呼ばれるので軽くしておく)。on なら規則が無くても読み位置は進める
# (規則を足したときに、足す前のイベントに当てない)。読み位置が無ければ今の最後を基準線にするだけ。
# off のあいだに溜まったイベントを on にしたときまとめて出さないよう、作られてから NRULE_WINDOW_MIN 分を過ぎたイベントには当てない。
# 並行する tb とは、読み位置の更新(古い値のときだけ書く)で範囲を取り合う
NRULE_WINDOW_MIN=${TB_NOTIFY_RULES_WINDOW_MIN:-60}
notify_rules_run() {
  notify_preload
  notify_on || return 0
  nrule_load
  local last max took from
  max=$(sql "SELECT COALESCE(MAX(id), 0) FROM main.task_events WHERE herdr_session=$SQS;") || return 1
  last=$(notify_seen_get rules:last)
  [[ $last =~ ^[0-9]+$ ]] || last=""
  ((max > ${last:-0})) || return 0
  if [[ -z $last ]]; then
    took=$(sql "INSERT OR IGNORE INTO main.notify_seen (herdr_session, key, value, updated_at) VALUES ($SQS, 'rules:last', $(q "$max"), $(q "$(now)")); SELECT changes();")
  else
    took=$(sql "UPDATE main.notify_seen SET value=$(q "$max"), updated_at=$(q "$(now)") WHERE herdr_session=$SQS AND key='rules:last' AND value=$(q "$last"); SELECT changes();")
  fi
  [[ $took == 1 ]] || return 0   # 別の tb が先に取った
  [[ -n $last && -n $NRULE_OUT ]] || return 0   # 初めて(基準線を引いただけ)か、規則なし(読み位置だけ進める)
  # 窓より古いイベントは飛ばす(範囲の下端を、窓の中で最初のイベントの手前まで上げる)
  from=$(sql "SELECT COALESCE(MIN(id) - 1, $max) FROM main.task_events WHERE herdr_session=$SQS AND id > ${last:-0} AND id <= $max
              AND julianday(created_at) >= julianday('now') - $((10#$NRULE_WINDOW_MIN)) / 1440.0;")
  nrule_apply "$from" "$max" 0
}
# チケットを書いたコマンドの最後に呼ぶ。失敗しても元のコマンドの結果は変えない
notify_rules_hook() { notify_hook notify_rules_run; }

# tb notify rules [show] / rules test E-<n> / rules run
notify_rules_cmd() {
  local sub=${1:-show}; shift || true
  case $sub in
    show)
      [[ $# -eq 0 ]] || usage "notify rules [show]"
      local body="" src
      if config_lookup notify-rules; then body=$CFG_BODY; fi
      src=$(config_source notify-rules)
      echo "# notify-rules(出どころ: $src)。書くのは人だけ: tb config set notify-rules < 規則のファイル"
      if ! nrule_parse "$body"; then printf '# 誤りのある行(飛ばしています):\n%s' "$(sed 's/^/#   /' <<<"$NRULE_ERR")"; echo; fi
      if [[ -z $NRULE_OUT ]]; then echo "(規則なし。今の決め打ちの通知だけが動きます)"; return 0; fi
      local ln kind conds sinks
      while IFS=$'\t' read -r ln kind conds sinks; do
        [[ -n $ln ]] && printf '%s 行目\t%s %s-> %s\n' "$ln" "$kind" "${conds:+$conds }" "$sinks"
      done <<<"$NRULE_OUT";;
    test)
      [[ $# -eq 1 && $1 =~ ^E-([0-9]{1,18})$ ]] || usage "notify rules test E-<n>"
      local id=${BASH_REMATCH[1]}
      [[ -n $(sql "SELECT 1 FROM main.task_events WHERE herdr_session=$SQS AND id=$id;") ]] || die "イベントが見つかりません: E-$id"
      nrule_load
      local out; out=$(nrule_apply "$((id - 1))" "$id" 1)
      if [[ -z $out ]]; then echo "E-$id: どの規則にも当たりません(送りません)"
      else IFS=$'\t' read -r _ s l <<<"$out"; echo "E-$id: 届け先 $s(当たった行: $l)。試すだけで、何も積みません"; fi;;
    run)
      [[ $# -eq 0 ]] || usage "notify rules run"
      notify_rules_run || true
      echo "notify rules: 読み位置 E-$(notify_seen_get rules:last)";;
    *) usage "notify rules [show | test E-<n> | run]";;
  esac
}
