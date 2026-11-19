# shellcheck shell=bash
# scheduler の見張り(tb digest の観測)。bin/tb から source される。単体では実行しない。
#
# scheduler のログ(scheduler/logs/<job>.log、1 行 1 JSON)と、ジョブの実効設定(scheduler/bin/show.sh)を読むだけ。
# launchd・scheduler の設定・ログは変更しない。次の 3 つを digest の観測として出す(宛先は DEFAULT_TARGET)。
#   schedoff   : ENABLED=0 のまま disabled_min 分以上、skipped_disabled が続いているジョブ
#   schedstale : 見張り系ジョブで、有効で窓の中なのに、ran が watch_stale_min 分以上無い。
#                「実行された」= ran(COMMAND 型)または sent(PROMPT 型)。failed は数えない。
#                見張り系 = conf の WATCHER=1(WATCHER=0 なら対象外)。WATCHER が無ければ、tb limits の watchers を
#                TARGET か、conf の COMMAND= の行に持つもの
#   schedfail  : 有効なジョブで、直近 fail_window_min 分の実行(ran + sent + failed)が fail_min_runs 回以上あり、
#                failed の割合が fail_rate_pct % 以上。
#                exit 10(宛先が作業中で、tb が送らずに保留した)の failed は「待ち」として、分子にも分母にも入れない。
#                待ちの回数は schedfail の本文に並べて出す。それ以外の exit(11 など)は失敗として数える
#   schedwait  : 有効なジョブで、直近の記録が exit 10 の待ちだけになってから wait_max_min 分以上たった
#                (宛先が長く作業中のまま、または止まっている。wait_max_min = 0 で出さない)
# 同じ観測は、状態が変わるまで 1 回だけ(observations の key。条件が外れたら記録を消すので、また当てはまれば再び出る)。
#   schedoff   key = schedwatch:off:<job>:<止まり始めの時刻(epoch)>
#   schedstale key = schedwatch:stale:<job>:<基準の時刻(epoch)>   (基準 = 最後の ran。無ければ有効になって最初の記録)
#   schedfail  key = schedwatch:fail:<job>
#   schedwait  key = schedwatch:wait:<job>:<待ち始めの時刻(epoch)>
# 設定: config/sched-watch.conf(無ければ .example、人だけが編集。再起動不要)。TB_SCHEDWATCH_<KEY 大文字> で上書きできる。
# 場所: TB_SCHED_DIR(jobs / logs / settings の親。既定は scheduler/)、TB_SCHED_SHOW(show.sh の場所)。
#       テストでは TB_SCHEDWATCH_NOW(epoch)で現在時刻を固定する。

SW_CONF=${TB_SCHEDWATCH_CONF:-$TB_ROOT/config/sched-watch.conf}
SW_DIR=${TB_SCHED_DIR:-$TB_ROOT/../scheduler}
SW_SHOW=${TB_SCHED_SHOW:-$TB_ROOT/../scheduler/bin/show.sh}

# 設定値: 環境変数 TB_SCHEDWATCH_<キー大文字> > sched-watch.conf > .example > 既定。$1=キー $2=既定
sw_cfg() {
  local key=$1 def=$2 up line k v ev
  up=$(tr '[:lower:]' '[:upper:]' <<<"$key"); ev="TB_SCHEDWATCH_$up"
  [[ -z ${!ev:-} ]] || { printf '%s' "${!ev}"; return; }
  if config_has sched-watch; then
    while IFS= read -r line || [[ -n $line ]]; do
      line=$(trim "${line%%#*}"); [[ $line == *=* ]] || continue
      k=$(trim "${line%%=*}"); v=$(trim "${line#*=}")
      [[ $k == "$key" ]] && { printf '%s' "$v"; return; }
    done < <(config_text sched-watch)
  fi
  printf '%s' "$def"
}

sw_now() { printf '%s' "${TB_SCHEDWATCH_NOW:-$(date +%s)}"; }

# ログ 1 ファイルを集計して JSON 1 行にする($1=ファイル $2=現在(epoch) $3=窓(秒))。dry_run の行は数えない
sw_summarize() {
  jq -Rsc --argjson now "$2" --argjson win "$3" '
    def ep: . as $t
      | if test("Z$") then ($t | sub("Z$"; "") | strptime("%Y-%m-%dT%H:%M:%S") | mktime)
        else ($t | capture("^(?<d>.{19})(?<s>[+-])(?<h>[0-9]{2}):?(?<m>[0-9]{2})$")) as $c
          | ($c.d | strptime("%Y-%m-%dT%H:%M:%S") | mktime)
            - (($c.s + "1" | tonumber) * (($c.h | tonumber) * 3600 + ($c.m | tonumber) * 60)) end;
    split("\n")
    | map(fromjson? | select(type == "object" and (.dry_run | not) and (.ts | type == "string") and (.action | type == "string"))
          | {e: (.ts | ep), a: .action, w: (.action == "failed" and .exit == 10)})
    | sort_by(.e) as $L
    | ([$L[] | select(.a == "skipped_disabled") | .e] | max) as $lastd
    | ([$L[] | select(.a != "skipped_disabled") | .e] | max) as $lastnd
    | ([$L[] | select((.a == "ran" or .a == "sent" or .a == "failed") and (.w | not)) | .e] | max) as $lastrun
    | ([$lastrun, $lastd] | map(select(. != null)) | max) as $waitfrom   # 送れた実行か、止めていた記録のあとからを待ちとして数える
    | {
        n: ($L | length),
        last_action: ($L | last | .a),
        last_is_disabled: (($L | last | .a) == "skipped_disabled"),
        streak_start: ([$L[] | select(.a == "skipped_disabled" and .e > ($lastnd // -1)) | .e] | min),
        streak_count: ([$L[] | select(.a == "skipped_disabled" and .e > ($lastnd // -1))] | length),
        last_ran: ([$L[] | select(.a == "ran" or .a == "sent") | .e] | max),
        enabled_since: ([$L[] | select(.a != "skipped_disabled" and .e > ($lastd // -1)) | .e] | min),
        ran_w: ([$L[] | select((.a == "ran" or .a == "sent") and .e >= ($now - $win))] | length),
        failed_w: ([$L[] | select(.a == "failed" and (.w | not) and .e >= ($now - $win))] | length),
        wait_w: ([$L[] | select(.w and .e >= ($now - $win))] | length),
        wait_start: ([$L[] | select(.w and .e > ($waitfrom // -1)) | .e] | min),
        wait_count: ([$L[] | select(.w and .e > ($waitfrom // -1))] | length)
      }' "$1" 2>/dev/null
}

# DAYS(show.sh の days)に今日の曜日(ISO 1..7)が入るか。$1=days $2=今日の曜日番号
sw_day_ok() {
  local t out=""
  for t in $(tr ',A-Z' ' a-z' <<<"$1"); do
    case $t in
      daily|everyday) out+=" 1 2 3 4 5 6 7";; weekdays) out+=" 1 2 3 4 5";; weekends) out+=" 6 7";;
      mon*) out+=" 1";; tue*) out+=" 2";; wed*) out+=" 3";; thu*) out+=" 4";; fri*) out+=" 5";; sat*) out+=" 6";; sun*) out+=" 7";;
    esac
  done
  [[ " $out " == *" $2 "* ]]
}

# "HH:MM[:SS]" → 0 時からの秒。終端で秒が無ければ :59(scheduler と同じ意味)
sw_hms() {
  [[ $1 =~ ^([0-9]{1,2}):([0-9]{2})(:([0-9]{2}))?$ ]] || return 1
  local s=0; [[ -n ${BASH_REMATCH[3]} ]] && s=$((10#${BASH_REMATCH[4]})) || { [[ ${2:-} == end ]] && s=59; }
  echo $((10#${BASH_REMATCH[1]} * 3600 + 10#${BASH_REMATCH[2]} * 60 + s))
}

# conf の COMMAND= の値だけを出す(1 行 1 値)。引用符で始まれば閉じ引用符までの内側、無ければ最初の " #" の前まで。
# 行末の コメント(COMMAND=x # watchx)の語は含めない。bash の while read だけで書く(awk・sed の方言に依存しない)
sw_command_value() {
  local line v q
  while IFS= read -r line || [[ -n $line ]]; do
    [[ $line =~ ^[[:space:]]*COMMAND=(.*)$ ]] || continue
    v=${BASH_REMATCH[1]}; q=${v:0:1}
    if [[ $q == "'" || $q == '"' ]]; then
      v=${v:1}; v=${v%%"$q"*}
    else
      v=${v%% \#*}; v=${v%%$'\t'\#*}
    fi
    printf '%s\n' "$v"
  done <"$1"
}

collect_sched_obs() {
  [[ $(sw_cfg enabled 1) == 1 ]] || return 0
  [[ -d $SW_DIR/jobs && -d $SW_DIR/logs && -f $SW_SHOW ]] || return 0
  local off_min stale_min fail_win fail_runs fail_pct wait_max
  off_min=$(sw_cfg disabled_min 30); stale_min=$(sw_cfg watch_stale_min 20)
  fail_win=$(sw_cfg fail_window_min 60); fail_runs=$(sw_cfg fail_min_runs 5); fail_pct=$(sw_cfg fail_rate_pct 50)
  wait_max=$(sw_cfg wait_max_min 180)
  [[ $off_min =~ ^[0-9]+$ && $stale_min =~ ^[0-9]+$ && $fail_win =~ ^[0-9]+$ && $fail_runs =~ ^[0-9]+$ && $fail_pct =~ ^[0-9]+$ && $wait_max =~ ^[0-9]+$ ]] \
    || usage "sched-watch.conf の disabled_min / watch_stale_min / fail_window_min / fail_min_runs / fail_rate_pct / wait_max_min は 0 以上の整数"
  # 先頭が 0 の値(08 など)を算術で 8 進数として読まないよう、10 進数にそろえる
  off_min=$((10#$off_min)); stale_min=$((10#$stale_min)); fail_win=$((10#$fail_win))
  fail_runs=$((10#$fail_runs)); fail_pct=$((10#$fail_pct)); wait_max=$((10#$wait_max))
  local now watchers conf job cfg log sum w is_watch enabled start end days tz dnum sec ws we ref key cur=""
  now=$(sw_now); watchers=$(limit_get watchers 2>/dev/null || true)
  for conf in "$SW_DIR"/jobs/*.conf; do
    [[ -f $conf ]] || continue
    job=$(basename "$conf" .conf)
    cfg=$(JOBS_DIR=$SW_DIR/jobs LOG_DIR=$SW_DIR/logs SCHED_SETTINGS_DIR=$SW_DIR/settings bash "$SW_SHOW" "$job" 2>/dev/null) || continue
    jq -e . >/dev/null 2>&1 <<<"$cfg" || continue
    enabled=$(jq -r .enabled <<<"$cfg"); log=$SW_DIR/logs/$job.log
    [[ -f $log ]] || continue
    sum=$(sw_summarize "$log" "$now" $((fail_win * 60))) || continue
    [[ -n $sum ]] || continue
    # 見張り系か: conf の WATCHER=1 / 0 の明示が最優先。無ければ、TARGET が watchers に入っている、または
    # conf の COMMAND= の値(行末コメントは除く)が watchers の名前を含む。PROMPT のパスやコメントに入った語には一致させない
    is_watch=0
    case $(jq -r '.watcher // empty' <<<"$cfg") in
      1) is_watch=1 ;;
      0) ;;
      *) for w in $watchers; do
           [[ $(jq -r .target <<<"$cfg") == "$w" ]] && is_watch=1
           sw_command_value "$conf" | grep -qwF -- "$w" && is_watch=1
         done ;;
    esac
    if [[ $enabled == false ]]; then
      # 1. ENABLED=0 のまま続いている(ログの最後が skipped_disabled のときだけ。止め始めは「最後の有効な記録のあと最初の skipped_disabled」)
      [[ $(jq -r .last_is_disabled <<<"$sum") == true ]] || continue
      start=$(jq -r '.streak_start // empty' <<<"$sum"); [[ -n $start ]] || continue
      ((now - start >= off_min * 60)) || continue
      key="schedwatch:off:$job:$start"; cur+="$(q "$key"),"
      obs_seen "$key" || add_item "$DEFAULT_TARGET" obs "sched" "$key" "" schedoff "scheduler 観測 / $job" \
        "ジョブ $job が ENABLED=0 のまま $(((now - start) / 60)) 分(skipped_disabled $(jq -r .streak_count <<<"$sum") 回、その間の実行 0 回)$( ((is_watch)) && echo '。見張り系のジョブで、見回りが動いていない' )。有効にするのは人(scheduler の settings か conf)"
      continue
    fi
    # 2. 見張り系: 有効で窓の中なのに ran が無い
    if ((is_watch)); then
      start=$(jq -r .start <<<"$cfg"); end=$(jq -r .end <<<"$cfg"); days=$(jq -r .days <<<"$cfg"); tz=$(jq -r '.tz // "Asia/Tokyo"' <<<"$cfg")
      dnum=$(TZ=$tz date -r "$now" +%u 2>/dev/null || TZ=$tz date -d "@$now" +%u)
      sec=$(TZ=$tz date -r "$now" +%H:%M:%S 2>/dev/null || TZ=$tz date -d "@$now" +%H:%M:%S)
      sec=$(sw_hms "$sec") || sec=-1
      ws=$(sw_hms "$start") || ws=-1; we=$(sw_hms "$end" end) || we=-1
      if sw_day_ok "$days" "$dnum" && ((ws >= 0 && we >= 0 && sec >= ws + stale_min * 60 && sec <= we)); then
        ref=$(jq -r '.last_ran // .enabled_since // empty' <<<"$sum")
        if [[ -n $ref ]] && ((now - ref >= stale_min * 60)); then
          key="schedwatch:stale:$job:$ref"; cur+="$(q "$key"),"
          obs_seen "$key" || add_item "$DEFAULT_TARGET" obs "sched" "$key" "" schedstale "scheduler 観測 / $job" \
            "見張り系ジョブ $job の ran が $(((now - ref) / 60)) 分無い($( [[ $(jq -r '.last_ran // empty' <<<"$sum") == "$ref" ]] && echo '最後の ran から' || echo '有効になってから ran が 1 回も無く、' )。直近の記録は $(jq -r .last_action <<<"$sum")。scheduler/logs/$job.log を確認)"
        fi
      fi
    fi
    # 3. 失敗が多い(exit 10 の待ちは分子にも分母にも入れない。回数は本文に並べる)
    local rans failed waits wstart
    failed=$(jq -r .failed_w <<<"$sum"); rans=$(( $(jq -r .ran_w <<<"$sum") + failed )); waits=$(jq -r .wait_w <<<"$sum")
    if ((rans > 0 && rans >= fail_runs && failed * 100 >= fail_pct * rans)); then
      key="schedwatch:fail:$job"; cur+="$(q "$key"),"
      obs_seen "$key" || add_item "$DEFAULT_TARGET" obs "sched" "$key" "" schedfail "scheduler 観測 / $job" \
        "ジョブ $job の失敗が多い(直近 $fail_win 分で $rans 回実行して failed $failed 回 = $((failed * 100 / rans))%。しきい値 ${fail_pct}%$( ((waits > 0)) && echo "。ほかに宛先の作業中で待った回(exit 10)が $waits 回あり、これは数えていない" )。scheduler/logs/$job.log を確認)"
    fi
    # 4. 宛先の作業中(exit 10)で待ったままの時間が長い
    wstart=$(jq -r '.wait_start // empty' <<<"$sum")
    if ((wait_max > 0)) && [[ -n $wstart ]] && ((now - wstart >= wait_max * 60)); then
      key="schedwatch:wait:$job:$wstart"; cur+="$(q "$key"),"
      obs_seen "$key" || add_item "$DEFAULT_TARGET" obs "sched" "$key" "" schedwait "scheduler 観測 / $job" \
        "ジョブ $job が、宛先の作業中(exit 10)で $(((now - wstart) / 60)) 分送れていない(その間 $(jq -r .wait_count <<<"$sum") 回とも待ち)。宛先が止まっていないか確認。scheduler/logs/$job.log を確認"
    fi
  done
  # 条件が外れたものは記録を消す(再び当てはまったら、また送る)
  sql "DELETE FROM main.observations WHERE herdr_session=$SQS AND kind IN ('schedoff','schedstale','schedfail','schedwait') AND key NOT IN (${cur}'');"
}
