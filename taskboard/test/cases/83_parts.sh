# shellcheck shell=bash
# 部品(ADR 0005 の段 1): 部品の一覧・引数の型・リスク階層の計算、人だけの部品がコードに固定されていること、権限カタログとの整合
# DB は $TMP/parts.db だけを使う。設定は環境変数で一時ファイルを明示し、最後に戻す
_saved_db=$TB_DB; _saved_appr=${TB_POLICY_A-}
export TB_DB=$TMP/parts.db

# --- 一覧
out=$($TB parts); r=$?
eq "parts: 一覧を出す" "$r" "0"
eq "parts: 11 件以上ある" "$(($(grep -c . <<<"$out") >= 11))" "1"
eq "parts: 1 行 8 列(タブ区切り)" "$(awk -F'\t' 'NF != 8' <<<"$out" | wc -l | tr -d ' ')" "0"
eq "parts: 名前が重複しない" "$(cut -f1 <<<"$out" | sort | uniq -d)" ""
eq "parts: human-only の部品は人だけ=yes、それ以外は no(リスク階層と食い違わない)" \
   "$(awk -F'\t' '($3 == "human-only") != ($4 == "yes")' <<<"$out" | wc -l | tr -d ' ')" "0"
for p in agent-force-stop comment-resolve pr-queue-approve config-set; do
  eq "parts: $p は人だけ" "$(awk -F'\t' -v p="$p" '$1 == p {print $3 "/" $4}' <<<"$out")" "human-only/yes"
done
eq "parts: 読み取りだけの部品は副作用 none で low" "$(awk -F'\t' '$1 ~ /^(agent-status|wait-idle|task-show)$/ {print $2 "/" $3}' <<<"$out" | sort -u)" "none/low"
js=$($TB parts --json)
eq "parts --json: 1 行 1 件の JSON で件数が同じ" "$(jq -s length <<<"$js")" "$(grep -c . <<<"$out")"
eq "parts --json: human_only は risk から決まる" "$(jq -s '[.[] | select(.human_only != (.risk == "human-only"))] | length' <<<"$js")" "0"
eq "parts --json: agent-restart は stop と revive の 2 つの操作名" "$(jq -r 'select(.name=="agent-restart") | .ops | join(",")' <<<"$js")" 'agent:stop:$target,agent:revive:$target'
eq "parts: 不明な引数は使い方の誤り" "$(rc $TB parts --bogus)" "2"

# --- 引数の型
eq "part-check: 通る" "$($TB part-check wait-idle target=w1 timeout=60)" "$(printf 'part: wait-idle\nrisk: low\nhuman_only: no\nrunner: inside')"
eq "part-check: 操作名の \$引数は値に置き換わる" "$($TB part-check agent-restart target=w1 | grep '^op:')" "$(printf 'op: agent:stop:w1\nop: agent:revive:w1')"
eq "part-check: 一覧にない部品は 1" "$(rc $TB part-check no-such-part)" "1"
eq "part-check: 部品の名前なしは 2" "$(rc $TB part-check)" "2"
eq "part-check: 必須の引数が無いと 2" "$(rc $TB part-check wait-idle target=w1)" "2"
eq "part-check: 省略できる引数(prompt?)は無くてよい" "$(rc $TB part-check agent-revive target=w1)" "0"
eq "part-check: 余分な引数は 2" "$(rc $TB part-check agent-status target=w1 extra=1)" "2"
eq "part-check: 同じ引数が 2 回は 2" "$(rc $TB part-check agent-status target=w1 target=w2)" "2"
eq "part-check: <名前>=<値> でない引数は 2" "$(rc $TB part-check agent-status w1)" "2"
eq "part-check: agent 型は human を受けない" "$(rc $TB part-check agent-status target=human)" "2"
eq "part-check: agent 型は大文字・記号を受けない" "$(rc $TB part-check agent-status target=W1) $(rc $TB part-check agent-status 'target=w1;rm')" "2 2"
eq "part-check: agent 型は空を受けない" "$(rc $TB part-check agent-status target=)" "2"
eq "part-check: int は境界ちょうどを受ける" "$(rc $TB part-check wait-idle target=w1 timeout=1) $(rc $TB part-check wait-idle target=w1 timeout=600)" "0 0"
eq "part-check: int は範囲の外・数字以外を受けない" \
   "$(rc $TB part-check wait-idle target=w1 timeout=0) $(rc $TB part-check wait-idle target=w1 timeout=601) $(rc $TB part-check wait-idle target=w1 timeout=1e2) $(rc $TB part-check wait-idle target=w1 timeout=-1)" "2 2 2 2"
eq "part-check: task 型" "$(rc $TB part-check task-show task=T-tk-001) $(rc $TB part-check task-show task=T-tk-1) $(rc $TB part-check task-show 'task=T-tk-001 x')" "0 2 2"
eq "part-check: comment / prq 型" "$(rc $TB part-check comment-resolve comment=C-3) $(rc $TB part-check comment-resolve comment=3) $(rc $TB part-check pr-queue-approve id=PQ-1) $(rc $TB part-check pr-queue-approve id=O-1)" "0 2 0 2"
eq "part-check: config-key 型は設定のキーだけ" "$(rc $TB part-check config-set key=approvals) $(rc $TB part-check config-set key=nothing) $(rc $TB part-check config-set key=)" "0 2 2"
t2000=$(printf 'あ%.0s' $(seq 2000)); t2001="${t2000}x"
eq "part-check: text は上限の字数ちょうど(多バイト)を受け、超えると 2" "$(rc $TB part-check send-info to=w1 "text=$t2000") $(rc $TB part-check send-info to=w1 "text=$t2001")" "0 2"
eq "part-check: text は改行・タブを受け、ほかの制御文字は受けない" \
   "$(rc $TB part-check send-info to=w1 "text=$(printf 'a\nb\tc')") $(rc $TB part-check send-info to=w1 "text=$(printf 'a\033[2J')") $(rc $TB part-check send-info to=w1 "text=$(printf 'a\rb')")" "0 2 2"
eq "part-check: = を含む値はそのまま(最初の = で分ける)" "$(rc $TB part-check send-info to=w1 'text=a=b')" "0"

# --- リスク階層の計算(最大。並びの中に 1 つでも高いものがあれば高い)
eq "part-risk: low だけなら low" "$($TB part-risk agent-status wait-idle task-show)" "low"
eq "part-risk: low と mid なら mid" "$($TB part-risk agent-status agent-restart wait-idle)" "mid"
eq "part-risk: human-only が 1 つあれば human-only(順序によらない)" "$($TB part-risk config-set agent-status) $($TB part-risk agent-status config-set)" "human-only human-only"
eq "part-risk: 一覧にない部品が混ざれば 1(並びとして使えない)" "$(rc $TB part-risk agent-status no-such-part)" "1"
eq "part-risk: 部品なしは 2" "$(rc $TB part-risk)" "2"

# --- 人だけの部品は、設定・環境変数で緩められない(コードに固定)
printf '* = allow\n' >"$TMP/parts-approvals.conf"
export TB_POLICY_A=$TMP/parts-approvals.conf
eq "parts: approvals を全部 allow にしても config-set は人だけのまま" "$($TB part-check config-set key=approvals | sed -n 's/^human_only: //p')" "yes"
eq "parts: 環境変数 PARTS_TABLE では差し替えられない" \
   "$(PARTS_TABLE='config-set | none | low | inside | - | key:config-key | x' $TB parts | awk -F'\t' '$1 == "config-set" {print $3}')" "human-only"
eq "parts: 環境変数 PART_RISK_LEVELS では順位を変えられない" "$(PART_RISK_LEVELS='human-only high mid low' $TB part-risk config-set agent-status)" "human-only"
eq "parts: 部品の一覧は設定のキーに無い(DB で増やせない)" "$($TB config list 2>/dev/null | grep -cE '^(parts|recipes?)\b')" "0"
eq "parts: 表は internal/recipe/parts.go の中だけにある" "$(grep -rlE --include='*.go' --exclude='*_test.go' '^const Table = ' "$HERE/../../internal" | xargs -n1 basename)" "parts.go"

# --- 権限カタログとの整合: 部品の操作名はカタログの active な項目に当たり、部品のリスク階層はカタログより低くない
_pcat=$HERE/../config/approvals-catalog.conf
_parts_cat_problems() {
  local name risk op ex line pat crisk cstate hit
  while IFS=$'\t' read -r name risk op; do
    [[ -n $op ]] || continue
    ex=${op//\$*/example}
    hit=0
    while IFS= read -r line; do
      IFS='|' read -r pat _ _ crisk _ cstate _ <<<"$line"
      pat=$(trim_ "$pat"); crisk=$(trim_ "$crisk"); cstate=$(trim_ "$cstate")
      # shellcheck disable=SC2053
      [[ $ex == $pat ]] || continue
      hit=1
      [[ $cstate == active ]] || echo "$name: $op はカタログで $cstate"
      (($(rank_ "$risk") >= $(rank_ "$crisk"))) || echo "$name: リスク階層 $risk がカタログの $crisk($pat)より低い"
      break
    done < <(grep -vE '^[[:space:]]*(#|$)' "$_pcat")
    ((hit)) || echo "$name: $op がカタログに無い"
  done < <(jq -r '. as $p | .ops[] | [$p.name, $p.risk, .] | @tsv' <<<"$js")
}
trim_() { local s=$1; s="${s#"${s%%[![:space:]]*}"}"; printf '%s' "${s%"${s##*[![:space:]]}"}"; }
rank_() { case $1 in low) echo 0;; mid) echo 1;; high) echo 2;; *) echo 3;; esac; }
eq "parts: 部品の操作名はカタログの active な項目にあり、リスク階層はカタログより低くない" "$(_parts_cat_problems)" ""
eq "parts: 整合の検査が働く(自己テスト: 低いリスク階層を検出)" \
   "$(js='{"name":"x","risk":"low","ops":["agent:new:$target"]}' _parts_cat_problems)" "x: リスク階層 low がカタログの high(agent:new:*)より低い"
eq "parts: 整合の検査が働く(自己テスト: カタログに無い操作名)" "$(js='{"name":"x","risk":"high","ops":["zzz:op:$t"]}' _parts_cat_problems)" 'x: zzz:op:$t がカタログに無い'

export TB_DB=$_saved_db
if [[ -n $_saved_appr ]]; then export TB_POLICY_A=$_saved_appr; else unset TB_POLICY_A; fi
