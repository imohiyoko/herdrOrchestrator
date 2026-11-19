# shellcheck shell=bash
# タスクの登録・状態遷移・報告(add / list / show / claim / update / done / block / report / pending)
# と、チケットの構造(種別・優先度・ラベル・説明・リンク: link / unlink / tree / labels)
# bin/tb から source される。単体では実行しない。

# ---------- チケットの構造(014)の小道具 ----------
# 引数(それぞれカンマ区切り可)→ ラベルの JSON 配列(重複なし・辞書順)。不正なラベルは usage で止める
labels_json() {
  local a p out=()
  for a in "$@"; do
    local IFS=,; read -ra parts <<<"$a"; unset IFS
    for p in "${parts[@]}"; do
      p=${p#"${p%%[![:space:]]*}"}; p=${p%"${p##*[![:space:]]}"}
      [[ -z $p ]] && continue
      valid_label "$p"; out+=("$p")
    done
  done
  jq -nc '$ARGS.positional | unique' --args ${out[@]+"${out[@]}"}
}
# 引数(<種類>=<URL> か URL)→ 外部リンクの JSON 配列 [{kind,url}](url で重複なし、先に書いたものを残す)
refs_json() {
  local a kind url out=()
  for a in "$@"; do
    [[ -n ${a//[[:space:]]/} ]] || continue
    if [[ $a =~ ^([a-z][a-z0-9_-]*)=(.*)$ ]]; then kind=${BASH_REMATCH[1]}; url=${BASH_REMATCH[2]}
    else
      url=$a
      case $url in
        https://*slack.com/*) kind=slack;; https://github.com/*) kind=github;; *) kind=link;;
      esac
    fi
    valid_ref_kind "$kind"; valid_ref_url "$url"
    out+=("$kind" "$url")
  done
  jq -nc '[$ARGS.positional as $a | range(0; $a|length; 2) | {kind: $a[.], url: $a[.+1]}]
          | reduce .[] as $r ([]; if any(.[]; .url == $r.url) then . else . + [$r] end)' \
     --args ${out[@]+"${out[@]}"}
}
# 種別と親の組み合わせ。$1=種別 $2=親の ID(無ければ空) $3=自分の ID(add では空)
# epic は親を持たない / subtask は親が必須で、親は epic でも subtask でもない / subtask は誰の親にもなれない
check_hierarchy() {
  local type=$1 parent=$2 self=${3-} ptype
  if [[ -n $parent ]]; then
    [[ $type != epic ]] || usage "epic は親を持てません"
    ptype=$(sql "SELECT type FROM tasks WHERE id=$(q "$parent");")
    [[ $ptype != subtask ]] || usage "subtask は親にできません: $parent"
    [[ ! ( $type == subtask && $ptype == epic ) ]] || usage "subtask の親は story / task / bug にする(epic の直下には置けない): $parent"
  else
    [[ $type != subtask ]] || usage "subtask には --parent が必要です"
  fi
  if [[ -n $self && $type == subtask ]]; then
    [[ $(sql "SELECT COUNT(*) FROM tasks WHERE parent_id=$(q "$self");") == 0 ]] || usage "子のあるタスクは subtask にできません: $self"
  fi
  if [[ -n $self && $type == epic ]]; then   # subtask は epic の直下に置けない
    [[ $(sql "SELECT COUNT(*) FROM tasks WHERE parent_id=$(q "$self") AND type='subtask';") == 0 ]] || usage "subtask の子があるタスクは epic にできません: $self"
  fi
  return 0
}
# 親をたどって $2(自分)に着くなら真になる SQL の条件。$1=新しい親。UNION で訪問済みを除くので、循環があっても止まる
parent_cycle_sql() {
  printf "EXISTS (WITH RECURSIVE up(id) AS (SELECT %s UNION SELECT t.parent_id FROM tasks t JOIN up ON t.id=up.id WHERE t.parent_id IS NOT NULL) SELECT 1 FROM up WHERE id=%s)" "$(q "$1")" "$(q "$2")"
}
# check_hierarchy と同じ規則を、書き込みの文の条件(SQL)にする。書き込みと同じトランザクションで評価して、並行する変更で規則を破らない
# $1=種別 $2=親(空可) $3=自分(add では空)
hierarchy_ok_sql() {
  local type=$1 parent=$2 self=${3-} c="1"
  if [[ -n $parent ]]; then
    c+=" AND NOT EXISTS (SELECT 1 FROM tasks WHERE id=$(q "$parent") AND (type='subtask' OR ($(q "$type")='subtask' AND type='epic')))"
  fi
  if [[ -n $self ]]; then
    case $type in
      subtask) c+=" AND NOT EXISTS (SELECT 1 FROM tasks WHERE parent_id=$(q "$self"))";;
      epic)    c+=" AND NOT EXISTS (SELECT 1 FROM tasks WHERE parent_id=$(q "$self") AND type='subtask')";;
    esac
    [[ -z $parent ]] || c+=" AND NOT $(parent_cycle_sql "$parent" "$self")"
  fi
  printf '(%s)' "$c"
}
# 優先度の並び(highest が先)。SQL の ORDER BY に使う
PRIORITY_RANK="CASE priority WHEN 'highest' THEN 1 WHEN 'high' THEN 2 WHEN 'medium' THEN 3 WHEN 'low' THEN 4 ELSE 5 END"
# 実行者の名前(agent の pane なら herdr から引いた名前、人の端末なら human)
actor_name() {
  if [[ -n ${HERDR_PANE_ID:-} ]]; then resolve_label || return 3; printf '%s' "$LABEL_NAME"; else printf human; fi
}

# ---------- add ----------
cmd_add() {
  local project="" title="" parent="" owner="" session="" account="" type=task priority=medium desc="" due="" labs=() refs=()
  while [[ $# -gt 0 ]]; do
    case $1 in
      -p|--project) need_arg "$@"; project=$2; shift 2;;
      -t|--title)   need_arg "$@"; title=$2; shift 2;;
      --type)       need_arg "$@"; valid_type "$2"; type=$2; shift 2;;
      --priority)   need_arg "$@"; valid_priority "$2"; priority=$2; shift 2;;
      -l|--label)   need_arg "$@"; labs+=("$2"); shift 2;;
      --desc|--description) need_arg "$@"; desc=$2; shift 2;;
      --ref)        need_arg "$@"; refs+=("$2"); shift 2;;
      --due)        need_arg "$@"; [[ -z $2 ]] || valid_due "$2"; due=$2; shift 2;;
      --parent)     need_arg "$@"; parent=$2; shift 2;;
      --owner)      need_arg "$@"; owner=$2; shift 2;;
      --session)    need_arg "$@"; session=$2; shift 2;;
      --account)    need_arg "$@"; account=$2; shift 2;;
      *) usage "add: 不明な引数 $1";;
    esac
  done
  [[ -n $project ]] || usage "add: -p <project> が必要です"
  valid_project "$project"
  blank "$title" && usage "add: -t <title> が必要です"
  if [[ -n $parent ]]; then valid_id "$parent"; require_task "$parent" >/dev/null; fi
  check_hierarchy "$type" "$parent"
  local lj rj; lj=$(labels_json ${labs[@]+"${labs[@]}"}); rj=$(refs_json ${refs[@]+"${refs[@]}"})
  event_actor_resolve
  local n=$(now) p out; p=$(q "$project")
  # 採番と挿入を 1 文で行い、同時 add でも重複しないようにする。親の種別の規則も同じ文で確かめる(HAVING)
  # ID は DB 全体で一意(採番は全セッションの行を見る。ここだけ herdr_session で絞らない)
  out=$(sql "BEGIN IMMEDIATE; INSERT INTO main.tasks (id, project, title, owner_agent, session_id, account, parent_id, type, priority, labels, description, refs, due, created_at, updated_at, herdr_session)
       SELECT 'T-'||$p||'-'||printf('%03d', COALESCE(MAX(CAST(substr(id, length($p)+4) AS INTEGER)),0)+1),
              $p, $(q "$title"), NULLIF($(q "$owner"),''), NULLIF($(q "$session"),''), NULLIF($(q "$account"),''),
              NULLIF($(q "$parent"),''), $(q "$type"), $(q "$priority"), $(q "$lj"), NULLIF($(q "$desc"),''), $(q "$rj"), NULLIF($(q "$due"),''),
              $(q "$n"), $(q "$n"), $SQS
       FROM main.tasks WHERE project=$p HAVING $(hierarchy_ok_sql "$type" "$parent");
       SELECT CASE WHEN changes()=1 THEN (SELECT id FROM main.tasks WHERE rowid=last_insert_rowid()) ELSE '' END;   -- rowid は表のもの(ビューには無い)
       COMMIT;")
  [[ -n $out ]] || usage "add: 親の種別が規則に合いません(並行して変わりました): $parent"
  printf '%s\n' "$out"
}

# ---------- list ----------
# 既定の出力(タブ区切り: ID 状態 担当 題)は変えない。--long で 種別・優先度・作成・更新・ラベル・期日も出す
# --overdue: 期日が今日より前で done でないもの。--due-within N: 期日が今日〜N 日後で done でないもの(0 は今日だけ)
cmd_list() {
  local project="" status="" json=0 long=0 sort="" where="1=1" order="project, id" l lj td
  while [[ $# -gt 0 ]]; do
    case $1 in
      -p|--project) need_arg "$@"; project=$2; shift 2;;
      -s|--status)  need_arg "$@"; status=$2; shift 2;;
      --type)       need_arg "$@"; valid_type "$2"; where+=" AND type=$(q "$2")"; shift 2;;
      --priority)   need_arg "$@"; valid_priority "$2"; where+=" AND priority=$(q "$2")"; shift 2;;
      -l|--label)   need_arg "$@"   # 複数指定はすべてを持つもの(AND)。カンマ区切りも同じ
                    lj=$(labels_json "$2")
                    while IFS= read -r l; do
                      [[ -n $l ]] && where+=" AND EXISTS (SELECT 1 FROM json_each(tasks.labels) WHERE value=$(q "$l"))"
                    done < <(jq -r '.[]' <<<"$lj"); shift 2;;
      --owner)      need_arg "$@"; where+=" AND owner_agent=$(q "$2")"; shift 2;;
      --parent)     need_arg "$@"; valid_id "$2"; where+=" AND parent_id=$(q "$2")"; shift 2;;
      --overdue)    td=$(today) || exit $?; where+=" AND due IS NOT NULL AND due < $(q "$td") AND status!='done'"; shift;;
      --due-within) need_arg "$@"; [[ $2 =~ ^[0-9]{1,4}$ ]] || usage "list: --due-within は 0〜9999 の日数: $2"
                    td=$(today) || exit $?
                    where+=" AND due IS NOT NULL AND due >= $(q "$td") AND due <= date($(q "$td"), '+$((10#$2)) days') AND status!='done'"; shift 2;;
      --has-due)    where+=" AND due IS NOT NULL"; shift;;
      --sort)       need_arg "$@"; sort=$2; shift 2;;
      --long)       long=1; shift;;
      --json)       json=1; shift;;
      *) usage "list: 不明な引数 $1";;
    esac
  done
  [[ -z $project ]] || where+=" AND project=$(q "$project")"
  if [[ -n $status ]]; then valid_status "$status"; where+=" AND status=$(q "$status")"; fi
  case $sort in
    "") ;;
    priority) order="$PRIORITY_RANK, updated_at DESC, id";;
    updated)  order="updated_at DESC, id";;
    created)  order="created_at DESC, id";;
    id)       order="id";;
    due)      order="due IS NULL, due, $PRIORITY_RANK, id";;   # 期日の近い順(期日なしは最後)
    *) usage "list: --sort は priority / updated / created / id / due のどれか: $sort";;
  esac
  if ((json)); then
    sql -json "SELECT * FROM tasks WHERE ($where) ORDER BY $order;" | jq -c '(. // [])[] | .deliverables |= fromjson | .labels |= fromjson | .refs |= fromjson'
  elif ((long)); then
    sql -separator $'\t' "SELECT id, status, type, priority, COALESCE(owner_agent,'-'), created_at, updated_at,
        COALESCE((SELECT group_concat(value, ',') FROM json_each(tasks.labels)), '-'), replace(title, char(10), ' '), COALESCE(due, '-')
      FROM tasks WHERE ($where) ORDER BY $order;"
  else
    sql -separator $'\t' "SELECT id, status, COALESCE(owner_agent,'-'), replace(title, char(10), ' ') FROM tasks WHERE ($where) ORDER BY $order;"
  fi
}

# ---------- show ----------
# リンクを向きつきで: blocks / blocked_by / relates / duplicates / duplicated_by。相手の題と状態も付ける
task_links_json() {
  local i; i=$(q "$1")
  sql -json "SELECT CASE WHEN l.from_id=$i THEN l.link_type
                         WHEN l.link_type='blocks' THEN 'blocked_by'
                         WHEN l.link_type='duplicates' THEN 'duplicated_by' ELSE 'relates' END AS rel,
                    t.id, t.title, t.status
             FROM task_links l JOIN tasks t ON t.id = CASE WHEN l.from_id=$i THEN l.to_id ELSE l.from_id END
             WHERE l.from_id=$i OR l.to_id=$i ORDER BY l.id;" |
    jq -sc '(.[0] // []) | group_by(.rel) | map({key: .[0].rel, value: map(del(.rel))}) | from_entries'
}

cmd_show() {
  [[ $# -eq 1 ]] || usage "show <id>"
  valid_id "$1"
  local row sid act=""
  row=$(require_task "$1"); sid=$(jq -r '.session_id // empty' <<<"$row")
  if [[ -n $sid ]]; then   # 担当 agent の最終活動(tasks.session_id と agent_activity.session_id を結ぶ)
    act=$(sql -json "$(act_select 10) WHERE session_id=$(q "$sid");" | jq -c '.[0] // null')
  fi
  local links; links=$(task_links_json "$1")
  jq --argjson a "${act:-null}" --argjson l "$links" '.deliverables |= fromjson | .labels |= fromjson | .refs |= fromjson | .activity = $a | .links = $l' <<<"$row"
}

# ---------- claim ----------
cmd_claim() {
  [[ $# -ge 1 ]] || usage "claim <id> --owner <name> [--session <sid>] [--account <acct>]"
  local id=$1 owner="" session="" account=""; shift
  valid_id "$id"
  while [[ $# -gt 0 ]]; do
    case $1 in
      --owner)   need_arg "$@"; owner=$2; shift 2;;
      --session) need_arg "$@"; session=$2; shift 2;;
      --account) need_arg "$@"; account=$2; shift 2;;
      *) usage "claim: 不明な引数 $1";;
    esac
  done
  blank "$owner" && usage "claim: --owner が必要です"
  require_task "$id" >/dev/null
  local blockers
  blockers=$(sql "SELECT group_concat(t.id||'('||t.status||')', ', ') FROM task_links l JOIN tasks t ON t.id=l.from_id
                  WHERE l.to_id=$(q "$id") AND l.link_type='blocks' AND t.status!='done';")
  [[ -z $blockers ]] || die "claim できません(先に終わらせるタスクがあります: $blockers): $id"
  local changed; event_actor_resolve
  changed=$(sql "UPDATE main.tasks SET status='in_progress', owner_agent=$(q "$owner"),
      session_id=COALESCE(NULLIF($(q "$session"),''), session_id),
      account=COALESCE(NULLIF($(q "$account"),''), account), updated_at=$(q "$(now)")
    WHERE herdr_session=$SQS AND id=$(q "$id") AND status='todo'; SELECT changes();")
  [[ $changed == 1 ]] || die "claim できません(status が todo ではない): $id"
  echo "$id claimed by $owner"
}

# ---------- update ----------
cmd_update() {
  [[ $# -ge 1 ]] || usage "update <id> [--status S] [--title T] [--owner N] [--session S] [--account A] [--parent ID] [--type T] [--priority P] [--desc D] [--due YYYY-MM-DD|''] [--label L ...] [--add-label L] [--remove-label L]"
  local id=$1; shift
  valid_id "$id"
  local sets=() row cur ntype="" nparent="" setlab=() addlab=() rmlab=() relabel=0 addref=() rmref=()
  while [[ $# -gt 0 ]]; do
    case $1 in
      --status)  need_arg "$@"; valid_status "$2"
                 [[ $2 != done ]] || usage "update: done にするには tb done を使う(deliverables と要約が必須)"
                 sets+=("status=$(q "$2")"); shift 2;;
      --title)   need_arg "$@"; blank "$2" && usage "update: title が空です"; sets+=("title=$(q "$2")"); shift 2;;
      --owner)   need_arg "$@"; sets+=("owner_agent=NULLIF($(q "$2"),'')"); shift 2;;
      --session) need_arg "$@"; sets+=("session_id=NULLIF($(q "$2"),'')"); shift 2;;
      --account) need_arg "$@"; sets+=("account=NULLIF($(q "$2"),'')"); shift 2;;
      --parent)  need_arg "$@"; valid_id "$2"; require_task "$2" >/dev/null
                 [[ $2 != "$id" ]] || usage "update: 自分自身を parent にできません"
                 nparent=$2; sets+=("parent_id=$(q "$2")"); shift 2;;
      --type)     need_arg "$@"; valid_type "$2"; ntype=$2; sets+=("type=$(q "$2")"); shift 2;;
      --priority) need_arg "$@"; valid_priority "$2"; sets+=("priority=$(q "$2")"); shift 2;;
      --desc|--description) need_arg "$@"; sets+=("description=NULLIF($(q "$2"),'')"); shift 2;;
      --due)      need_arg "$@"; [[ -z $2 ]] || valid_due "$2"; sets+=("due=NULLIF($(q "$2"),'')"); shift 2;;   # '' で外す
      -l|--label)       need_arg "$@"; relabel=1; setlab+=("$2"); shift 2;;   # 置き換え(--label '' で全部外す)
      --add-label)      need_arg "$@"; addlab+=("$2"); shift 2;;
      --remove-label)   need_arg "$@"; rmlab+=("$2"); shift 2;;
      --add-ref)        need_arg "$@"; addref+=("$2"); shift 2;;      # <種類>=<URL> か URL
      --remove-ref)     need_arg "$@"; valid_ref_url "$2"; rmref+=("$2"); shift 2;;   # URL で外す
      *) usage "update: 不明な引数 $1";;
    esac
  done
  [[ ${#sets[@]} -gt 0 || $relabel == 1 || ${#addlab[@]} -gt 0 || ${#rmlab[@]} -gt 0 || ${#addref[@]} -gt 0 || ${#rmref[@]} -gt 0 ]] || usage "update: 変更する項目がありません"
  row=$(require_task "$id"); cur=$(jq -r .status <<<"$row")
  [[ $cur != done ]] || die "done のタスクは update できません: $id"
  if [[ -n $ntype || -n $nparent ]]; then
    check_hierarchy "${ntype:-$(jq -r .type <<<"$row")}" "${nparent:-$(jq -r '.parent_id // empty' <<<"$row")}" "$id"
  fi
  if [[ $relabel == 1 || ${#addlab[@]} -gt 0 || ${#rmlab[@]} -gt 0 ]]; then
    # 付け外しは書く時点の labels に当てる(並行する --add-label を互いに消さない)
    local base="labels" add rm
    if ((relabel)); then base=$(q "$(labels_json "${setlab[@]}")"); fi
    add=$(labels_json ${addlab[@]+"${addlab[@]}"}); rm=$(labels_json ${rmlab[@]+"${rmlab[@]}"})
    sets+=("labels=(SELECT json_group_array(value) FROM (SELECT value FROM (SELECT value FROM json_each($base) UNION SELECT value FROM json_each($(q "$add")))
              WHERE value NOT IN (SELECT value FROM json_each($(q "$rm"))) ORDER BY value))")
  fi
  if [[ ${#addref[@]} -gt 0 || ${#rmref[@]} -gt 0 ]]; then
    # 外部リンクも書く時点の refs に当てる。同じ URL を足すと種類を置き換える
    local ra rr; ra=$(refs_json ${addref[@]+"${addref[@]}"}); rr=$(jq -nc '$ARGS.positional' --args ${rmref[@]+"${rmref[@]}"})
    sets+=("refs=(SELECT json_group_array(json(v)) FROM (
              SELECT value AS v FROM json_each(refs) WHERE json_extract(value,'\$.url') NOT IN (SELECT value FROM json_each($(q "$rr")))
                AND json_extract(value,'\$.url') NOT IN (SELECT json_extract(value,'\$.url') FROM json_each($(q "$ra")))
              UNION ALL SELECT value FROM json_each($(q "$ra"))))")
  fi
  # 種別・親を変えるときは、規則(循環を含む)の検査と書き込みを 1 つの書き込みトランザクションで行う(並行する変更で規則を破らない)
  local guard="" n ftype fparent
  if [[ -n $ntype || -n $nparent ]]; then
    ftype=${ntype:-$(jq -r .type <<<"$row")}; fparent=${nparent:-$(jq -r '.parent_id // empty' <<<"$row")}
    # 指定しなかった種別・親は読んだときの値で検査したので、書く時点でも変わっていないことを条件にする
    guard=" AND $(hierarchy_ok_sql "$ftype" "$fparent" "$id") AND type=$(q "$(jq -r .type <<<"$row")")
            AND parent_id IS NULLIF($(q "$(jq -r '.parent_id // empty' <<<"$row")"),'')"
  fi
  event_actor_resolve
  local IFS=,
  n=$(sql "BEGIN IMMEDIATE; UPDATE main.tasks SET ${sets[*]}, updated_at=$(q "$(now)") WHERE herdr_session=$SQS AND id=$(q "$id")$guard;
           SELECT changes(); COMMIT;")
  unset IFS
  if [[ $n != 1 ]]; then
    [[ -z $fparent || -z $(sql "SELECT 1 WHERE $(parent_cycle_sql "$fparent" "$id");") ]] || usage "親子が循環します: $id → $fparent"
    usage "update: 種別・親が規則に合いません(並行して変わりました): $id"
  fi
  echo "$id updated"
}

# ---------- done ----------
cmd_done() {
  [[ $# -ge 1 ]] || usage "done <id> -d <成果物パス> [-d ...] -m <要約>"
  local id=$1 summary="" dels=(); shift
  valid_id "$id"
  while [[ $# -gt 0 ]]; do
    case $1 in
      -d|--deliverable) need_arg "$@"; blank "$2" || dels+=("$2"); shift 2;;
      -m|--summary)     need_arg "$@"; summary=$2; shift 2;;
      *) usage "done: 不明な引数 $1";;
    esac
  done
  [[ ${#dels[@]} -gt 0 ]] || usage "done: 成果物(-d)が必須です"
  blank "$summary" && usage "done: 要約(-m)が必須です"
  local row cur dj
  row=$(require_task "$id"); cur=$(jq -r .status <<<"$row")
  [[ $cur != done ]] || die "すでに done です: $id"
  dj=$(jq -nc '$ARGS.positional' --args "${dels[@]}")
  event_actor_resolve
  sql "UPDATE main.tasks SET status='done', deliverables=$(q "$dj"), summary=$(q "$summary"),
         reported_at=NULL, updated_at=$(q "$(now)") WHERE herdr_session=$SQS AND id=$(q "$id");"
  notify_hook notify_hook_task_done "$id"
  echo "$id done (未報告。tb report $id を実行)"
}

# ---------- block ----------
cmd_block() {
  [[ $# -ge 1 ]] || usage "block <id> [-m <理由>]"
  local id=$1 reason=""; shift
  valid_id "$id"
  while [[ $# -gt 0 ]]; do
    case $1 in
      -m|--message) need_arg "$@"; reason=$2; shift 2;;
      *) usage "block: 不明な引数 $1";;
    esac
  done
  local row cur; row=$(require_task "$id"); cur=$(jq -r .status <<<"$row")
  [[ $cur != done ]] || die "done のタスクは block できません: $id"
  event_actor_resolve
  sql "UPDATE main.tasks SET status='blocked', note=NULLIF($(q "$reason"),''), updated_at=$(q "$(now)") WHERE herdr_session=$SQS AND id=$(q "$id");"
  echo "$id blocked"
}

# ---------- report ----------
cmd_report() {
  [[ $# -eq 1 ]] || usage "report <id>"
  if [[ $1 =~ ^C-[0-9]+$ ]]; then deliver_comment "${1#C-}"; return $?; fi   # 未配送コメントの再送
  local id=$1; valid_id "$id"
  local row status project summary paths reported target msg
  row=$(require_task "$id")
  status=$(jq -r .status <<<"$row"); project=$(jq -r .project <<<"$row")
  summary=$(jq -r '.summary // ""' <<<"$row"); reported=$(jq -r '.reported_at // ""' <<<"$row")
  paths=$(jq -r '.deliverables | fromjson | join(", ")' <<<"$row")
  [[ $status == done ]] || die "done ではないタスクは report できません($status): $id"
  if [[ -n $reported ]]; then echo "$id は報告済みです($reported)"; return 0; fi

  target=$(target_for "$project")
  # prompt は 1 行にする(改行が入ると途中で submit されうる)
  summary=$(printf '%s' "$summary" | tr '\r\n\t' '   ' | tr -s ' ')
  summary=${summary%"${summary##*[![:space:]]}"}
  msg="[$id done] $summary / $paths"
  herdr_send "$target" "$msg" "$id" || return $?
  sql "UPDATE main.tasks SET reported_at=$(q "$(now)"), updated_at=$(q "$(now)") WHERE herdr_session=$SQS AND id=$(q "$id");"
  echo "$id reported to $target"
}

# ---------- pending ----------
cmd_pending() {
  local mode=tsv
  while [[ $# -gt 0 ]]; do
    case $1 in
      --json) mode=json; shift;;
      --ids)  mode=ids; shift;;
      *) usage "pending: 不明な引数 $1";;
    esac
  done
  local where="status='done' AND (reported_at IS NULL OR reported_at='')"
  case $mode in
    ids)  sql "SELECT id FROM tasks WHERE ($where) ORDER BY updated_at, id;"
          sql "SELECT 'C-'||id FROM comments WHERE delivered_at IS NULL ORDER BY id;";;
    json) sql -json "SELECT * FROM tasks WHERE ($where) ORDER BY updated_at, id;" | jq -c '(. // [])[] | .deliverables |= fromjson'
          sql -json "SELECT * FROM comments WHERE delivered_at IS NULL ORDER BY id;" | jq -c '(. // [])[] | .deliverables |= fromjson | .id = "C-\(.id)"';;
    tsv)  local id project summary
          while IFS=$'\t' read -r id project summary; do
            printf '%s\t%s\t%s\t%s\n' "$id" "$project" "$(target_for "$project")" "$summary"
          done < <(sql -separator $'\t' "SELECT id, project, replace(replace(COALESCE(summary,''), char(10), ' '), char(9), ' ') FROM tasks WHERE ($where) ORDER BY updated_at, id;")
          # 未配送のコメント: ID は C-<n>(tb report C-<n> で再送)
          while IFS=$'\t' read -r id project summary; do
            printf '%s\t%s\t%s\t%s\n' "$id" "$project" "$(target_for "$project")" "$summary"
          done < <(sql -separator $'\t' "SELECT 'C-'||c.id, COALESCE(t.project,''), '['||c.kind||'] '||substr(replace(replace(c.text, char(10), ' '), char(9), ' '),1,80)
            FROM comments c LEFT JOIN tasks t ON t.id=c.task_id WHERE c.delivered_at IS NULL ORDER BY c.id;");;
  esac
}

# ---------- link / unlink ----------
# tb link <from> blocks|relates|duplicates <to>。blocks は「to は from が done になるまで claim できない」
link_args() {
  [[ $# -eq 3 ]] || usage "$LINK_CMD <from-id> blocks|relates|duplicates <to-id>"
  valid_id "$1"; valid_id "$3"
  [[ " $LINK_TYPES " == *" $2 "* ]] || usage "リンクの種類は $LINK_TYPES のどれか: $2"
  [[ $1 != "$3" ]] || usage "自分自身とはリンクできません: $1"
  require_task "$1" >/dev/null; require_task "$3" >/dev/null
}
cmd_link() {
  LINK_CMD=link link_args "$@"
  local from=$1 type=$2 to=$3 by cyc=0 out
  by=$(actor_name) || return $?
  event_actor_resolve
  # blocks は、to から blocks をたどって from に着くなら循環。検査と挿入は 1 つの書き込みトランザクションで行う
  [[ $type != blocks ]] || cyc="EXISTS (WITH RECURSIVE r(id) AS (SELECT $(q "$to") UNION SELECT l.to_id FROM task_links l JOIN r ON l.from_id=r.id WHERE l.link_type='blocks')
        SELECT 1 FROM r WHERE id=$(q "$from"))"
  out=$(sql "BEGIN IMMEDIATE; SELECT $cyc;
      INSERT OR IGNORE INTO main.task_links (from_id, link_type, to_id, created_by, created_at, herdr_session)
        SELECT $(q "$from"), $(q "$type"), $(q "$to"), $(q "$by"), $(q "$(now)"), $SQS WHERE NOT $cyc;
      SELECT changes(); COMMIT;")
  [[ ${out%%$'\n'*} == 0 ]] || usage "blocks が循環します: $from → $to"
  [[ ${out##*$'\n'} == 1 ]] && echo "$from $type $to" || echo "$from $type $to(既にあります)"
}
cmd_unlink() {
  LINK_CMD=unlink link_args "$@"
  event_actor_resolve
  local n; n=$(sql "DELETE FROM main.task_links WHERE herdr_session=$SQS AND from_id=$(q "$1") AND link_type=$(q "$2") AND to_id=$(q "$3"); SELECT changes();")
  [[ $n == 1 ]] || die "そのリンクはありません: $1 $2 $3"
  echo "unlinked $1 $2 $3"
}

# ---------- tree ----------
# tb tree <id>: そのタスクと子孫を字下げで。tb tree -p <project>: その project の親のないタスクから
cmd_tree() {
  local root="" project=""
  while [[ $# -gt 0 ]]; do
    case $1 in
      -p|--project) need_arg "$@"; project=$2; shift 2;;
      -*) usage "tree: 不明な引数 $1";;
      *) [[ -z $root ]] || usage "tree <id> | tree -p <project>"; valid_id "$1"; root=$1; shift;;
    esac
  done
  [[ -n $root || -n $project ]] || usage "tree <id> | tree -p <project>"
  local seed
  if [[ -n $root ]]; then require_task "$root" >/dev/null; seed="id=$(q "$root")"
  else seed="parent_id IS NULL AND project=$(q "$project")"; fi
  sql -separator $'\t' "WITH RECURSIVE t(id, d, path) AS (
      SELECT id, 0, id FROM tasks WHERE $seed
      UNION ALL SELECT c.id, t.d+1, t.path||'/'||c.id FROM tasks c JOIN t ON c.parent_id=t.id WHERE t.d < 20)
    SELECT substr('                                        ', 1, t.d*2)||x.id, x.type, x.priority, x.status,
           COALESCE(x.owner_agent,'-'), replace(x.title, char(10), ' ')
    FROM t JOIN tasks x ON x.id=t.id ORDER BY t.path;"
}

# ---------- labels ----------
# 使われているラベルと件数(done を除いた件数 / 全部)
cmd_labels() {
  local where="1=1" json=0
  while [[ $# -gt 0 ]]; do
    case $1 in
      -p|--project) need_arg "$@"; where+=" AND t.project=$(q "$2")"; shift 2;;
      --json) json=1; shift;;
      *) usage "labels: 不明な引数 $1";;
    esac
  done
  local qs="SELECT j.value AS label, SUM(t.status!='done') AS open, COUNT(*) AS total
            FROM tasks t, json_each(t.labels) j WHERE ($where) GROUP BY j.value ORDER BY j.value;"
  if ((json)); then sql -json "$qs" | jq -c '(. // [])[]'; else sql -separator $'\t' "$qs"; fi
}

# ---------- history / events(016 の task_events。ADR 0003 の段 1) ----------
# tb history <id>: そのタスクの履歴を古い順に(E-番号 時刻 種類 前 後 変えた者 元)
EVENT_COLS="'E-'||id AS id, task_id, kind, old, new, actor, ref, created_at"
events_out() {  # $1=json(0/1) $2=WHERE $3=ORDER/LIMIT
  # task_events には一時ビューが無い(lib/db.sh)ので、このセッションの行に明示して絞る
  local w="herdr_session=$SQS AND ($2)"
  if (($1)); then sql -json "SELECT $EVENT_COLS FROM main.task_events WHERE $w $3;" | jq -c '(. // [])[]'
  else sql -separator $'\t' "SELECT 'E-'||id, created_at, task_id, kind, COALESCE(replace(old, char(10), ' '),'-'), COALESCE(replace(new, char(10), ' '),'-'), actor, COALESCE(ref,'-')
         FROM main.task_events WHERE $w $3;"; fi
}
cmd_history() {
  local id="" json=0
  while [[ $# -gt 0 ]]; do
    case $1 in
      --json) json=1; shift;;
      -*) usage "history: 不明な引数 $1";;
      *) [[ -z $id ]] || usage "history <id> [--json]"; valid_id "$1"; id=$1; shift;;
    esac
  done
  [[ -n $id ]] || usage "history <id> [--json]"
  require_task "$id" >/dev/null
  events_out "$json" "task_id=$(q "$id")" "ORDER BY id"
}
# tb events [--since E-n] [--kind K] [-p project] [--limit N] [--json]: このセッションの履歴を古い順に(既定は最新の 50 件)
cmd_events() {
  local where="1=1" json=0 limit=50 n
  while [[ $# -gt 0 ]]; do
    case $1 in
      --since) need_arg "$@"; [[ $2 =~ ^E-([0-9]{1,18})$ ]] || usage "events: --since は E-<数字>: $2"; where+=" AND id > ${BASH_REMATCH[1]}"; shift 2;;
      --kind)  need_arg "$@"; [[ $2 =~ ^[a-z_]{1,32}$ ]] || usage "events: --kind が不正です: $2"; where+=" AND kind=$(q "$2")"; shift 2;;
      -p|--project) need_arg "$@"; valid_project "$2"; where+=" AND task_id LIKE $(q "T-$2-%")"; shift 2;;
      --limit) need_arg "$@"; [[ $2 =~ ^[0-9]{1,6}$ ]] && ((10#$2 > 0)) || usage "events: --limit は 1〜999999: $2"; limit=$((10#$2)); shift 2;;
      --json)  json=1; shift;;
      *) usage "events: 不明な引数 $1";;
    esac
  done
  # 最新の N 件を古い順に
  events_out "$json" "id IN (SELECT id FROM main.task_events WHERE herdr_session=$SQS AND $where ORDER BY id DESC LIMIT $limit)" "ORDER BY id"
}
