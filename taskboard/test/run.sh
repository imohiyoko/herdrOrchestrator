#!/usr/bin/env bash
# tb のテスト。本物の herdr は一切呼ばない(HERDR_BIN を疑似 herdr に固定)。
# 各機能のテストは cases/NN_*.sh(taskboard/lib/ のファイルにおおむね対応)。
# ケースは「ユニット」ごとに別のプロセス・別の一時ディレクトリ(DB・設定・疑似 herdr の記録)で流し、ユニット同士は並列に流す。
#   ユニット = 同じシェルで番号順に流すケースの並び。前のケースが作った状態(変数・DB)を使うケースは groups に 1 行で書く。
#   groups に無いケースは 1 つで 1 ユニット。どのユニットでも最後に 99_zzz_session_sweep.sh を流す(そのユニットが作った DB を見る)
#
#   run.sh                  すべてを並列に流す(本数は -j。既定は CPU の数、TB_TEST_JOBS でも変えられる)
#   run.sh -j 1             1 本ずつ流す
#   run.sh <ケース> ...      指定したケース(を含むユニット)だけ流す。例: run.sh 93_prqueue.sh
#   run.sh --shard 2of4     4 つに分けた 2 つ目だけ流す(CI の並列ジョブ用。weights の秒数でおおむね揃える)
#   run.sh --list-shards    組の名前を 1 行ずつ出す(既定 4 つ。TB_TEST_SHARDS で変える)
#   run.sh --shard-cases 2of4   組に入るケースの一覧
#   run.sh --units          ユニットの一覧(1 行 1 ユニット)
#   run.sh --timings        すべてを流して、ケースごとの秒数を weights に書き直す
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
SWEEP=99_zzz_session_sweep.sh

# ---------- 1 ユニットを流す(子プロセス): run.sh --unit <ケース> ...
if [[ ${1-} == --unit ]]; then
  shift
  TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
  export HERDR_SESSION_FILE=$TMP/no-herdr-session   # 手元の checkout の herdr-session に左右されない
  export TB_DB=$TMP/tasks.db TB_CONFIG=$TMP/projects.conf TB_LIMITS=$TMP/limits-default.conf
  unset HERDR_SOCKET_PATH   # 実行する pane の socket に左右されない(HSESS は HERDR_SOCKET_PATH を HERDR_SESSION より優先する)
  export HERDR_BIN=$HERE/fake-herdr FAKE_LOG=$TMP/herdr.log
  # scheduler のジョブをファイル(jobs/*.conf)に置くケースがあるので、DB の後にファイルも読ませる。既定(DB だけ)は 53_sched_jobs で確かめる
  export SCHED_FILE_FALLBACK=1
  TB=$HERE/../bin/tb
  # 起動直後の準備待ちは既定では短く、送信確認は切る(98_ready で個別に有効化)
  export TB_PROMPT_VERIFY_SEC=0 TB_SPAWN_POLL_SEC=0.05 TB_SPAWN_READY_SEC=0.2 TB_VERIFY_POLL_SEC=0.05
  : >"$FAKE_LOG"
  pass=0; fail=0
  ok()   { pass=$((pass+1)); echo "ok   $1"; }
  ng()   { fail=$((fail+1)); echo "FAIL $1"; }
  eq()   { [[ "$2" == "$3" ]] && ok "$1" || { ng "$1"; echo "   expected: $3"; echo "   actual:   $2"; }; }
  rc()   { "$@" >/dev/null 2>&1; echo $?; }
  prompts() { grep -c '^prompt' "$FAKE_LOG"; }
  # ケースは変数 c などを自由に使うので、ここの変数は __ で始める
  __cases=("$@" "$SWEEP")
  for ((__i = 0; __i < ${#__cases[@]}; __i++)); do
    __t0=${EPOCHREALTIME-}
    # shellcheck source=/dev/null
    source "$HERE/cases/${__cases[__i]}"
    [[ -n ${TB_TEST_TIMES-} ]] && echo "${__cases[__i]} $__t0 ${EPOCHREALTIME-}" >>"$TB_TEST_TIMES"   # --timings は bash 5 以上(EPOCHREALTIME)
  done
  echo; echo "pass=$pass fail=$fail"; [[ $fail -eq 0 ]]
  exit
fi

# ---------- ユニット・重み・組
all_cases() { (cd "$HERE/cases" && ls [0-9][0-9]_*.sh) | grep -vxF "$SWEEP"; }
units() {  # 1 行 1 ユニット(ケースを空白区切り)。groups の行が先、残りは 1 つずつ
  local listed c
  sed -n 's/^[a-z][a-z0-9_-]*:[[:space:]]*//p' "$HERE/groups" | tr -s ' \t' '  ' | sed 's/ *$//' | grep . || true
  listed=$(sed -n 's/^[a-z][a-z0-9_-]*:\(.*\)/\1/p' "$HERE/groups" | tr -s ' \t' '\n\n' | grep . || true)
  for c in $listed; do [[ -f $HERE/cases/$c ]] || { echo "groups: ケースがありません: $c" >&2; return 1; }; done
  all_cases | grep -vxF -f <(printf '%s\n' "$listed") || true
}
weight() {  # ユニットの重み(秒。weights に無いケースは 10)
  awk -v u=" $* " 'NR == FNR { w[$1] = $2; next } END { n = split(u, a, " "); s = 0; for (i = 1; i <= n; i++) s += (a[i] in w) ? w[a[i]] : 10; printf "%d\n", s + 0.5 }' "$HERE/weights" /dev/null
}
by_weight() {  # ユニットを重い順に(重いものから始めると並列の終わりが揃う)
  local u; while IFS= read -r u; do printf '%s\t%s\n' "$(weight $u)" "$u"; done | sort -t$'\t' -k1,1nr -k2,2 | cut -f2
}
nshards=${TB_TEST_SHARDS:-4}
shard_names() { local i; for ((i = 1; i <= nshards; i++)); do echo "${i}of$nshards"; done; }
shard_units() {  # $1=KofN。重い順に、いちばん軽い組へ入れていく
  local k=${1%of*} n=${1#*of}
  units | by_weight | while IFS= read -r u; do printf '%s\t%s\n' "$(weight $u)" "$u"; done |
    awk -F'\t' -v k="$k" -v n="$n" '{ m = 1; for (i = 2; i <= n; i++) if (t[i] < t[m]) m = i; t[m] += $1; if (m == k) print $2 }'
}

# ---------- 引数
jobs=${TB_TEST_JOBS:-$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 2)} sel=() shard="" timings=""
while [[ $# -gt 0 ]]; do
  case $1 in
    --list-shards) shard_names; exit 0;;
    --units) units; exit $?;;
    --shard-cases) [[ " $(shard_names | tr '\n' ' ') " == *" ${2-} "* ]] || { echo "run.sh: 組は次のどれか: $(shard_names | tr '\n' ' ')" >&2; exit 2; }
                   shard_units "$2" | tr ' ' '\n'; exit 0;;
    --shard) [[ " $(shard_names | tr '\n' ' ') " == *" ${2-} "* ]] || { echo "run.sh: --shard は次のどれか: $(shard_names | tr '\n' ' ')" >&2; exit 2; }
             shard=$2; shift 2;;
    --timings) timings=1; shift;;
    -j) [[ ${2-} =~ ^[1-9][0-9]*$ ]] || { echo "run.sh: -j は 1 以上の整数" >&2; exit 2; }; jobs=$2; shift 2;;
    [0-9][0-9]_*.sh) [[ -f $HERE/cases/$1 ]] || { echo "run.sh: ケースがありません: $1" >&2; exit 2; }; sel+=("$1"); shift;;
    *) echo "usage: run.sh [-j N] [--shard KofN | --list-shards | --shard-cases KofN | --units | --timings] [ケース ...]" >&2; exit 2;;
  esac
done

mapfile -t ulist < <(if [[ -n $shard ]]; then shard_units "$shard"; else units | by_weight; fi) || exit 2
if ((${#sel[@]})); then  # 指定したケースを含むユニットだけ
  mapfile -t ulist < <(printf '%s\n' "${ulist[@]}" | grep -wF -f <(printf '%s\n' "${sel[@]}"))
fi
((${#ulist[@]})) || { echo "run.sh: 流すケースがありません" >&2; exit 2; }

# ---------- ユニットを並列に流す。出力はユニットごとのファイルにためて、終わった順ではなく一覧の順に出す
out=$(mktemp -d); trap 'rm -rf "$out"' EXIT
[[ -n $timings ]] && export TB_TEST_TIMES=$out/times
running=0
for i in "${!ulist[@]}"; do
  # shellcheck disable=SC2086
  ( bash "$HERE/run.sh" --unit ${ulist[i]} >"$out/$i.log" 2>&1; echo $? >"$out/$i.rc" ) &
  running=$((running + 1))
  if ((running >= jobs)); then wait -n; running=$((running - 1)); fi
done
wait

pass=0 fail=0 bad=()
for i in "${!ulist[@]}"; do
  echo "=== ${ulist[i]}"
  grep -v '^pass=[0-9]* fail=[0-9]*$' "$out/$i.log" | sed '${/^$/d}'
  last=$(grep -E '^pass=[0-9]+ fail=[0-9]+$' "$out/$i.log" | tail -n 1)
  if [[ -z $last ]]; then bad+=("${ulist[i]}"); echo "FAIL (ユニットが最後まで流れなかった: 終了コード $(cat "$out/$i.rc"))"; continue; fi
  p=${last#pass=}; p=${p%% *}; f=${last##*fail=}
  pass=$((pass + p)); fail=$((fail + f))
  [[ $(cat "$out/$i.rc") == 0 ]] || bad+=("${ulist[i]}")
done
if [[ -n $timings ]]; then
  { echo "# ケースごとの秒数(run.sh --timings が書く)。--shard の組分けと、流す順(重い順)に使う。無いケースは 10 秒とみなす"
    awk '$1 != "'"$SWEEP"'" { printf "%s %d\n", $1, $3 - $2 + 0.999 }' "$out/times" | sort; } >"$HERE/weights"
fi
echo; ((${#bad[@]})) && echo "失敗したユニット: ${bad[*]}"
echo "pass=$pass fail=$fail"; [[ $fail -eq 0 && ${#bad[@]} -eq 0 ]]
