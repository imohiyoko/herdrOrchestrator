# shellcheck shell=bash
# 人が忘れているかもしれないもののまとめ(tb reminder)。bin/tb から source される。単体では実行しない。
#
# - 読み取り専用。書くのは notify_seen の自分の行(キーが reminders: で始まるもの)だけ
# - 材料: 未対応の質問・ブロック報告 / 承認待ち(送信・agent 操作・代行依頼・アラートの解析役)/ 宛先に届いていない送信 /
#         停滞したタスク(最後の動きは更新と履歴 task_events)/ 期限(tasks.due 列があれば。無くても期限ファイル)/ 未読の保留メッセージ(inbox 表があれば)/
#         変更依頼が付いたままの PR(tb pr-watch がチケットの履歴に残したもの)/
#         許可リストのリポジトリの、レビュー待ちの PR(gh で読むだけ)
# - 本文は出さない。題(短い見出し)と参照 ID だけ。出す前に秘密らしい値を探して伏せる(notify と同じ規則)
# - 出力は stdout と、Web UI が読む行(notify_seen の reminders:last)。agent にも外にも送らない
#
# 重複の抑え: 項目ごとに決まったキー(q:<id> / pr:<repo>#<n> / due:<task> など)と状態の署名を reminders:state に残す。
# 前回と同じ項目は「継続(N 日目)」の 1 行にまとめる。状態や期限の段階が変わった項目、新しい項目、1 日 1 回の全件では、行を出し直す。
# 当てはまらなくなった項目は記録から消す(解消として数だけ出す)。

REMINDERS_STALE_HOURS=${TB_REMINDERS_STALE_HOURS:-24}   # in_progress のまま更新がない時間(Web UI の「停滞」と同じ)
REMINDERS_DUE_DAYS=${TB_REMINDERS_DUE_DAYS:-7}          # 期限がこの日数以内なら「予定」として載せる(明日までは「急ぎ」)
REMINDERS_DEADLINES=${TB_REMINDERS_DEADLINES:-$TB_ROOT/config/deadlines.conf}   # 「deadline = YYYY-MM-DD | 題」の行。人が書く
REMINDERS_NO_CHANGE=3   # --check: 新しく出すものが無い

reminders_today() { today; }   # 期限の判定に使う日(tb list --overdue / --due-within と同じ。手元の地方時の日付、TB_TODAY で固定できる)

# 表・列があるか(他の変更より前の DB でも動くように)
reminders_has_col() { [[ -n $(sql "SELECT 1 FROM pragma_table_info($(q "$1")) WHERE name=$(q "$2");") ]]; }

# 期限の段階と優先度。$1=期限の式(SQL)。overdue / today / tomorrow は 1(急ぎ)、soon は 4(予定)
reminders_due_sql() {
  local d=$1 t; t=$(q "$(reminders_today)")
  printf "CASE WHEN date(%s) < date(%s) THEN 'overdue' WHEN date(%s) = date(%s) THEN 'today'
    WHEN date(%s) = date(%s, '+1 day') THEN 'tomorrow' ELSE 'soon' END" "$d" "$t" "$d" "$t" "$d" "$t"
}

# 1 項目 = JSON 1 行 {p, key, sig, what, who, due, ref, since}
# p: 1 急ぎ(期限切れ・今日・明日) 2 人の番(質問・承認待ち・レビュー) 3 停滞 4 待ち行列・予定
reminders_collect_db() {
  local t; t=$(q "$(reminders_today)")
  local parts=()
  parts+=("SELECT 2 AS p, 'q:'||c.id AS key, c.kind AS sig,
      CASE c.kind WHEN 'question' THEN '質問' ELSE 'ブロック報告' END || ': ' || c.task_id || ' ' || COALESCE(t.title, '') AS what,
      '人' AS who, '' AS due, 'C-'||c.id AS ref, c.created_at AS since
    FROM comments c LEFT JOIN tasks t ON t.id=c.task_id
    WHERE c.kind IN ('question','blocked') AND c.resolved_at IS NULL AND COALESCE(t.status, '') NOT IN ($ST_CLOSED_SQL)")
  parts+=("SELECT 2, 'o:'||id, status, '送信の承認待ち: ' || from_name || ' → ' || target || ' (' || kind || ')', '人', '', 'O-'||id, created_at
    FROM outbox WHERE status='pending'")
  parts+=("SELECT 2, 'l:'||id, status, 'agent 操作の承認待ち: ' || op || ' ' || target || ' (' || requested_by || ')', '人', '', 'L-'||id, created_at
    FROM agent_ops WHERE status='pending'")
  parts+=("SELECT 2, 'p:'||id, status, '代行依頼の判断待ち: ' || task_id || ' (' || effect || ')', '人', '', 'proxy '||id, updated_at
    FROM proxy_requests WHERE status='pending-human'")
  parts+=("SELECT 2, 'a:'||id, status, '解析役の起動待ち: ' || title, '人', '', 'A-'||id, last_seen
    FROM alerts WHERE status='awaiting-approval'")
  # 停滞: in_progress のまま、最後の動き(更新か、履歴のイベント。報告のコメントも履歴に入る)から時間が経ったもの
  local last="updated_at"
  if reminders_has_col task_events task_id; then   # チケットの履歴(018)。一時ビューが無いので herdr_session で絞る
    last="MAX(updated_at, COALESCE((SELECT MAX(e.created_at) FROM main.task_events e WHERE e.herdr_session=$SQS AND e.task_id=tasks.id), ''))"
  fi
  parts+=("SELECT 3, 'stall:'||id, status || ':' || $last, '停滞: ' || id || ' ' || title, COALESCE(owner_agent, '-'), '', id, $last
    FROM tasks WHERE status IN ($ST_ACTIVE_SQL) AND (julianday('now') - julianday($last)) * 24 > $REMINDERS_STALE_HOURS")
  # 変更依頼が付いたままの PR(tb pr-watch がチケットに残した最後の PR の出来事が pr_changes。ADR 0003 の段 4c)。担当の番
  if reminders_has_col task_events task_id; then
    parts+=("SELECT 2, 'prchg:'||e.task_id||':'||e.new, 'E-'||e.id, '変更依頼の PR: ' || e.new || ' (' || e.task_id || ' ' || t.title || ')',
        COALESCE(t.owner_agent, '人'), '', e.task_id, e.created_at
      FROM main.task_events e JOIN tasks t ON t.id=e.task_id
      WHERE e.herdr_session=$SQS AND e.kind='pr_changes' AND t.status NOT IN ($ST_CLOSED_SQL)
        AND e.id = (SELECT MAX(x.id) FROM main.task_events x WHERE x.herdr_session=$SQS AND x.task_id=e.task_id AND x.new=e.new AND x.kind LIKE 'pr\_%' ESCAPE '\')")
  fi
  parts+=("SELECT 4, 'queue:'||id, status, '宛先に届いていない送信: ' || from_name || ' → ' || target || ' (' || kind || ')', target, '', 'O-'||id, created_at
    FROM outbox WHERE status='queued' AND (julianday('now') - julianday(created_at)) * 86400 >= $DIGEST_QUEUED_SEC")
  if reminders_has_col tasks due; then   # 期日の列(schema 015)。done のタスクと期日なしは載せない
    local st; st=$(reminders_due_sql due)
    parts+=("SELECT CASE WHEN $st = 'soon' THEN 4 ELSE 1 END, 'due:'||id, ($st) || ':' || due,
        '期限: ' || id || ' ' || title, COALESCE(owner_agent, '人'), due, id, created_at
      FROM tasks WHERE status NOT IN ($ST_CLOSED_SQL) AND due IS NOT NULL AND due != ''
        AND date(due) <= date($t, '+$REMINDERS_DUE_DAYS days')")
  fi
  if reminders_has_col inbox recipient; then   # 保留した送信の受信箱(別の変更で足す)。未読だけ。本文は出さない
    parts+=("SELECT 4, 'inbox:'||id, 'unread', '未読の保留メッセージ: ' || from_name || ' → ' || recipient || ' (' || kind || ')',
        recipient, '', 'I-'||id, created_at
      FROM main.inbox WHERE herdr_session=$SQS AND read_at IS NULL")
  fi
  local all="" s
  for s in "${parts[@]}"; do all+="${all:+ UNION ALL }$s"; done
  sql -json "SELECT * FROM ($all);" | jq -c '(. // [])[]'
}

# 期限ファイルの行を読む。「deadline = YYYY-MM-DD | 題」。それ以外の行は読み飛ばす。出力: 日付<TAB>題
reminders_deadline_lines() {
  [[ -f $REMINDERS_DEADLINES ]] || return 0
  local line
  while IFS= read -r line || [[ -n $line ]]; do
    line=$(trim "${line%%#*}")
    [[ $line =~ ^deadline[[:space:]]*=[[:space:]]*([0-9]{4}-[0-9]{2}-[0-9]{2})[[:space:]]*\|(.*)$ ]] || continue
    [[ -n $(trim "${BASH_REMATCH[2]}") ]] || continue
    printf '%s\t%s\n' "${BASH_REMATCH[1]}" "$(trim "${BASH_REMATCH[2]}")"
  done <"$REMINDERS_DEADLINES"
}
# 同じ題・同じ期日のチケットがあるか(tb deadlines-import で取り込んだ行は、チケットの期限として載るのでファイルからは出さない)
reminders_deadline_ticket() {  # $1=日付 $2=題 → チケット ID か空
  reminders_has_col tasks due || return 0
  sql "SELECT id FROM tasks WHERE due=$(q "$1") AND title=$(q "$2") ORDER BY id LIMIT 1;"
}

# 期限ファイル(タスクにしていない期限のため)。チケットにした行(tb deadlines-import)は出さない
reminders_collect_file() {
  [[ -f $REMINDERS_DEADLINES ]] || return 0
  local d label key st today; today=$(reminders_today)
  while IFS=$'\t' read -r d label; do
    [[ -n $d && -n $label ]] || continue
    [[ -z $(reminders_deadline_ticket "$d" "$label") ]] || continue
    st=$(sql "SELECT CASE WHEN date($(q "$d")) IS NULL THEN 'bad' WHEN date($(q "$d")) > date($(q "$today"), '+$REMINDERS_DUE_DAYS days') THEN 'later'
                ELSE $(reminders_due_sql "$(q "$d")") END;")
    [[ $st == bad || $st == later ]] && continue
    key="due:f:$(printf '%s' "$label" | sha256 | cut -c1-10)"
    jq -nc --arg key "$key" --arg st "$st" --arg d "$d" --arg label "$label" \
      '{p: (if $st == "soon" then 4 else 1 end), key: $key, sig: ($st + ":" + $d), what: ("期限: " + $label), who: "人", due: $d, ref: ("deadline " + $d), since: ""}'
  done < <(reminders_deadline_lines)
}

# tb deadlines-import [-p project] [--dry-run]: 期限ファイルの行を、担当なしで期日つきのチケットにする(ADR 0003 の段 5。人だけ)。
# 同じ題・同じ期日のチケットが既にあれば作らない(何度流してもよい)。ファイルは書き換えない(取り込んだ行は reminders がファイルからは出さなくなる)。
# チケットは種別 task・ラベル deadline(名前は設定 system-labels の deadline で変えられる)。期限の通知は他のチケットと同じ(tb reminder scan の overdue / due_soon、通知の規則、tb reminder)
cmd_deadlines-import() {
  local project=deadline dry=0
  while [[ $# -gt 0 ]]; do
    case $1 in
      -p|--project) need_arg "$@"; valid_project "$2"; project=$2; shift 2;;
      --dry-run) dry=1; shift;;
      *) usage "deadlines-import [-p project(既定 deadline)] [--dry-run]";;
    esac
  done
  require_human "tb deadlines-import"
  reminders_has_col tasks due || die "deadlines-import: この DB には期日の列がありません(スキーマが古い)"
  [[ -f $REMINDERS_DEADLINES ]] || { echo "期限ファイルがありません: $REMINDERS_DEADLINES"; return 0; }
  local d label have n=0 m=0 id
  while IFS=$'\t' read -r d label; do
    [[ -n $d && -n $label ]] || continue
    [[ $(sql "SELECT date($(q "$d")) IS $(q "$d");") == 1 ]] || { echo "skip(日付が正しくない): $d | $label"; continue; }
    have=$(reminders_deadline_ticket "$d" "$label")
    if [[ -n $have ]]; then echo "skip(取り込み済み $have): $d | $label"; m=$((m + 1)); continue; fi
    if ((dry)); then echo "add(dry-run): $d | $label"; n=$((n + 1)); continue; fi
    id=$(cmd_add -p "$project" -t "$label" --due "$d" -l "$(system_label_of deadline)") || die "deadlines-import: チケットを作れませんでした: $label"
    echo "add $id: $d | $label"; n=$((n + 1))
  done < <(reminders_deadline_lines)
  echo "作成 $n 件 / 取り込み済み $m 件$( ((dry)) && echo '(dry-run: 何も書いていません)')"
}

# 許可リスト(notify と同じ repos.md)のリポジトリで、開いていて draft でなく、まだ承認も変更依頼もない PR。gh で読むだけ
reminders_collect_prs() {
  local r gh=${TB_GH_BIN:-gh} out
  while IFS= read -r r; do
    [[ -n $r ]] || continue
    out=$("$gh" pr list --repo "$r" --state open --limit 30 --json number,title,isDraft,reviewDecision,createdAt 2>/dev/null) || continue
    jq -c --arg r "$r" '(. // [])[] | select((.isDraft | not) and ((.reviewDecision // "") | IN("", "REVIEW_REQUIRED")))
      | {p: 2, key: "pr:\($r)#\(.number)", sig: (.reviewDecision // "none"), what: ("レビュー待ちの PR: " + (.title // "")),
         who: "レビュー担当", due: "", ref: "\($r)#\(.number)", since: (.createdAt // "")}' <<<"$out" 2>/dev/null || true
  done < <(notify_repos)
}

# 全項目(優先度・期限・古い順・キーの順。同じ状態なら同じ並び)。題は 1 行・80 字まで
reminders_collect() {
  { reminders_collect_db; reminders_collect_file; reminders_collect_prs; } | jq -sc --arg now "$(now)" '
    def age($s): if ($s // "") == "" then "" else
      (((($now | sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601) - ($s | sub("\\.[0-9]+Z$"; "Z") | sub("\\+00:00$"; "Z") | try fromdateiso8601 catch null)) // null) as $d
      | if $d == null then "" elif $d >= 86400 then "\($d / 86400 | floor)d" elif $d >= 3600 then "\($d / 3600 | floor)h" else "\([$d / 60 | floor, 0] | max)m" end) end;
    map(.what = ((.what // "") | gsub("[[:cntrl:]|]"; " ") | gsub(" +"; " ") | sub("^ | $"; "") | .[0:80])
        | .who = ((.who // "") | gsub("[[:cntrl:]|]"; " ") | .[0:40])
        | .age = age(.since))
    | unique_by(.key) | sort_by(.p, (if .due == "" then "9999" else .due end), (if .since == "" then "~" else .since end), .key)'
}

# 前回の記録と比べる。$1=項目(JSON 配列) $2=前回の記録 $3=今日 $4=前回の全件の日 $5=全件にするか(1/0)
# → {items(各項目に status: new|changed|continuing と day), removed, state, changed(出すものがあるか)}
reminders_diff() {
  jq -nc --argjson items "$1" --argjson prev "$2" --arg today "$3" --arg full_day "$4" --argjson full "$5" '
    def days($a; $b): ((($b + "T00:00:00Z") | fromdateiso8601) - (($a + "T00:00:00Z") | fromdateiso8601)) / 86400 | floor;
    ($items | map(. as $i | ($prev[$i.key] // null) as $o
       | $i + {status: (if $o == null then "new" elif $o.sig != $i.sig then "changed" else "continuing" end),
               first: ($o.first // $today)}
       | .day = (days(.first; $today) + 1))) as $cur
    | ($prev | keys - ($items | map(.key))) as $removed
    | {items: $cur, removed: $removed, full: ($full == 1),
       state: ($cur | map({key: .key, value: {sig: .sig, first: .first}}) | from_entries),
       changed: (($cur | any(.status != "continuing")) or ($removed | length > 0) or (($cur | length > 0) and $full_day != $today))}'
}

REMINDERS_PRIO_LABEL='{"1":"急ぎ","2":"人の番","3":"停滞","4":"待ち・予定"}'
# 1 通分の本文。新しい・変わった項目(全件の日はすべて)を 1 行ずつ、継続の項目は 1 行にまとめる
reminders_text() {  # $1=reminders_diff の結果
  jq -r --argjson L "$REMINDERS_PRIO_LABEL" --arg now "$(now)" '
    .full as $full
    | (.items | map(select($full or .status != "continuing"))) as $rows
    | (.items | map(select(($full | not) and .status == "continuing"))) as $cont
    | "[reminders \($now) / \(.items | length) 件"
      + (if $full then " / 全件" else " / 新規 \(.items | map(select(.status == "new")) | length)・変化 \(.items | map(select(.status == "changed")) | length)・継続 \($cont | length)" end)
      + (if (.removed | length) > 0 then "・解消 \(.removed | length)" else "" end) + "]",
      (if ($rows | length) > 0 then "優先 | 何が | 誰の番 | 期限 | 参照 | 経過" else empty end),
      ($rows[] | "\($L[.p | tostring]) | \(.what)\(if .status == "changed" then " (変化)" elif .status == "new" and ($full | not) then " (新規)" else "" end) | \(.who) | \(if .due == "" then "-" else .due end) | \(.ref) | \(if .age == "" then "-" else .age end)"),
      (if ($cont | length) > 0 then "継続: " + ($cont | map("\(.ref)(\(.day) 日目)") | join(", ")) else empty end),
      (if (.items | length) == 0 then "忘れているかもしれないものはありません" else empty end)' <<<"$1"
}

cmd_reminders() {
  local mode=show json=0
  while [[ $# -gt 0 ]]; do
    case $1 in
      --check) mode=check; shift;;
      --publish) mode=publish; shift;;
      --json) json=1; shift;;
      *) usage "reminders: 不明な引数 $1(--check | --publish [--json] | --json)";;
    esac
  done
  [[ $REMINDERS_STALE_HOURS =~ ^[0-9]+$ && $REMINDERS_DUE_DAYS =~ ^[0-9]+$ && $DIGEST_QUEUED_SEC =~ ^[0-9]+$ ]] \
    || usage "TB_REMINDERS_STALE_HOURS / TB_REMINDERS_DUE_DAYS / TB_DIGEST_QUEUED_SEC は数字"
  reminders_today >/dev/null   # TB_TODAY が日付でなければ、ここで使い方の誤り
  local items prev full_day today full=0 diff text
  items=$(reminders_collect) || die "reminders: 材料を読めませんでした"
  prev=$(notify_seen_get reminders:state); [[ -n $prev ]] && jq -e 'type == "object"' <<<"$prev" >/dev/null 2>&1 || prev='{}'
  full_day=$(notify_seen_get reminders:full-day)
  today=$(reminders_today)
  [[ $mode == publish && $full_day != "$today" && $items != "[]" ]] && full=1
  diff=$(reminders_diff "$items" "$prev" "$today" "$full_day" "$full")
  if [[ $mode == check ]]; then   # 何も書かない・何も出さない
    [[ $(jq -r .changed <<<"$diff") == true ]] && return 0
    return $REMINDERS_NO_CHANGE
  fi
  text=$(reminders_text "$diff")
  # 秘密らしい値: 題に紛れ込んだものを伏せる(本文は載せていないが、題や PR の名前に入ることがある)。
  # 継続の項目は本文の行に出ないが、--json と Web UI の行には出るので、全項目の題もあわせて調べる
  if notify_secret_scan "$text"$'\n'"$(jq -r '.items[] | .what, .who' <<<"$diff")"; then
    local kinds=$NOTIFY_HIT_KINDS
    text=$(notify_redact "$text")$'\n'"(秘密らしい値を伏せました: $kinds)"
    local h; while IFS= read -r h; do [[ -n $h ]] && diff=$(jq -c --arg h "$h" '.items |= map((.what, .who) |= (split($h) | join("[伏せた秘密]")))' <<<"$diff"); done <<<"$NOTIFY_HITS"
  fi
  if [[ $mode == publish ]]; then
    local ts last; ts=$(now)
    last=$(jq -c --arg at "$ts" --arg text "$text" '{at: $at, full: .full, removed: .removed, text: $text,
      items: (.items | map({p, key, what, who, due, ref, age, status, day}))}' <<<"$diff")
    sql "BEGIN; $(notify_seen_sql reminders:state "$(jq -c .state <<<"$diff")" "$ts")
      $(notify_seen_sql reminders:last "$last" "$ts")
      $( ((full)) && notify_seen_sql reminders:full-day "$today" "$ts") COMMIT;"
  fi
  if ((json)); then
    jq -c '{full, removed, items: (.items | map({p, key, what, who, due, ref, age, status, day}))}' <<<"$diff"
  else
    printf '%s\n' "$text"
  fi
}

# tb reminder <list|check|publish|scan|nudge>(サービスの名前。古い名前 reminders / due-scan / nudge も残す)
cmd_reminder() {
  if [[ $# -eq 0 ]]; then reminder_usage; exit 2; fi
  local sub=$1 a; shift
  case $sub in
    list|publish)
      for a; do [[ $a == --json ]] || usage "reminder $sub: 不明な引数 $a(--json だけ)"; done
      if [[ $sub == publish ]]; then cmd_reminders --publish "$@"; else cmd_reminders "$@"; fi;;
    check) [[ $# -eq 0 ]] || usage "reminder check(引数なし)"; cmd_reminders --check;;
    scan) cmd_due-scan "$@";;
    nudge) "cmd_$sub" "$@";;   # nudge の本体は届け方(delivery・outbox)と一緒に send.sh にある。bash を外すまでの入口だけ
    help|-h|--help) reminder_usage;;
    *) usage "reminder: 不明なサブコマンド $sub(list / check / publish / scan / nudge)";;
  esac
}
reminder_usage() {
  cat <<'USAGE'
tb reminder list [--json]          人が忘れているかもしれないものを優先度順に出す(何も書かない)
tb reminder check                  出すものがあれば 0、無ければ 3(何も出さず、何も書かない)
tb reminder publish [--json]       まとめを出し、前回の記録を更新する(同じ項目は「継続(N 日目)」の 1 行。1 日 1 回は全件)
tb reminder scan [-p project] [--within N]   期限切れ・期限間近のタスクに履歴のイベントを 1 日 1 回ずつ残す
tb reminder nudge [<宛先> ...] [--pool] [--dry-run] [--idle-min N] [--max N] [--json]   チケットを握ったまま休んでいる agent に催促する
(古い名前 tb reminders [--check | --publish] / tb due-scan / tb nudge も同じに使える。期限ファイルの取り込みは tb deadlines-import のまま)
USAGE
}
