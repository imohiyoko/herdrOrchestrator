# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- 会話の学習(#97)と段階的な自動化(#98)。組と台帳は checkout の外の置き場だけに書く
_saved_pane=${HERDR_PANE_ID-}; unset HERDR_PANE_ID
_saved_db=$TB_DB; export TB_DB=$TMP/conv.db; rm -f "$TB_DB"
export TB_CONV_STATE_DIR=$TMP/convstate TB_CONV_DIR=$TMP/conversation
export TB_CONV_LEARN=$TMP/conv-learn.conf TB_CONV_STAGES=$TMP/conv-stages.conf
mkdir -p "$TB_CONV_DIR/kinds/thanks" "$TB_CONV_DIR/kinds/refusal" "$TB_CONV_DIR/kinds/reminder"
printf 'threshold = 3\ndeny = Acme, 山田\n' >"$TB_CONV_LEARN"
printf 'high = refusal\ndraft-save = unverified\nstage3 =\nmin-pairs.1 = 2\nwindow-days = 28\nmin-samples.2 = 2\nmax-edit-ratio.2 = 0.9\nmin-adopted.2 = 1\nquiet-days.2 = 0\nquiet-days.3 = 10\n' >"$TB_CONV_STAGES"
CW=$TMP/convwork; mkdir -p "$CW"
printf 'ありがとうございます。確認しました。\n' >"$CW/d1"; printf '確認しました。ありがとうございます。\n' >"$CW/f1"
: >"$CW/empty"
padd() {  # $1=kind $2=draft $3=final $4=situation → pair-id
  $TB conv-pair-add --kind "$1" --draft-file "$2" --final-file "$3" --situation "$4" --intent "受け取ったと伝える" --posted | cut -f1
}

# --- 組の記録
eq "conv-pair-add: --posted が無ければ記録しない" "$(rc $TB conv-pair-add --kind thanks --draft-file "$CW/d1" --final-file "$CW/f1" --situation s --intent i)" "2"
eq "conv-pair-add: 未登録の種別は断る" "$(rc $TB conv-pair-add --kind nope --draft-file "$CW/d1" --final-file "$CW/f1" --situation s --intent i --posted)" "2"
eq "conv-pair-add: agent の pane からは記録しない" "$(HERDR_PANE_ID=wW:p1 rc $TB conv-pair-add --kind thanks --draft-file "$CW/d1" --final-file "$CW/f1" --situation s --intent i --posted)" "1"
p1=$(padd thanks "$CW/d1" "$CW/f1" "山田さんから 2026-11-19 に PR 123 のレビュー https://example.com/x")
eq "conv-pair-add: pair-<日時>-<16進> の ID" "$([[ $p1 =~ ^pair-[0-9]{14}-[0-9a-f]{4}$ ]] && echo y)" "y"
pd=$TB_CONV_STATE_DIR/pairs/thanks/$p1
eq "conv-pair-add: 置き場は 700、本文は 600" "$(stat -c %a "$TB_CONV_STATE_DIR" 2>/dev/null || stat -f %Lp "$TB_CONV_STATE_DIR") $(stat -c %a "$pd/final.txt" 2>/dev/null || stat -f %Lp "$pd/final.txt")" "700 600"
eq "conv-pair-add: 状況メモは名前・番号・URL を消し、日付は年月まで" "$(jq -r .situation "$pd/meta.json")" "<name>さんから 2026-11 に <ref> のレビュー <url>"
eq "conv-pair-add: 編集の割合を測る" "$(jq -r '.edit_ratio > 0 and .edit_ratio < 1' "$pd/meta.json")" "true"
eq "conv-pair-add: DB には何も書かない" "$(sqlite3 "$TB_DB" "SELECT count(*) FROM config;" 2>/dev/null || echo 0)" "0"
eq "conv-pairs: 一覧に本文は出さない" "$($TB conv-pairs | grep -c 'ありがとう')" "0"
bad_root=$(cd "$HERE/../.." && pwd)/convstate-test
eq "conv-pair-add: checkout の中には置かない" "$(TB_CONV_STATE_DIR=$bad_root rc $TB conv-pair-add --kind thanks --draft-file "$CW/d1" --final-file "$CW/f1" --situation s --intent i --posted)" "1"
rm -rf "$bad_root"

# --- 候補: 1 回の手直しは候補止まり、agent の証拠は数えない
cadd() { $TB conv-candidate-add --kind thanks --direction conclusion-first --desc "結論を先に置く" --conclusion first "$@"; }
eq "conv-candidate-add: human-edit は組が要る" "$(rc cadd --evidence human-edit)" "2"
eq "conv-candidate-add: 1 件目は candidate" "$(cadd --evidence human-edit --pair "$p1" | cut -f2,3)" "candidate	1/3"
eq "conv-candidate-add: 同じ組をもう一度足しても数は増えない" "$(cadd --evidence human-edit --pair "$p1" 2>/dev/null | cut -f3)" "1/3"
for i in 1 2 3 4; do cadd --evidence agent-generated --ref "声の役割の指摘 $i" >/dev/null; done
eq "conv-candidate-add: agent-generated は数えない" "$($TB conv-ledger --kind thanks --json | jq -r '.[0] | "\(.counted) \(.evidence["agent-generated"]) \(.state)"')" "1 4 candidate"
eq "conv-candidate-adopt: 1 回の手直しでは採用しない" "$(rc $TB conv-candidate-adopt thanks conclusion-first --situation 受け取りの連絡 --intent 受け取ったと伝える)" "1"
p1b=$(padd thanks "$CW/d1" "$CW/f1" "山田さんから 2026-11-19 に PR 123 のレビュー https://example.com/x")
cadd --evidence human-edit --pair "$p1b" >/dev/null
eq "conv-candidate-add: 同じ状況の組は 1 回と数える" "$($TB conv-ledger --kind thanks --json | jq -r '.[0].counted')" "1"
p0=$(padd thanks "$CW/d1" "$CW/d1" "下書きのまま送った")
eq "conv-candidate-add: 手直しの無い組は human-edit にならない" "$(rc cadd --evidence human-edit --pair "$p0")" "1"
pw=$(padd thanks "$CW/empty" "$CW/f1" "一から書いた")
eq "conv-candidate-add: 一から書いた組は human-written" "$(rc cadd --evidence human-edit --pair "$pw") $(rc cadd --evidence human-written --pair "$pw")" "1 0"
p2=$(padd thanks "$CW/d1" "$CW/f1" "依頼の完了の連絡")
eq "conv-candidate-add: 別々の状況で 3 件そろうと proposed" "$(cadd --evidence human-edit --pair "$p2" | cut -f2,3)" "proposed	3/3"
eq "conv-candidate-add: 説明の名前・数字は台帳に残さない" "$($TB conv-candidate-add --kind thanks --direction shorter --desc "Acme の 3 件をまとめて山田に返す" --evidence human-edit --pair "$p1" >/dev/null; $TB conv-ledger --kind thanks --json | jq -r '.[] | select(.direction=="shorter") .desc')" "<name> の <n> 件をまとめて<name>に返す"

# --- 採用: 人だけ、検査を通したものだけ、patterns.conf の 8 列
eq "conv-candidate-adopt: agent の pane からは採用しない" "$(HERDR_PANE_ID=wW:p1 rc $TB conv-candidate-adopt thanks conclusion-first --situation 受け取りの連絡 --intent 受け取ったと伝える)" "1"
eq "conv-candidate-adopt: 数字が残っていれば採用しない" "$(rc $TB conv-candidate-adopt thanks conclusion-first --situation '受け取り 3 件' --intent 受け取ったと伝える)" "1"
eq "conv-candidate-adopt: 名前の疑いがあれば採用しない" "$(rc $TB conv-candidate-adopt thanks conclusion-first --situation 'Tanaka への受け取り' --intent 受け取ったと伝える)" "1"
eq "conv-candidate-adopt: 状況が空なら採用しない" "$(rc $TB conv-candidate-adopt thanks conclusion-first --intent 受け取ったと伝える)" "1"
$TB conv-candidate-adopt thanks conclusion-first --situation 受け取りの連絡 --intent 受け取ったと伝える --ending 短いお礼 --avoid 長い前置き >/dev/null
eq "conv-candidate-adopt: patterns.conf に adopted で 8 列" "$(cat "$TB_CONV_DIR/kinds/thanks/patterns.conf")" "P-conclusionfirst | adopted | human-edit | 受け取りの連絡 | 受け取ったと伝える | 短いお礼 | first | 長い前置き"
eq "conv-ledger: 採用済み" "$($TB conv-ledger --kind thanks --json | jq -r '.[] | select(.direction=="conclusion-first") | "\(.state) \(.pattern_id)"')" "adopted P-conclusionfirst"
eq "conv-candidate-adopt: 二度は採用しない" "$(rc $TB conv-candidate-adopt thanks conclusion-first)" "1"

# 許す方向の変更は明示の承認が要る
for p in "$p1" "$p2" "$pw"; do
  ev=human-edit; [[ $p == "$pw" ]] && ev=human-written
  $TB conv-candidate-add --kind thanks --direction allow-casual --desc "くだけた言い方を許す" --evidence $ev --pair "$p" --permissive --conclusion last >/dev/null
done
eq "conv-candidate-adopt: 許す方向は --approve-permissive が無ければ断る" "$(rc $TB conv-candidate-adopt thanks allow-casual --situation 親しい相手 --intent 受け取ったと伝える)" "1"
eq "conv-candidate-adopt: --approve-permissive なら採用" "$(rc $TB conv-candidate-adopt thanks allow-casual --situation 親しい相手 --intent 受け取ったと伝える --approve-permissive)" "0"

eq "conv-scan: URL・メール・番号を見つける" "$(printf 'see https://x.y a@b.co #12' | $TB conv-scan | cut -f1 | sort -u | tr '\n' ' ')" "email number ref url "
eq "conv-scan: きれいな文は 0" "$(printf '結論を先に置く' | rc $TB conv-scan)" "0"

# --- 段階
eq "conv-stage: 記録が無ければ段階 0" "$($TB conv-stage thanks --json | jq -r '.[0].stage')" "0"
eq "conv-gate: 段階 0 は review を断る" "$(rc $TB conv-gate thanks --action review)" "4"
eq "conv-gate: draft はいつでも allow" "$($TB conv-gate thanks --action draft | head -1)" "allow"
eq "conv-stage-set: 2 段上げは断る" "$(rc $TB conv-stage-set thanks 2 --reason x)" "1"
eq "conv-stage-set: 理由が無ければ断る" "$(rc $TB conv-stage-set thanks 1)" "2"
eq "conv-stage-set: agent の pane からは断る" "$(HERDR_PANE_ID=wW:p1 rc $TB conv-stage-set thanks 1 --reason x)" "1"
eq "conv-stage-set: 条件を満たせば段階 1" "$($TB conv-stage-set thanks 1 --reason 組がそろった)" "thanks: 段階 0 → 1 (raise)"
eq "conv-stage-log: 測った値と一緒に記録" "$($TB conv-stage-log thanks --json | jq -r '.[0] | "\(.how) \(.by) \(.metrics.pairs)"')" "raise human 5"
eq "conv-gate: 段階 1 は review を allow" "$(rc $TB conv-gate thanks --action review)" "0"
eq "conv-stage-set: 下書き保存を確かめていなければ段階 2 にしない(--override でも)" "$(rc $TB conv-stage-set thanks 2 --reason x --override)" "4"
sed -i.bak 's/^draft-save = .*/draft-save = available/' "$TB_CONV_STAGES"
eq "conv-stage: 次の段階の条件を出す" "$($TB conv-stage thanks --json | jq -r '.[0].next.ok')" "true"
eq "conv-stage-set: 段階 2" "$(rc $TB conv-stage-set thanks 2 --reason 編集が少ない)" "0"
eq "conv-gate: 情報の流れを渡さなければ保存しない" "$(rc $TB conv-gate thanks --action save-draft)" "4"
eq "conv-gate: 狭い範囲の情報を広い範囲へは出さない" "$(rc $TB conv-gate thanks --action save-draft --source-vis team --dest-vis org)" "4"
eq "conv-gate: 段階 2 は下書き保存を allow" "$(rc $TB conv-gate thanks --action save-draft --source-vis org --dest-vis team)" "0"
eq "conv-gate: 段階 2 では送らない" "$(rc $TB conv-gate thanks --action post --counterpart teammate --source-vis org --dest-vis team)" "4"
eq "conv-gate: severity high は段階 1 まで" "$(rc $TB conv-gate thanks --action save-draft --severity high --source-vis org --dest-vis team)" "4"
eq "conv-gate: 組織の外へは段階 1 まで" "$(rc $TB conv-gate thanks --action save-draft --source-vis external --dest-vis external)" "4"
eq "conv-stage-set: stage3 の一覧に無ければ段階 3 にしない" "$(rc $TB conv-stage-set thanks 3 --reason x --override)" "4"
sed -i.bak 's/^stage3 = .*/stage3 = thanks:teammate/' "$TB_CONV_STAGES"
eq "conv-stage-set: 段階 2 の日数が足りなければ上げない" "$(rc $TB conv-stage-set thanks 3 --reason x)" "4"
eq "conv-stage-set: --override なら記録して上げる" "$($TB conv-stage-set thanks 3 --reason 人が判断 --override)" "thanks: 段階 2 → 3 (override)"
eq "conv-gate: 段階 3 でも post は ask(1 通ごとの承認)" "$(rc $TB conv-gate thanks --action post --counterpart teammate --source-vis org --dest-vis team) $($TB conv-gate thanks --action post --counterpart teammate --source-vis org --dest-vis team | head -1)" "20 ask"
eq "conv-gate: 一覧に無い相手には送らない" "$(rc $TB conv-gate thanks --action post --counterpart customer --source-vis org --dest-vis team)" "4"
sed -i.bak 's/^stage3 = .*/stage3 =/' "$TB_CONV_STAGES"
eq "conv-gate: 一覧から消せば段階 2 の扱い(送らない・保存はできる)" "$(rc $TB conv-gate thanks --action post --counterpart teammate --source-vis org --dest-vis team) $(rc $TB conv-gate thanks --action save-draft --source-vis org --dest-vis team)" "4 0"

# high の種別は設定で段階 1 より上にしない
pr1=$(padd refusal "$CW/d1" "$CW/f1" "断り一"); pr2=$(padd refusal "$CW/d1" "$CW/f1" "断り二")
$TB conv-stage-set refusal 1 --reason x >/dev/null
eq "conv-stage-set: high の種別は --override でも段階 2 にしない" "$(rc $TB conv-stage-set refusal 2 --reason x --override)" "4"
eq "conv-stage: high の種別の上限は 1" "$($TB conv-stage refusal --json | jq -r '.[0] | "\(.max_stage) \(.next)"')" "1 null"

# 事故の記録と、1 回の操作で戻す
$TB conv-incident thanks --note "誤って保存した" >/dev/null
eq "conv-incident: 事故の無い日数が 0 に戻る" "$($TB conv-stage thanks --json | jq -r '.[0].quiet_days')" "0"
eq "conv-stage-reset: すべてを 1 回で戻す" "$($TB conv-stage-reset --reason 見直し | sort | tr '\n' '|')" "thanks: 段階 3 → 1|"
eq "conv-stage-reset: データは消えない" "$($TB conv-pairs | wc -l | tr -d ' ') $($TB conv-stage-log --json | jq length)" "7 5"
eq "conv-stage-set: 下げるのは条件なしで 1 回" "$($TB conv-stage-set thanks 0 --reason 止める)" "thanks: 段階 1 → 0 (lower)"

# 学習の記録を消しても、採用済みのパターンと段階の記録は残る(下書きは続く)
eq "conv-learn-purge: --yes が無ければ消さない" "$(rc $TB conv-learn-purge)" "2"
$TB conv-learn-purge --yes >/dev/null
eq "conv-learn-purge: 組と台帳は消え、patterns.conf と段階の記録は残る" "$($TB conv-pairs | wc -l | tr -d ' ') $(grep -c adopted "$TB_CONV_DIR/kinds/thanks/patterns.conf") $($TB conv-stage thanks --json | jq -r '.[0].stage')" "0 2 0"

unset TB_CONV_STATE_DIR TB_CONV_DIR TB_CONV_LEARN TB_CONV_STAGES
export TB_DB=$_saved_db
if [[ -n $_saved_pane ]]; then export HERDR_PANE_ID=$_saved_pane; fi
