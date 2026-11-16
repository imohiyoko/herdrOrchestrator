# shellcheck shell=bash
# run.sh から番号順に source される。lib/schedwatch.sh の「見張り系の判定」と「実行として数えるもの」。
# 51 とは別の fake scheduler ディレクトリ・DB を使う(51 の件数の検査に影響させない)。
#   - PROMPT 型のジョブは成功すると ran ではなく sent を記録する。sent は実行として数える(failed は数えない)
#   - 見張り系かどうかは、conf の WATCHER=1/0 の明示 > TARGET が watchers に一致 > COMMAND の値が watchers の語を含む。
#     PROMPT のパスやコメントに偶然入った語には一致しない(notify-incident-watch の誤検知の再現)
unset TB_IMMEDIATE_KINDS
export TB_DB=$TMP/schedwatch2.db TB_CONFIG=$TMP/schedwatch2.conf
: >"$TB_CONFIG"
export TB_SCHED_DIR=$TMP/sched2 TB_SCHEDWATCH_NOW=1800000000 TB_LIMIT_WATCHERS="watchx"
export TB_SCHEDWATCH_CONF=$TMP/sched-watch2.conf
mkdir -p "$TB_SCHED_DIR/jobs" "$TB_SCHED_DIR/logs" "$TB_SCHED_DIR/settings"
NOW=$TB_SCHEDWATCH_NOW
# conf: <job> <追加の行>(PROMPT 型。TARGET は watchers に入っていない名前)
mkp() {
  { printf "TARGET=agentz\nWINDOW=00:00-23:59\nINTERVAL_MIN=5\nDAYS=daily\nTZ=Asia/Tokyo\n"; printf '%s\n' "${@:2}"; } >"$TB_SCHED_DIR/jobs/$1.conf"
}
lg() { jq -nc --arg job "$1" --arg a "$3" --argjson e $((NOW - $2 * 60 + 32400)) \
         '{ts:($e|strftime("%Y-%m-%dT%H:%M:%S+0900")),job:$job,action:$a,reason:"t"}' >>"$TB_SCHED_DIR/logs/$1.log"; }

# 誤検知の再現: PROMPT 型・sent あり・ran 0・パスのファイル名に watchx(単語として一致する)。見張り系ではない
mkp falsepos "PROMPT='see /x/incident-watchx-prompt.md'" "# watchx のコメント"
for m in 55 50 45 40 35 30 25 20 15 10 5; do lg falsepos $m sent; done
# 同じ誤検知で、最後の sent が 45 分前に止まっている(部分一致のままだと見張り系になり、ran が無いと出る)。見張り系ではないので出ない
mkp falsepos2 "PROMPT='see /x/incident-watchx-prompt.md'"; lg falsepos2 45 sent; lg falsepos2 5 skipped_state
# sent を数える: WATCHER=1 の PROMPT 型。最後の sent は 5 分前(ran は 0)
mkp sentok "PROMPT='x'" "WATCHER=1"
for m in 55 40 25 5; do lg sentok $m sent; done
# sent が止まった: WATCHER=1 で、最後の sent が 45 分前。その後は skipped_state だけ
mkp sentstale "PROMPT='x'" "WATCHER=1"; lg sentstale 45 sent; for m in 30 15 5; do lg sentstale $m skipped_state; done
# failed は実行として数えない: 最後の sent が 45 分前で、その後は failed
mkp failonly "PROMPT='x'" "WATCHER=1"; lg failonly 45 sent; for m in 30 15 5; do lg failonly $m failed; done
# WATCHER=1 の明示: TARGET が watchers に無くても見張り系(止まっていれば出る)
mkp explicit1 "PROMPT='x'" "WATCHER=1"; lg explicit1 45 ran; lg explicit1 5 skipped_state
# WATCHER=0 の明示: TARGET が watchers に入っていても見張り系ではない
{ printf "TARGET=watchx\nPROMPT='x'\nWATCHER=0\nWINDOW=00:00-23:59\nINTERVAL_MIN=5\nDAYS=daily\nTZ=Asia/Tokyo\n"; } >"$TB_SCHED_DIR/jobs/explicit0.conf"
lg explicit0 45 sent; lg explicit0 5 skipped_state
# 従来どおり: WATCHER 未設定でも TARGET が watchers に一致すれば見張り系
{ printf "TARGET=watchx\nPROMPT='x'\nWINDOW=00:00-23:59\nINTERVAL_MIN=5\nDAYS=daily\nTZ=Asia/Tokyo\n"; } >"$TB_SCHED_DIR/jobs/bytarget.conf"
lg bytarget 45 sent; lg bytarget 5 skipped_state
# 従来どおり: WATCHER 未設定でも COMMAND の値が watchers の語を含めば見張り系
{ printf "COMMAND='bin/watchx check'\nWINDOW=00:00-23:59\nINTERVAL_MIN=5\nDAYS=daily\nTZ=Asia/Tokyo\n"; } >"$TB_SCHED_DIR/jobs/bycommand.conf"
lg bycommand 45 ran; lg bycommand 5 failed
# 見張り系でない COMMAND は、ran が無くても出ない
{ printf "COMMAND='bin/other check'\nWINDOW=00:00-23:59\nINTERVAL_MIN=5\nDAYS=daily\nTZ=Asia/Tokyo\n"; } >"$TB_SCHED_DIR/jobs/plaincmd.conf"
lg plaincmd 45 ran; lg plaincmd 5 skipped_state

dry=$($TB digest --dry-run)
for want in "見張り系ジョブ sentstale の ran が 45 分無い" "見張り系ジョブ failonly の ran が 45 分無い" \
            "見張り系ジョブ explicit1 の ran が 45 分無い" "見張り系ジョブ bycommand の ran が 45 分無い"; do
  [[ $dry == *"$want"* ]] && ok "schedwatch2: dry-run has '$want'" || { ng "schedwatch2: dry-run has '$want'"; echo "   actual: $dry"; }
done
for not in "見張り系ジョブ falsepos" "見張り系ジョブ falsepos2" "見張り系ジョブ sentok" "見張り系ジョブ explicit0" "見張り系ジョブ plaincmd"; do
  [[ $dry != *"$not"* ]] && ok "schedwatch2: dry-run lacks '$not'" || { ng "schedwatch2: dry-run lacks '$not'"; echo "   actual: $dry"; }
done
# bytarget は最後の sent が 45 分前で止まっているので、従来どおり出る
[[ $dry == *"見張り系ジョブ bytarget の ran が 45 分無い"* ]] && ok "schedwatch2: WATCHER unset + TARGET match is still watched" || ng "schedwatch2: WATCHER unset + TARGET match is still watched"
# 失敗率: sent は実行として数える(sent 10 回 + failed 5 回 = 33% は出さない。sent を数えないと 5 回中 5 回で 100% になる)
mkp ratio "PROMPT='x'"; for m in 58 56 54 52 50 48 46 44 42 40; do lg ratio $m sent; done; for m in 30 25 20 15 10; do lg ratio $m failed; done
mkp allfail "PROMPT='x'"; for m in 50 40 30 20 10; do lg allfail $m failed; done
dry=$($TB digest --dry-run)
[[ $dry != *"ジョブ ratio の失敗が多い"* ]] && ok "schedwatch2: sent counts as a run in the failure rate" || { ng "schedwatch2: sent counts as a run in the failure rate"; echo "   actual: $dry"; }
[[ $dry == *"ジョブ allfail の失敗が多い(直近 60 分で 5 回実行して failed 5 回 = 100%"* ]] && ok "schedwatch2: all-failed still alerts" || { ng "schedwatch2: all-failed still alerts"; echo "   actual: $dry"; }
# WATCHER の値が不正なら、見張りは黙らず scheduler 側(show.sh)が拒否する = その job は観測の対象から外れる(他のジョブは見える)
mkp badwatcher "PROMPT='x'" "WATCHER=yes"; lg badwatcher 45 sent
d=$($TB digest --dry-run)
[[ $d == *"見張り系ジョブ sentstale"* && $d != *"badwatcher"* ]] && ok "schedwatch2: a bad WATCHER skips only that job" || ng "schedwatch2: a bad WATCHER skips only that job"
unset TB_SCHED_DIR TB_SCHEDWATCH_NOW TB_LIMIT_WATCHERS TB_SCHEDWATCH_CONF
