# shellcheck shell=bash
# 会計役のレビュー材料と、設定役への改善提案(review-pack / proposal*)
# bin/tb から source される。単体では実行しない。

# ---------- 会計役(定期レビュー)→ 設定役(人事)へのプロンプト改善提案 ----------
HR_NAME=${TB_HR_NAME:-hr}
# 提案(project proposal のチケット)の宛先・担当: projects の proposal=<名前> の行。無ければ HR_NAME(TB_HR_NAME、既定 hr)
proposal_target() {
  local k v t=$HR_NAME
  if config_has projects; then
    while IFS='=' read -r k v || [[ -n $k ]]; do
      k=${k//[[:space:]]/}; v=${v//[[:space:]]/}
      [[ $k == proposal && -n $v ]] && t=$v
    done < <(config_text projects)
  fi
  printf '%s' "$t"
}
SCHED_LOGS_DIR=${TB_SCHED_LOGS:-$TB_ROOT/../scheduler/logs}

# 会計役に渡すレビュー材料。生ログではなく集計と上位だけを出す(トークン節約)。--since 省略時は前回 --mark 以降(初回は 24 時間)
cmd_review-pack() {
  local since="" mark=0 lim=${TB_REVIEW_TOP:-8}
  while [[ $# -gt 0 ]]; do
    case $1 in
      --since) need_arg "$@"; since=$2; shift 2;;
      --mark) mark=1; shift;;
      *) usage "review-pack: 不明な引数 $1";;
    esac
  done
  [[ -n $since ]] || since=$(sql "SELECT COALESCE(MAX(until_at), strftime('%Y-%m-%dT%H:%M:%SZ','now','-1 day')) FROM reviews;")
  [[ $since =~ ^[0-9T:.Z-]{10,30}$ ]] || usage "review-pack: --since は ISO 8601"
  local until; until=$(now)
  local w="created_at > $(q "$since")"   # どの表もこのセッションの行だけ(表の名前はこのセッションのビュー)
  sec() { printf '\n## %s\n' "$1"; }
  tsv() { sql -separator ' | ' -nullvalue - "$@"; }
  printf '# レビュー材料(%s 〜 %s)\n' "$since" "$until"
  echo "生ログではなく集計です。詳しく見たいものだけ tb のコマンドで引いてください(tb messages は無いので sqlite3 で messages テーブル)。"
  sec "送信(種類 | 結果 | 件数 | 平均文字数)"
  tsv "SELECT kind, result, COUNT(*), CAST(AVG(length(body)) AS INTEGER) FROM messages WHERE $w GROUP BY kind, result ORDER BY COUNT(*) DESC;"
  sec "送れなかった宛先(宛先 | busy | failed)"
  tsv "SELECT target, SUM(result='busy'), SUM(result='failed') FROM messages WHERE $w AND result!='sent' GROUP BY target ORDER BY COUNT(*) DESC LIMIT $lim;"
  sec "長い送信(トークンを食うもの: 種類 | 宛先 | 文字数 | 先頭)"
  tsv "SELECT kind, target, length(body), substr(replace(body, char(10), ' '),1,80) FROM messages WHERE $w ORDER BY length(body) DESC LIMIT 5;"
  sec "tb send(状態 | 件数)と拒否・承認待ちの例"
  tsv "SELECT status, COUNT(*) FROM outbox WHERE $w GROUP BY status;"
  tsv "SELECT 'O-'||id, status, from_name||' -> '||target, kind, COALESCE(reason,'') FROM outbox WHERE $w AND status IN ('denied','pending') ORDER BY id DESC LIMIT $lim;"
  sec "人の承認(承認された送信の操作 | 回数)— 何度も承認されているものは allow にする候補"
  tsv "SELECT 'send:'||kind||':'||target, COUNT(*) FROM outbox WHERE $w AND reason LIKE '%人が承認%' GROUP BY kind, target ORDER BY COUNT(*) DESC LIMIT $lim;"
  tsv "SELECT 'approvals 変更', pattern, value, created_at FROM approval_changes WHERE $w ORDER BY id;"
  sec "人への依頼の繰り返し(依頼元 | 種類 | 対象・内容 | 回数)— 同じ依頼を何度も人に出していないか(2 回以上だけ)"
  tsv "SELECT who, what, substr(obj,1,40), COUNT(*) AS n FROM (
         SELECT from_name AS who, 'send:'||kind AS what, target AS obj FROM outbox
           WHERE $w AND (status='pending' OR reason LIKE '%人が承認%' OR status='rejected')
         UNION ALL
         SELECT requested_by, 'agent:'||op, target FROM agent_ops
           WHERE $w AND requested_by!='human' AND (decided_at IS NULL OR decided_at!=created_at)
         UNION ALL
         SELECT from_name, kind, replace(text, char(10), ' ') FROM comments
           WHERE $w AND kind IN ('question','blocked'))
       GROUP BY who, what, obj HAVING n >= 2 ORDER BY n DESC LIMIT $lim;"
  sec "仕事の混在(agent | ロール | project 数 | ロール変更 | project)— 1 体が複数種類の仕事をしていないか(該当だけ)"
  tsv "SELECT a.name, COALESCE(a.role,'-'), p.np, COALESCE(rc.n,0), p.ps
       FROM agents a
       LEFT JOIN (SELECT owner_agent, COUNT(DISTINCT project) AS np, group_concat(DISTINCT project) AS ps FROM tasks
                  WHERE owner_agent IS NOT NULL AND (status IN ($ST_HELD_SQL) OR updated_at > $(q "$since"))
                  GROUP BY owner_agent) p ON p.owner_agent=a.name
       LEFT JOIN (SELECT agent, COUNT(*) AS n FROM role_changes WHERE $w GROUP BY agent) rc ON rc.agent=a.name
       WHERE (COALESCE(p.np,0) > 1 OR COALESCE(rc.n,0) > 0) ORDER BY COALESCE(p.np,0) DESC, a.name LIMIT $lim;"
  sec "報告(種類 | 件数)と、質問・ブロックの例"
  tsv "SELECT kind, COUNT(*) FROM comments WHERE $w GROUP BY kind;"
  tsv "SELECT 'C-'||id, kind, from_name, substr(replace(text, char(10), ' '),1,100) FROM comments WHERE $w AND kind IN ('question','blocked') ORDER BY id DESC LIMIT $lim;"
  sec "まとめ送信(回数 | 平均件数 | 平均文字数)"
  tsv "SELECT COUNT(*), ROUND(AVG(item_count),1), CAST(AVG(length(body)) AS INTEGER) FROM digests WHERE $w;"
  sec "アラート(状態 | 緊急度 | 件数 | まとめた重複の合計 | 平均解析分)"
  tsv "SELECT status, COALESCE(severity,'未解析'), COUNT(*), SUM(count)-COUNT(*),
         CAST(ROUND(AVG(CASE WHEN analyzed_at IS NOT NULL THEN (julianday(analyzed_at)-julianday(analysis_started_at))*1440 END)) AS INTEGER)
       FROM alerts WHERE last_seen > $(q "$since") GROUP BY status, severity;"
  echo "重複の多いアラート(見張り役の見回りが同じものを拾い続けていないか):"
  tsv "SELECT 'A-'||id, count, COALESCE(severity,'-'), substr(title,1,60) FROM alerts WHERE last_seen > $(q "$since") AND count > 1 ORDER BY count DESC LIMIT $lim;"
  echo "low と判定された解析(誤報・過剰な見回りの候補):"
  tsv "SELECT 'A-'||id, substr(title,1,60), substr(replace(COALESCE(result_summary,''), char(10), ' '),1,80) FROM alerts WHERE analyzed_at > $(q "$since") AND severity='low' ORDER BY id DESC LIMIT $lim;"
  sec "観測(tb が見つけた滞留など: 種類 | 件数)"
  tsv "SELECT kind, COUNT(*) FROM observations WHERE $w GROUP BY kind;"
  sec "役割の配布(agent | 回数)— compact が多い agent は文脈が大きすぎる候補"
  tsv "SELECT agent, COUNT(*), SUM(source='compact') FROM role_briefings WHERE $w GROUP BY agent ORDER BY COUNT(*) DESC LIMIT $lim;"
  sec "定期実行(job | action | 件数)"
  if [[ -d $SCHED_LOGS_DIR ]]; then
    local f
    for f in "$SCHED_LOGS_DIR"/*.log; do
      [[ -f $f ]] || continue
      tail -n 2000 "$f" | jq -r --arg s "$since" 'select((.ts // "") >= ($s[0:10])) | "\(.job) | \(.action)"' 2>/dev/null | sort | uniq -c | awk '{c=$1; $1=""; print substr($0,2) " | " c}'
    done
  else echo "(scheduler のログなし)"; fi
  sec "提案の状況(状態 | 件数)"
  tsv "SELECT status, COUNT(*) FROM proposals WHERE $w GROUP BY status;"
  ((mark)) && sql "INSERT INTO main.reviews (herdr_session, since_at, until_at, created_at) VALUES ($SQS, $(q "$since"), $(q "$until"), $(q "$until"));" >/dev/null
  return 0
}

# 提案のサービス: tb proposal add|list|show|resolve。古い名前(tb proposal --role ... / tb proposals / tb proposal-resolve)も同じに動く
cmd_proposal() {
  local sub=${1:-}
  if [[ $sub == --* && $sub != --help ]]; then proposal_add "$@"; return; fi   # 古い形: tb proposal --role ...
  shift || true
  case $sub in
    add) proposal_add "$@";;
    list) [[ $# -eq 0 || ${1:-} == --status ]] || usage "proposal list: 不明な引数 $1"; cmd_proposals "$@";;
    show) [[ $# -eq 1 && $1 =~ ^P-[0-9]+$ ]] || usage "proposal show P-<n>"; cmd_proposals "$1";;
    resolve) cmd_proposal-resolve "$@";;
    ""|-h|--help|help) proposal_usage; [[ -n $sub ]] || exit 2;;
    *) usage "proposal: 不明なサブコマンド $sub(add / list / show / resolve)";;
  esac
}
proposal_usage() {
  cat <<'USAGE'
tb proposal add --role <roles のファイル|-> --title T (--text X|--text-file F)   改善提案を設定役へ送る
tb proposal list [--status open|applied|rejected]
tb proposal show P-<n>
tb proposal resolve P-<n> --status applied|rejected [--note N]   設定役か人だけ
(古い名前 tb proposal --role ... / tb proposals / tb proposal-resolve も同じに使える)
USAGE
}

# 会計役 → 設定役(人事)への提案。保存してから送る(使用中なら tb digest が再送)
proposal_add() {
  local role="" title="" text="" tfile=""
  while [[ $# -gt 0 ]]; do
    case $1 in
      --role) need_arg "$@"; role=$2; shift 2;;
      --title) need_arg "$@"; title=$2; shift 2;;
      --text) need_arg "$@"; text=$2; shift 2;;
      --text-file) need_arg "$@"; tfile=$2; shift 2;;
      --from|--name|--pane) usage "proposal: 提案者は herdr から自動で決まります";;
      *) usage "proposal: 不明な引数 $1";;
    esac
  done
  [[ $role =~ ^[A-Za-z0-9._-]{1,64}(\.md|\.conf)?$|^-$ ]] || usage "proposal: --role は roles/ のファイル名(全体に関わるなら -)"
  blank "$title" && usage "proposal: --title が必要です"
  if [[ -n $tfile ]]; then [[ -f $tfile && -r $tfile ]] || die "--text-file を読めません: $tfile"; text=$(head -c 20000 "$tfile"); fi
  blank "$text" && usage "proposal: --text か --text-file が必要です"
  resolve_label || return 3
  # proposals はチケット(project proposal)と proposal_details の互換ビュー(020)。INSTEAD OF トリガーが両方に書く。
  # ビュー経由では last_insert_rowid() が使えないので、同じトランザクションで振った番号を読む。チケットの担当は設定役
  local id; id=$(sql "BEGIN IMMEDIATE; INSERT INTO main.proposals (herdr_session, role_file, title, text, status, by_name, created_at) VALUES
    ($SQS, $(q "$role"), $(q "$title"), $(q "$text"), 'open', $(q "$LABEL_NAME"), $(q "$(now)"));
    UPDATE main.tasks SET owner_agent=$(q "$(proposal_target)") WHERE herdr_session=$SQS AND id=(SELECT task_id FROM main.proposal_details WHERE proposal_no=(SELECT MAX(proposal_no) FROM main.proposal_details));
    SELECT MAX(proposal_no) FROM main.proposal_details; COMMIT;")
  local r=0; proposal_deliver "$id" >/dev/null 2>&1 || r=$?
  echo "P-$id $([[ $r == 0 ]] && echo delivered || echo saved-undelivered)"
}

proposal_deliver() {  # $1=id
  local row msg
  row=$(session_row proposals id "$1")
  [[ -n $row && $(jq -r '.delivered_at // ""' <<<"$row") == "" ]] || return 0
  msg="[from: tb / proposal / P-$1 / info] $(jq -r .by_name <<<"$row") から roles/$(jq -r .role_file <<<"$row") の改善提案: $(oneline "$(jq -r .title <<<"$row")" 120) (全文: tb proposal show P-$1。対応後に tb proposal resolve P-$1)"
  herdr_send "$(proposal_target)" "$msg" "P-$1" proposal || return $?
  sql "UPDATE main.proposals SET delivered_at=$(q "$(now)") WHERE herdr_session=$SQS AND id=$1;"
}

cmd_proposals() {
  if [[ ${1:-} =~ ^P-[0-9]+$ ]]; then
    local row; row=$(session_row proposals id "${1#P-}")
    [[ -n $row ]] || die "提案が見つかりません: $1"
    jq -r '"P-\(.id) [\(.status)] roles/\(.role_file)  \(.title)\n提案: \(.by_name) / \(.created_at)  チケット: \(.task_id // "-")\(if .note then "\n対応: \(.note)" else "" end)\n\n\(.text)"' <<<"$row"
    return 0
  fi
  local where="1=1"
  if [[ ${1:-} == --status ]]; then [[ ${2:-} =~ ^(open|applied|rejected)$ ]] || usage "proposals: --status は open|applied|rejected"; where="status=$(q "$2")"; fi
  sql -nullvalue - -separator $'\t' "SELECT 'P-'||id, status, role_file, by_name, created_at, title FROM proposals WHERE ($where) ORDER BY id DESC LIMIT 100;"
}

# 設定役(人事)か人が、提案を反映した・見送ったと記録する。提案者に結果が返る
cmd_proposal-resolve() {
  [[ $# -ge 1 && $1 =~ ^P-[0-9]+$ ]] || usage "proposal-resolve P-<n> --status applied|rejected [--note N]"
  local id=${1#P-} st="" note=""; shift
  while [[ $# -gt 0 ]]; do
    case $1 in
      --status) need_arg "$@"; st=$2; shift 2;;
      --note) need_arg "$@"; note=$2; shift 2;;
      *) usage "proposal-resolve: 不明な引数 $1";;
    esac
  done
  [[ $st == applied || $st == rejected ]] || usage "proposal-resolve: --status は applied|rejected"
  if [[ -n ${HERDR_PANE_ID:-} ]]; then
    resolve_label || return 3
    local pt; pt=$(proposal_target)
    [[ $LABEL_NAME == "$pt" ]] || die "提案を閉じられるのは設定役(${pt}。projects の proposal の行)か人だけです"
  fi
  [[ $(sql "SELECT status FROM proposals WHERE id=$id;") == open ]] || die "P-$id は open ではありません"
  sql "UPDATE main.proposals SET status=$(q "$st"), note=NULLIF($(q "$note"),''), resolved_at=$(q "$(now)") WHERE herdr_session=$SQS AND id=$id;"
  echo "P-$id $st"
}

# tb review <pack|submit>(サービスの名前。古い名前 review-pack / review-submit も残す)。submit の本体は gate.sh(記録の形を gate と共有する)
cmd_review() {
  if [[ $# -eq 0 ]]; then review_usage; exit 2; fi
  local sub=$1; shift
  case $sub in
    pack|submit) "cmd_review-$sub" "$@";;
    help|-h|--help) review_usage;;
    *) usage "review: 不明なサブコマンド $sub(pack / submit)";;
  esac
}
review_usage() {
  cat <<'USAGE'
tb review pack [--since ISO8601] [--mark]   会計役に渡すレビュー材料(集計と上位だけ)
tb review submit <task-id> --lineage L --sha S --blocker N --should N --verdict pass|fail [--pr o/r#n] [--evidence PATH]   レビュー結果を記録する
(古い名前 tb review-pack / tb review-submit も同じに使える)
USAGE
}
