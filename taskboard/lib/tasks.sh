# shellcheck shell=bash
# タスクの登録・状態遷移・報告(add / list / show / claim / update / done / block / report / pending)
# と、チケットの構造(種別・優先度・ラベル・説明・リンク: link / unlink / tree / labels)とスプリント(sprint-* / sprints / board)
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
# ---------- 用意したラベル(設定 labels) ----------
# 設定 labels があれば、tb add -l / tb update --label / --add-label で付けられるのは、そこに書いたラベルと tb 自身が使うラベルだけ。
# 設定が無ければ今までどおり自由に付けられる。外す(--remove-label)・絞り込む(list / board -l)は制限しない。
# 書式: 1 行 1 つ「<ラベル> [= 説明]」。# 以降はコメント。書くのは人だけ(tb config set / edit labels)
# tb 自身が付けるラベル(アラート・提案・期限の取り込み)は設定に書かなくても付けられる
# deadline の名前は設定 system-labels で変えられる(lib/core.sh の system_label_of)。alert / proposal / rejected / role:* は DB のトリガーが付けるので固定
SYSTEM_LABELS="alert proposal rejected"
system_label() { [[ " $SYSTEM_LABELS " == *" $1 "* || $1 == role:* || $1 == "$(system_label_of deadline)" ]]; }

config_validate_labels() {  # $1=本文
  local line k n=0 bad="" seen=" "
  while IFS= read -r line || [[ -n $line ]]; do
    n=$((n + 1))
    line=$(trim "${line%%#*}"); [[ -n $line ]] || continue
    k=$(trim "${line%%=*}")
    if [[ -z $k || ${#k} -gt 40 || $k =~ [[:space:],\"\'] ]]; then
      bad+="${n} 行目: ラベルは 1〜40 文字で、空白・カンマ・引用符を含まない: $k"$'\n'
    elif [[ $seen == *" $k "* ]]; then
      bad+="${n} 行目: ラベルが重複しています: $k"$'\n'
    fi
    seen+="$k "
  done <<<"$1"
  [[ -z $bad ]] && return 0
  printf '%s' "$bad" >&2; return 1
}

# 用意したラベルを「ラベル<TAB>説明」で 1 行ずつ(書いた順)。設定が無ければ 1
defined_labels() {
  config_has labels || return 1
  local line k v
  while IFS= read -r line || [[ -n $line ]]; do
    line=$(trim "${line%%#*}"); [[ -n $line ]] || continue
    k=$(trim "${line%%=*}"); v=""; [[ $line != *=* ]] || v=$(trim "${line#*=}")
    printf '%s\t%s\n' "$k" "$v"
  done <<<"$CFG_BODY"
}

# 付けるラベル(JSON 配列)が用意したものかを確かめる。設定が無ければ何もしない。用意していないものがあれば usage で止める
labels_check_defined() {  # $1=ラベルの JSON 配列
  local defs l bad=()
  [[ $1 != '[]' ]] || return 0   # 付けるものが無ければ設定を引かない
  defs=$(defined_labels) || return 0
  while IFS= read -r l; do
    [[ -n $l ]] || continue
    system_label "$l" && continue
    grep -qxF -- "$l" < <(cut -f1 <<<"$defs") || bad+=("$l")
  done < <(jq -r '.[]' <<<"$1")
  [[ ${#bad[@]} -eq 0 ]] && return 0
  usage "用意されていないラベルです: ${bad[*]}(使えるのは: $(cut -f1 <<<"$defs" | paste -sd' ' -)。一覧は tb labels --defined、足すのは人が tb config edit labels)"
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
# スプリントに入れてよいか: $1=S-<n> $2=project。同じ project で closed でないこと。数字の ID を出す
sprint_for_task() {
  valid_sprint "$1"
  local n=${1#S-} st
  st=$(sql "SELECT state||'|'||project FROM sprints WHERE id=$n;")
  [[ -n $st ]] || die "スプリントが見つかりません: $1"
  [[ ${st#*|} == "$2" ]] || usage "スプリント $1 は project ${st#*|} のものです(タスクは $2)"
  [[ ${st%%|*} != closed ]] || usage "閉じたスプリントには入れられません: $1"
  printf '%s' "$n"
}
# 優先度の並び(highest が先)。SQL の ORDER BY に使う
PRIORITY_RANK="CASE priority WHEN 'highest' THEN 1 WHEN 'high' THEN 2 WHEN 'medium' THEN 3 WHEN 'low' THEN 4 ELSE 5 END"
# 実行者の名前(agent の pane なら herdr から引いた名前、人の端末なら human)
actor_name() {
  if [[ -n ${HERDR_PANE_ID:-} ]]; then resolve_label || return 3; printf '%s' "$LABEL_NAME"; else printf human; fi
}

# アラート・提案のチケット(020。ADR 0003)の状態は、それぞれのコマンドだけが変える。
# 閉じる・承認するのは人だけ(tb alert-close / tb alert-approve)なので、tb claim / update --status / done / block では変えない
special_ticket_guard() {  # $1=タスク ID $2=操作の名前
  local w; w=$(sql "SELECT 'A-'||alert_no FROM alert_details WHERE task_id=$(q "$1") UNION ALL SELECT 'P-'||proposal_no FROM proposal_details WHERE task_id=$(q "$1") LIMIT 1;" 2>/dev/null) || true
  case $w in
    A-*) die "$1 はアラート $w のチケットです。状態は tb alert-approve / alert-result / alert-close $w で変えます(閉じるのは人だけ。$2 では変えません)";;
    P-*) die "$1 は提案 $w のチケットです。状態は tb proposal-resolve $w で変えます($2 では変えません)";;
  esac
}

# ---------- add ----------
cmd_add() {
  local project="" title="" parent="" owner="" session="" account="" type=task priority=medium desc="" due="" labs=() refs=() sprint="" points=""
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
      --sprint)     need_arg "$@"; sprint=$2; shift 2;;
      --points)     need_arg "$@"; valid_points "$2"; points=$2; shift 2;;
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
  local sid=""; [[ -z $sprint ]] || sid=$(sprint_for_task "$sprint" "$project")
  local lj rj; lj=$(labels_json ${labs[@]+"${labs[@]}"}); rj=$(refs_json ${refs[@]+"${refs[@]}"})
  labels_check_defined "$lj"
  event_actor_resolve
  local n=$(now) p out; p=$(q "$project")
  # 採番と挿入を 1 文で行い、同時 add でも重複しないようにする。親の種別の規則も同じ文で確かめる(HAVING)
  # ID は DB 全体で一意(採番は全セッションの行を見る。ここだけ herdr_session で絞らない)
  out=$(sql "BEGIN IMMEDIATE; INSERT INTO main.tasks (id, project, title, status, owner_agent, session_id, account, parent_id, type, priority, labels, description, refs, due, sprint_id, points, created_at, updated_at, herdr_session)
       SELECT 'T-'||$p||'-'||printf('%03d', COALESCE(MAX(CAST(substr(id, length($p)+4) AS INTEGER)),0)+1),
              $p, $(q "$title"), $(q "$(st_main todo)"), NULLIF($(q "$owner"),''), NULLIF($(q "$session"),''), NULLIF($(q "$account"),''),
              NULLIF($(q "$parent"),''), $(q "$type"), $(q "$priority"), $(q "$lj"), NULLIF($(q "$desc"),''), $(q "$rj"), NULLIF($(q "$due"),''),
              CAST(NULLIF($(q "$sid"),'') AS INTEGER), CAST(NULLIF($(q "$points"),'') AS INTEGER),
              $(q "$n"), $(q "$n"), $SQS
       FROM main.tasks WHERE project=$p HAVING $(hierarchy_ok_sql "$type" "$parent")${sid:+ AND EXISTS (SELECT 1 FROM sprints WHERE id=$sid AND state!='closed')};
       SELECT CASE WHEN changes()=1 THEN (SELECT id FROM main.tasks WHERE rowid=last_insert_rowid()) ELSE '' END;   -- rowid は表のもの(ビューには無い)
       COMMIT;")
  [[ -n $out ]] || usage "add: 親の種別かスプリントが規則に合いません(並行して変わりました): ${parent:-$sprint}"
  printf '%s\n' "$out"
  notify_rules_hook
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
      --sprint)     need_arg "$@"   # S-<n> か backlog(スプリントに入っていないもの)
                    if [[ $2 == backlog ]]; then where+=" AND sprint_id IS NULL"; else valid_sprint "$2"; where+=" AND sprint_id=${2#S-}"; fi
                    shift 2;;
      --overdue)    td=$(today) || exit $?; where+=" AND due IS NOT NULL AND due < $(q "$td") AND status NOT IN ($ST_CLOSED_SQL)"; shift;;
      --due-within) need_arg "$@"; [[ $2 =~ ^[0-9]{1,4}$ ]] || usage "list: --due-within は 0〜9999 の日数: $2"
                    td=$(today) || exit $?
                    where+=" AND due IS NOT NULL AND due >= $(q "$td") AND due <= date($(q "$td"), '+$((10#$2)) days') AND status NOT IN ($ST_CLOSED_SQL)"; shift 2;;
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
        COALESCE((SELECT group_concat(value, ',') FROM json_each(tasks.labels)), '-'), replace(title, char(10), ' '), COALESCE(due, '-'),
        COALESCE('S-'||sprint_id, '-'), COALESCE(points, '-')
      FROM tasks WHERE ($where) ORDER BY $order;"
  else
    # 既定の列: ID・状態・担当・タイトル・優先度。前の 4 列の位置は変えない(列の位置で読む agent がいる)ので、優先度は最後に足す
    sql -separator $'\t' "SELECT id, status, COALESCE(owner_agent,'-'), replace(replace(title, char(10), ' '), char(9), ' '), priority FROM tasks WHERE ($where) ORDER BY $order;"
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
  special_ticket_guard "$id" "tb claim"
  local blockers
  blockers=$(sql "SELECT group_concat(t.id||'('||t.status||')', ', ') FROM task_links l JOIN tasks t ON t.id=l.from_id
                  WHERE l.to_id=$(q "$id") AND l.link_type='blocks' AND t.status NOT IN ($ST_CLOSED_SQL);")
  [[ -z $blockers ]] || die "claim できません(先に終わらせるタスクがあります: $blockers): $id"
  local changed; event_actor_resolve
  changed=$(sql "UPDATE main.tasks SET status=$(q "$(st_main active)"), owner_agent=$(q "$owner"),
      session_id=COALESCE(NULLIF($(q "$session"),''), session_id),
      account=COALESCE(NULLIF($(q "$account"),''), account), updated_at=$(q "$(now)")
    WHERE herdr_session=$SQS AND id=$(q "$id") AND status IN ($ST_TODO_SQL)
      AND (owner_agent IS NULL OR owner_agent='' OR owner_agent=$(q "$owner")); SELECT changes();")
  if [[ $changed != 1 ]]; then
    # 0 件の理由を分ける: todo の分類の状態のまま別の担当に割り当て済みか、状態が todo の分類ではないか(状態名ではなく分類で見る)
    local cur; cur=$(sql "SELECT COALESCE(owner_agent,'') FROM tasks WHERE herdr_session=$SQS AND id=$(q "$id") AND status IN ($ST_TODO_SQL);")
    if [[ -n $cur ]]; then
      die "claim できません($cur に割り当て済み。付け替えは tb update $id --owner <名前>。次の候補は tb list で探す): $id"
    fi
    die "claim できません(status が $(st_names todo | sed 's/ / \/ /g') ではない): $id"
  fi
  notify_rules_hook
  echo "$id claimed by $owner"
}

# ---------- update ----------
cmd_update() {
  [[ $# -ge 1 ]] || usage "update <id> [--status S] [--title T] [--owner N] [--session S] [--account A] [--parent ID] [--type T] [--priority P] [--desc D] [--due YYYY-MM-DD|''] [--label L ...] [--add-label L] [--remove-label L]"
  local id=$1; shift
  valid_id "$id"
  local sets=() row cur ntype="" nparent="" setlab=() addlab=() rmlab=() relabel=0 addref=() rmref=() nsprint="-"
  while [[ $# -gt 0 ]]; do
    case $1 in
      --status)  need_arg "$@"; valid_status "$2"
                 ! st_is "$2" done || usage "update: $2 にするには tb done を使う(deliverables と要約が必須)"
                 ! st_is "$2" cancelled || usage "update: $2 にするには tb cancel を使う"
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
      --sprint)         need_arg "$@"; nsprint=$2; shift 2;;          # S-<n>('' でバックログへ)
      --points)         need_arg "$@"; [[ -z $2 ]] || valid_points "$2"; sets+=("points=CAST(NULLIF($(q "$2"),'') AS INTEGER)"); shift 2;;
      --remove-ref)     need_arg "$@"; valid_ref_url "$2"; rmref+=("$2"); shift 2;;   # URL で外す
      *) usage "update: 不明な引数 $1";;
    esac
  done
  [[ ${#sets[@]} -gt 0 || $relabel == 1 || ${#addlab[@]} -gt 0 || ${#rmlab[@]} -gt 0 || ${#addref[@]} -gt 0 || ${#rmref[@]} -gt 0 || $nsprint != - ]] || usage "update: 変更する項目がありません"
  row=$(require_task "$id"); cur=$(jq -r .status <<<"$row")
  [[ " $CLOSED_STATUSES " != *" $cur "* ]] || die "$cur のタスクは update できません: $id"
  [[ " ${sets[*]} " != *" status="* ]] || special_ticket_guard "$id" "tb update --status"
  local sguard=""
  if [[ $nsprint != - ]]; then
    if [[ -z $nsprint ]]; then sets+=("sprint_id=NULL")
    else
      local sn; sn=$(sprint_for_task "$nsprint" "$(jq -r .project <<<"$row")")
      sets+=("sprint_id=$sn"); sguard=" AND EXISTS (SELECT 1 FROM sprints WHERE id=$sn AND state!='closed')"   # 書く時点でも閉じていない
    fi
  fi
  if [[ -n $ntype || -n $nparent ]]; then
    check_hierarchy "${ntype:-$(jq -r .type <<<"$row")}" "${nparent:-$(jq -r '.parent_id // empty' <<<"$row")}" "$id"
  fi
  if [[ $relabel == 1 || ${#addlab[@]} -gt 0 || ${#rmlab[@]} -gt 0 ]]; then
    # 付け外しは書く時点の labels に当てる(並行する --add-label を互いに消さない)
    local base="labels" add rm
    if ((relabel)); then local sj; sj=$(labels_json "${setlab[@]}"); labels_check_defined "$sj"; base=$(q "$sj"); fi
    add=$(labels_json ${addlab[@]+"${addlab[@]}"}); rm=$(labels_json ${rmlab[@]+"${rmlab[@]}"})
    labels_check_defined "$add"
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
  n=$(sql "BEGIN IMMEDIATE; UPDATE main.tasks SET ${sets[*]}, updated_at=$(q "$(now)") WHERE herdr_session=$SQS AND id=$(q "$id") AND status NOT IN ($ST_CLOSED_SQL)$guard$sguard;
           SELECT changes(); COMMIT;")
  unset IFS
  if [[ $n != 1 ]]; then
    cur=$(sql "SELECT status FROM tasks WHERE id=$(q "$id");")
    [[ " $CLOSED_STATUSES " != *" $cur "* ]] || die "$cur のタスクは update できません(並行して閉じられました): $id"
    [[ -z $sguard || -n $(sql "SELECT 1 FROM sprints WHERE id=${nsprint#S-} AND state!='closed';") ]] || usage "閉じたスプリントには入れられません: $nsprint"
    [[ -z $fparent || -z $(sql "SELECT 1 WHERE $(parent_cycle_sql "$fparent" "$id");") ]] || usage "親子が循環します: $id → $fparent"
    usage "update: 種別・親が規則に合いません(並行して変わりました): $id"
  fi
  notify_rules_hook
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
  local row cur dj changed
  row=$(require_task "$id"); cur=$(jq -r .status <<<"$row")
  ! st_is "$cur" done || die "すでに $cur です: $id"
  ! st_is "$cur" cancelled || die "$cur のタスクは done にできません: $id"
  special_ticket_guard "$id" "tb done"
  dj=$(jq -nc '$ARGS.positional' --args "${dels[@]}")
  event_actor_resolve
  changed=$(sql "UPDATE main.tasks SET status=$(q "$(st_main done)"), deliverables=$(q "$dj"), summary=$(q "$summary"),
         reported_at=NULL, updated_at=$(q "$(now)") WHERE herdr_session=$SQS AND id=$(q "$id") AND status NOT IN ($ST_CLOSED_SQL); SELECT changes();")
  [[ $changed == 1 ]] || die "done できません(並行して閉じられました): $id"
  notify_hook notify_hook_task_done "$id"
  notify_rules_hook
  echo "$id done (報告は tb digest でまとめて届く。すぐ届けるなら tb report $id)"
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
  local row cur changed; row=$(require_task "$id"); cur=$(jq -r .status <<<"$row")
  [[ " $CLOSED_STATUSES " != *" $cur "* ]] || die "$cur のタスクは block できません: $id"
  special_ticket_guard "$id" "tb block"
  event_actor_resolve
  changed=$(sql "UPDATE main.tasks SET status=$(q "$(st_main blocked)"), note=NULLIF($(q "$reason"),''), updated_at=$(q "$(now)")
    WHERE herdr_session=$SQS AND id=$(q "$id") AND status NOT IN ($ST_CLOSED_SQL); SELECT changes();")
  [[ $changed == 1 ]] || die "block できません(並行して閉じられました): $id"
  notify_rules_hook
  echo "$id $(st_main blocked)"
}

# ---------- cancel ----------
# やらないと決めたタスクを閉じる(cancelled)。done と同じく閉じた状態で、期限・nudge・先行チケット待ちの対象から外れる。
# 理由(-m)は要約(summary)に残す。完了ではないので完了報告(tb digest / tb report)には出さない(reported_at を埋める)
cmd_cancel() {
  [[ $# -ge 1 ]] || usage "cancel <id> [-m <理由>]"
  local id=$1 reason=""; shift
  valid_id "$id"
  while [[ $# -gt 0 ]]; do
    case $1 in
      -m|--message) need_arg "$@"; reason=$2; shift 2;;
      *) usage "cancel: 不明な引数 $1";;
    esac
  done
  local row cur t changed; row=$(require_task "$id"); cur=$(jq -r .status <<<"$row")
  [[ " $CLOSED_STATUSES " != *" $cur "* ]] || die "すでに $cur です: $id"
  special_ticket_guard "$id" "tb cancel"
  event_actor_resolve
  t=$(now)
  changed=$(sql "UPDATE main.tasks SET status=$(q "$(st_main cancelled)"), summary=NULLIF($(q "$reason"),''), reported_at=$(q "$t"), updated_at=$(q "$t")
    WHERE herdr_session=$SQS AND id=$(q "$id") AND status NOT IN ($ST_CLOSED_SQL); SELECT changes();")
  [[ $changed == 1 ]] || die "cancel できません(並行して閉じられました): $id"
  notify_rules_hook
  echo "$id $(st_main cancelled)"
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
  st_is "$status" done || die "done ではないタスクは report できません($status): $id"
  if [[ -n $reported ]]; then echo "$id は報告済みです($reported)"; return 0; fi

  target=$(target_for "$project")
  # 完了の報告も報告コメント(kind done の C-<n>)として、tb comment と同じ経路で届ける(digest でも同じ行がまとめて届く)。
  # 未配送の done コメントがあればそれを送り直す(作り直さない)。tb が作った行は from_pane が '-'(報告者はチケットの担当)
  local cid
  cid=$(sql "SELECT id FROM comments WHERE task_id=$(q "$id") AND kind='done' AND delivered_at IS NULL ORDER BY id LIMIT 1;")
  if [[ -z $cid ]]; then
    cid=$(sql "INSERT INTO main.comments (herdr_session, task_id, kind, text, deliverables, from_name, from_pane, from_session, created_at)
      SELECT $SQS, id, 'done', COALESCE(summary,''), deliverables, COALESCE(owner_agent,'-'), '-', COALESCE(session_id,'-'), $(q "$(now)")
        FROM tasks WHERE id=$(q "$id");
      SELECT last_insert_rowid();")
  fi
  deliver_comment "$cid" >/dev/null || return $?
  echo "$id reported to $target (C-$cid)"
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
  # 未配送の done コメントがあるタスクは C-<n> の行で出す(同じ報告を 2 回送らない)
  local where="status IN ($ST_DONE_SQL) AND (reported_at IS NULL OR reported_at='') AND NOT EXISTS (SELECT 1 FROM comments c WHERE c.task_id=tasks.id AND c.kind='done' AND c.delivered_at IS NULL)"
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
  notify_rules_hook
  [[ ${out##*$'\n'} == 1 ]] && echo "$from $type $to" || echo "$from $type $to(既にあります)"
}
cmd_unlink() {
  LINK_CMD=unlink link_args "$@"
  event_actor_resolve
  local n; n=$(sql "DELETE FROM main.task_links WHERE herdr_session=$SQS AND from_id=$(q "$1") AND link_type=$(q "$2") AND to_id=$(q "$3"); SELECT changes();")
  [[ $n == 1 ]] || die "そのリンクはありません: $1 $2 $3"
  notify_rules_hook
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
# --defined: 用意したラベル(設定 labels)を書いた順に「ラベル 未完了 全部 説明」で(使われていないものは 0)
cmd_labels() {
  local where="1=1" json=0 defined=0
  while [[ $# -gt 0 ]]; do
    case $1 in
      -p|--project) need_arg "$@"; where+=" AND t.project=$(q "$2")"; shift 2;;
      --defined) defined=1; shift;;
      --json) json=1; shift;;
      *) usage "labels: 不明な引数 $1";;
    esac
  done
  if ((defined)); then
    local defs dj
    defs=$(defined_labels) || die "用意したラベルはありません(設定 labels が無いので、ラベルは自由に付けられます。用意するのは人: tb config edit labels)"
    dj=$(jq -Rnc '[inputs | select(length > 0) | (capture("^(?<label>[^\t]*)\t(?<description>.*)$"; "s") // {label: ., description: ""})]' <<<"$defs")
    local qs="WITH d AS (SELECT key AS ord, json_extract(value,'\$.label') AS label, json_extract(value,'\$.description') AS description FROM json_each($(q "$dj")))
              SELECT d.label, COALESCE(SUM(t.id IS NOT NULL AND t.status NOT IN ($ST_CLOSED_SQL)), 0) AS open, COUNT(t.id) AS total, d.description
              FROM d LEFT JOIN (SELECT t.id, t.status, j.value AS lv FROM tasks t, json_each(t.labels) j WHERE ($where)) t ON t.lv = d.label
              GROUP BY d.ord ORDER BY d.ord;"
    if ((json)); then sql -json "$qs" | jq -c '(. // [])[]'; else sql -separator $'\t' "$qs"; fi
    return
  fi
  local qs="SELECT j.value AS label, SUM(t.status NOT IN ($ST_CLOSED_SQL)) AS open, COUNT(*) AS total
            FROM tasks t, json_each(t.labels) j WHERE ($where) GROUP BY j.value ORDER BY j.value;"
  if ((json)); then sql -json "$qs" | jq -c '(. // [])[]'; else sql -separator $'\t' "$qs"; fi
}

# ---------- スプリント ----------
# tb sprint-add -p <project> -n <名前> [--start YYYY-MM-DD] [--end YYYY-MM-DD] [--goal G] → S-<n>(planned)
cmd_sprint-add() {
  local project="" name="" start="" end="" goal=""
  while [[ $# -gt 0 ]]; do
    case $1 in
      -p|--project) need_arg "$@"; project=$2; shift 2;;
      -n|--name)    need_arg "$@"; name=$2; shift 2;;
      --start)      need_arg "$@"; valid_due "$2"; start=$2; shift 2;;
      --end)        need_arg "$@"; valid_due "$2"; end=$2; shift 2;;
      --goal)       need_arg "$@"; goal=$2; shift 2;;
      *) usage "sprint-add: 不明な引数 $1";;
    esac
  done
  [[ -n $project ]] || usage "sprint-add: -p <project> が必要です"
  valid_project "$project"
  blank "$name" && usage "sprint-add: -n <名前> が必要です"
  [[ -z $start || -z $end || ! $end < $start ]] || usage "sprint-add: --end は --start より後にする"
  printf 'S-%s\n' "$(sql "INSERT INTO main.sprints (project, name, goal, start_date, end_date, created_at, herdr_session)
    VALUES ($(q "$project"), $(q "$name"), NULLIF($(q "$goal"),''), NULLIF($(q "$start"),''), NULLIF($(q "$end"),''), $(q "$(now)"), $SQS);
    SELECT last_insert_rowid();")"
}
sprint_row() { valid_sprint "$1"; local r; r=$(session_row sprints id "${1#S-}"); [[ -n $r ]] || die "スプリントが見つかりません: $1"; printf '%s' "$r"; }
# tb sprint-start S-<n>: planned → active(同じ project の active は 1 つだけ)
cmd_sprint-start() {
  [[ $# -eq 1 ]] || usage "sprint-start S-<n>"
  local row st n; row=$(sprint_row "$1"); st=$(jq -r .state <<<"$row")
  [[ $st == planned ]] || die "planned のスプリントだけ始められます($st): $1"
  n=$(sql "UPDATE main.sprints SET state='active', started_at=$(q "$(now)")
           WHERE herdr_session=$SQS AND id=${1#S-} AND state='planned'
             AND NOT EXISTS (SELECT 1 FROM sprints WHERE project=$(q "$(jq -r .project <<<"$row")") AND state='active');
           SELECT changes();")
  [[ $n == 1 ]] || die "project $(jq -r .project <<<"$row") には active なスプリントがあります(先に tb sprint-close)"
  echo "$1 active"
}
# tb sprint-close S-<n> [--carry S-<m>]: 閉じる。終わっていないタスクは S-<m>(無ければバックログ)へ移す
cmd_sprint-close() {
  [[ $# -ge 1 ]] || usage "sprint-close S-<n> [--carry S-<m>]"
  local id=$1 carry=""; shift
  while [[ $# -gt 0 ]]; do
    case $1 in
      --carry) need_arg "$@"; carry=$2; shift 2;;
      *) usage "sprint-close: 不明な引数 $1";;
    esac
  done
  local row st project to=NULL out; row=$(sprint_row "$id"); st=$(jq -r .state <<<"$row"); project=$(jq -r .project <<<"$row")
  [[ $st != closed ]] || die "すでに閉じています: $id"
  if [[ -n $carry ]]; then
    [[ $carry != "$id" ]] || usage "sprint-close: 同じスプリントには移せません"
    to=$(sprint_for_task "$carry" "$project")
  fi
  # 閉じる側・移す先が閉じていないことを書く時点で確かめる(並行した close と競っても二重に動かさない)
  local ok="EXISTS (SELECT 1 FROM main.sprints WHERE herdr_session=$SQS AND id=${id#S-} AND state!='closed')"
  [[ $to == NULL ]] || ok+=" AND EXISTS (SELECT 1 FROM main.sprints WHERE herdr_session=$SQS AND id=$to AND state!='closed')"
  out=$(sql "BEGIN IMMEDIATE;
    UPDATE main.tasks SET sprint_id=$to, updated_at=$(q "$(now)")
      WHERE herdr_session=$SQS AND sprint_id=${id#S-} AND status NOT IN ($ST_CLOSED_SQL) AND $ok;
    SELECT changes();
    UPDATE main.sprints SET state='closed', closed_at=$(q "$(now)") WHERE herdr_session=$SQS AND id=${id#S-} AND $ok;
    SELECT changes();
    COMMIT;")
  [[ ${out##*$'\n'} == 1 ]] || die "sprint-close: $id か ${carry:-移す先} が並行して閉じられました"
  out=${out%%$'\n'*}
  echo "$id closed(未完了 $out 件を ${carry:-バックログ} へ)"
}
# tb sprints [-p project] [--state planned|active|closed] [--json]
# 件数・ポイントの分母(tasks / points)に cancelled は入れない(やらないと決めたものでスプリントの進み具合を下げない)
cmd_sprints() {
  local where="1=1" json=0
  while [[ $# -gt 0 ]]; do
    case $1 in
      -p|--project) need_arg "$@"; where+=" AND s.project=$(q "$2")"; shift 2;;
      --state) need_arg "$@"; [[ " planned active closed " == *" $2 "* ]] || usage "state は planned / active / closed のどれか: $2"
               where+=" AND s.state=$(q "$2")"; shift 2;;
      --json) json=1; shift;;
      *) usage "sprints: 不明な引数 $1";;
    esac
  done
  local qs="SELECT 'S-'||s.id AS id, s.state, s.project, s.name, s.start_date, s.end_date, s.goal,
              COALESCE(SUM(t.id IS NOT NULL AND t.status NOT IN ($ST_CANCELLED_SQL)),0) AS tasks, SUM(t.status IN ($ST_DONE_SQL)) AS done,
              COALESCE(SUM(CASE WHEN t.status NOT IN ($ST_CANCELLED_SQL) THEN t.points END),0) AS points, COALESCE(SUM(CASE WHEN t.status IN ($ST_DONE_SQL) THEN t.points END),0) AS done_points
            FROM sprints s LEFT JOIN tasks t ON t.sprint_id=s.id WHERE ($where)
            GROUP BY s.id ORDER BY s.project, CASE s.state WHEN 'active' THEN 1 WHEN 'planned' THEN 2 ELSE 3 END, s.id"
  if ((json)); then sql -json "$qs;" | jq -c '(. // [])[] | .done //= 0'
  else sql -separator $'\t' "SELECT id, state, project, name, COALESCE(start_date,'-')||'..'||COALESCE(end_date,'-'),
         COALESCE(done,0)||'/'||tasks, done_points||'/'||points FROM ($qs);"; fi
}
# tb board [S-<n> | -p <project>] [-l ラベル] [--json]: 状態ごとの列。-p だけならその project の active なスプリント
cmd_board() {
  local sid="" project="" json=0 where="" l lj
  while [[ $# -gt 0 ]]; do
    case $1 in
      -p|--project) need_arg "$@"; project=$2; shift 2;;
      -l|--label) need_arg "$@"; lj=$(labels_json "$2")
                  while IFS= read -r l; do
                    [[ -n $l ]] && where+=" AND EXISTS (SELECT 1 FROM json_each(tasks.labels) WHERE value=$(q "$l"))"
                  done < <(jq -r '.[]' <<<"$lj"); shift 2;;
      --json) json=1; shift;;
      S-*) valid_sprint "$1"; sid=$1; shift;;
      *) usage "board: 不明な引数 $1";;
    esac
  done
  if [[ -z $sid ]]; then
    [[ -n $project ]] || usage "board S-<n> | board -p <project>"
    sid=$(sql "SELECT 'S-'||id FROM sprints WHERE project=$(q "$project") AND state='active';")
    [[ -n $sid ]] || die "project $project に active なスプリントがありません(tb sprints -p $project)"
  fi
  local row; row=$(sprint_row "$sid")
  local qs="SELECT id, status, type, priority, COALESCE(owner_agent,'-') AS owner, points, labels, title FROM tasks
            WHERE sprint_id=${sid#S-}$where ORDER BY $PRIORITY_RANK, id"
  # 列は設定 statuses の状態(分類の順)。設定から外した状態のチケットが残っていれば、その列も後ろに足す(黙って見えなくしない)
  local c cols=$ST_BOARD
  while IFS= read -r c; do [[ -z $c || " $cols " == *" $c "* ]] || cols+=" $c"; done < <(sql "SELECT DISTINCT status FROM ($qs) ORDER BY status;")
  if ((json)); then
    sql -json "$qs;" | jq -sc --argjson s "$row" --arg sid "$sid" --arg cols "$cols" \
      '(.[0] // []) as $t | {sprint: ($s | .id = $sid | del(.herdr_session)),
        columns: ([$cols | split(" ")[] as $c | {key: $c, value: [$t[] | select(.status == $c) | .labels |= fromjson]}] | from_entries)}'
    return
  fi
  jq -r --arg sid "$sid" '"\($sid) \(.name) [\(.state)] \(.start_date // "-")..\(.end_date // "-")" + (if .goal then "\n目標: \(.goal)" else "" end)' <<<"$row"
  for c in $cols; do
    printf '\n== %s ==\n' "$c"
    sql -separator $'\t' "SELECT id, priority, owner, COALESCE(points,'-'), replace(title, char(10), ' ') FROM ($qs) WHERE status=$(q "$c");"
  done
}

# ---------- history / events(018 の task_events。ADR 0003 の段 1) ----------
# tb history <id>: そのタスクの履歴を古い順に(E-番号 時刻 種類 前 後 変えた者 元)
EVENT_COLS="'E-'||id AS id, task_id, kind, old, new, actor, ref, created_at"
events_out() {  # $1=json(0/1) $2=WHERE $3=ORDER/LIMIT
  # ORDER BY は列名より別名(EVENT_COLS の id = 'E-n' の文字列)を先に見るので、並べるときは main.task_events.id と書く
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
  events_out "$json" "task_id=$(q "$id")" "ORDER BY main.task_events.id"
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
  events_out "$json" "id IN (SELECT id FROM main.task_events WHERE herdr_session=$SQS AND $where ORDER BY id DESC LIMIT $limit)" "ORDER BY main.task_events.id"
}

# ---------- due-scan(期限のイベント。ADR 0003 の段 3) ----------
# tb due-scan [-p project] [--within N]: 期日が今日より前(overdue)と、今日〜N 日後(due_soon。既定 1 = 今日と明日)の done でないタスクに、
# 履歴のイベントを 1 日 1 回ずつ残す(変えた者 system、元 day:<今日>)。期日なし・done は出さない。通知の規則(notify-rules)の材料になる。
# 「今日」は tb を動かす端末の地方時の日付(TB_TODAY で固定できる)。tb notify flush も呼ぶ。出力: overdue <件数> due_soon <件数>
cmd_due-scan() {
  local within=1 td out project=""
  while [[ $# -gt 0 ]]; do
    case $1 in
      -p|--project) need_arg "$@"; valid_project "$2"; project=$2; shift 2;;
      --within) need_arg "$@"; [[ $2 =~ ^[0-9]{1,3}$ ]] || usage "due-scan: --within は 0〜999 の日数: $2"; within=$((10#$2)); shift 2;;
      *) usage "due-scan: 不明な引数 $1";;
    esac
  done
  td=$(today) || exit $?
  out=$(due_scan_sql "$td" "$within" "$project")
  notify_rules_hook
  echo "$out"
}
due_scan_sql() {  # $1=今日 $2=日数 [$3=project] → "overdue <n> due_soon <m>"
  local td=$1 within=$2 ts day pw=""; ts=$(q "$(now)"); day=$(q "day:$1")
  [[ -z ${3:-} ]] || pw=" AND t.project=$(q "$3")"
  local seen="NOT EXISTS (SELECT 1 FROM main.task_events e WHERE e.herdr_session=$SQS AND e.task_id=t.id AND e.kind=%s AND e.ref=$day)"
  # shellcheck disable=SC2059
  sql "BEGIN IMMEDIATE;
    INSERT INTO main.task_events (herdr_session, task_id, kind, old, new, actor, ref, created_at)
      SELECT $SQS, t.id, 'overdue', NULL, t.due, 'system', $day, $ts FROM tasks t
      WHERE t.due IS NOT NULL AND t.status NOT IN ($ST_CLOSED_SQL) AND t.due < $(q "$td")$pw AND $(printf "$seen" "'overdue'") ORDER BY t.due, t.id;
    SELECT 'overdue ' || changes();
    INSERT INTO main.task_events (herdr_session, task_id, kind, old, new, actor, ref, created_at)
      SELECT $SQS, t.id, 'due_soon', NULL, t.due, 'system', $day, $ts FROM tasks t
      WHERE t.due IS NOT NULL AND t.status NOT IN ($ST_CLOSED_SQL) AND t.due >= $(q "$td") AND t.due <= date($(q "$td"), '+$within days')
       $pw AND $(printf "$seen" "'due_soon'") ORDER BY t.due, t.id;
    SELECT 'due_soon ' || changes();
    COMMIT;" | paste -sd' ' -
}
