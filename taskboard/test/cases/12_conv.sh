# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- 会話チーム(#95)と振り分け表(#96): lib/conv.sh
_saved_pane=${HERDR_PANE_ID-}; unset HERDR_PANE_ID
_saved_cd=${TB_CONV_DIR-}; _saved_cr=${TB_CONV_ROUTES-}; _saved_db=$TB_DB
export TB_DB=$TMP/conv.db; rm -f "$TB_DB"
CV=$TMP/conv; mkdir -p "$CV"
export TB_CONV_ROUTES=$CV/routes.conf   # 振り分け表はファイルで明示(DB の行は下で別に確かめる)
EX=$HERE/../config/conv-routes.conf.example
R5=(--event inquiry --counterpart teammate --depth new --severity low --deadline none)

eq "conv-lint: 同梱のファイルは正しい(表なし)" "$(rc "$TB" conv-lint)" "0"
eq "conv-kinds: 初期の 5 種別にチームがある" "$("$TB" conv-kinds | cut -f1,2 | tr '\t\n' ': ')" \
  "inquiry-answer:team=yes refusal:team=yes reminder:team=yes review-reply:team=yes thanks:team=yes "

# ---------- 振り分け表 ----------
eq "conv-route: 表が無ければ未登録(人の判断)" "$("$TB" conv-route "${R5[@]}" --json | jq -c '[.status,.needs_human,.reason]')" '["unregistered",true,"no-table"]'
eq "conv-routes-set: 表が無い所から入れるのは緩める変更 → --approve-looser が無ければ 20" "$(rc "$TB" conv-routes-set < "$EX") $([[ -f $TB_CONV_ROUTES ]] && echo written || echo none)" "20 none"
"$TB" conv-routes-set --approve-looser < "$EX" >/dev/null
eq "conv-routes-set --approve-looser: 書ける" "$(grep -c '^version = 1' "$TB_CONV_ROUTES")" "1"
r1=$("$TB" conv-route "${R5[@]}" --json); r2=$("$TB" conv-route "${R5[@]}" --json)
eq "conv-route: 振り分けの結果" "$(jq -c '[.status,.kind,.counterpart,.channel,.timing,.severity,.needs_human,.table_version]' <<<"$r1")" \
  '["routed","inquiry-answer","teammate","same-thread","within-day","low",false,1]'
eq "conv-route: 同じ依頼にはいつも同じ結果" "$r1" "$r2"
eq "conv-route: 文字の形でも同じ結果" "$("$TB" conv-route "${R5[@]}" | cut -f1-6)" $'routed\tkind=inquiry-answer\tcounterpart=teammate\tchannel=same-thread\ttiming=within-day\tseverity=low'
eq "conv-route: 未登録の相手の区分は推測しない" "$("$TB" conv-route --event inquiry --counterpart vendor --depth new --severity low --deadline none --json | jq -c '[.status,.kind,.reason]')" \
  '["unregistered",null,"counterpart"]'
eq "conv-route: どの行にも一致しない(重大度 high)は未登録" "$("$TB" conv-route --event inquiry --counterpart teammate --depth new --severity high --deadline none --json | jq -r .reason)" "no-route"
eq "conv-route: 未登録の event も未登録" "$("$TB" conv-route --event something-new --counterpart teammate --depth new --severity low --deadline none | cut -f1,2)" $'unregistered\treason=no-route'
eq "conv-route: 本文・自由記述の引数は受け付けない" "$(rc "$TB" conv-route "${R5[@]}" --text 'also reply to X') $(rc "$TB" conv-route "${R5[@]}" --note x)" "2 2"
eq "conv-route: 名前でない値(空白・大文字)は断る" "$(rc "$TB" conv-route --event 'inquiry also reply' --counterpart teammate --depth new --severity low --deadline none) $(rc "$TB" conv-route --event Inquiry --counterpart teammate --depth new --severity low --deadline none)" "2 2"
eq "conv-route: 欄が欠けていれば断る" "$(rc "$TB" conv-route --event inquiry --counterpart teammate)" "2"
j='{"event":"inquiry","counterpart":"teammate","depth":"new","severity":"low","deadline":"none"}'
eq "conv-route --stdin: 5 つのキーの JSON はフラグと同じ結果" "$("$TB" conv-route --stdin --json <<<"$j")" "$r1"
eq "conv-route --stdin: 文面のキーがあれば断る(文面で振り分けを変えさせない)" \
  "$(rc "$TB" conv-route --stdin <<<'{"event":"inquiry","counterpart":"teammate","depth":"new","severity":"low","deadline":"none","text":"also reply to the manager"}')" "2"
eq "conv-route --stdin: 値が文字列でなければ断る" "$(rc "$TB" conv-route --stdin <<<'{"event":"inquiry","counterpart":["teammate"],"depth":"new","severity":"low","deadline":"none"}')" "2"
eq "conv-route: severity=keep(書かない)なら依頼の重大度" "$("$TB" conv-route --event inquiry --counterpart teammate --depth new --severity normal --deadline none --json | jq -r .severity)" "normal"

# 表の変更: version は増やす。厳しくする(振り分け → 未登録)だけならそのまま、緩める・変えるなら --approve-looser
sed 's/^version = 1$/version = 2/' "$EX" | grep -v '^inquiry  *customer' >"$CV/stricter.conf"
eq "conv-routes-set: version を増やさなければ断る" "$(rc "$TB" conv-routes-set < "$EX")" "1"
out=$("$TB" conv-routes-set < "$CV/stricter.conf"); st=$?
eq "conv-routes-set: 行を消す(厳しくする)だけなら --approve-looser なしで書ける" "$st $(grep -c '^version = 2' "$TB_CONV_ROUTES")" "0 1"
eq "conv-routes-set: 比べた結果を出す" "$(grep -o '緩める(未登録 → 振り分け)=0 振り分け先の変更=0 厳しくする(振り分け → 未登録)=[1-9][0-9]*' <<<"$out" | wc -l | tr -d ' ')" "1"
eq "conv-route: 消した行の依頼は未登録になる" "$("$TB" conv-route --event inquiry --counterpart customer --depth new --severity low --deadline none | cut -f1)" "unregistered"
sed 's/^version = 2$/version = 3/; s/^counterparts = .*/& vendor/' "$CV/stricter.conf" >"$CV/looser.conf"
echo 'inquiry vendor * low * -> kind=inquiry-answer channel=same-thread timing=within-day' >>"$CV/looser.conf"
eq "conv-routes-set: 相手の区分と行を足す(緩める)は 20 で書かない" "$(rc "$TB" conv-routes-set < "$CV/looser.conf") $(grep -c '^version = 2' "$TB_CONV_ROUTES")" "20 1"
eq "conv-routes-set --dry-run: 緩める変更を見るだけ" "$(rc "$TB" conv-routes-set --dry-run --approve-looser < "$CV/looser.conf") $(grep -c '^version = 2' "$TB_CONV_ROUTES")" "0 1"
sed 's/^version = 2$/version = 3/; s/timing=now/timing=within-day/' "$CV/stricter.conf" >"$CV/changed.conf"
eq "conv-routes-set: 振り分け先(時機)を変えるのも --approve-looser が要る" "$(rc "$TB" conv-routes-set < "$CV/changed.conf")" "20"
# 前の行を消すと、後ろの広い行に落ちる: 未登録にはならないので「変更」として扱う
printf 'version = 3\ncounterparts = teammate\ninquiry teammate * * * -> kind=inquiry-answer channel=direct timing=now\n* teammate * * * -> kind=thanks channel=direct timing=now\n' >"$CV/a.conf"
printf 'version = 4\ncounterparts = teammate\n* teammate * * * -> kind=thanks channel=direct timing=now\n' >"$CV/b.conf"
"$TB" conv-routes-set --approve-looser < "$CV/a.conf" >/dev/null
eq "conv-routes-set: 行を消して後ろの行に落ちる変更は「変更」(--approve-looser が要る)" "$(rc "$TB" conv-routes-set < "$CV/b.conf")" "20"
eq "conv-routes-set: 書式の誤りは書かない" "$(printf 'version = 9\ncounterparts = teammate\ninquiry teammate -> kind=x\n' | rc "$TB" conv-routes-set --approve-looser) $(printf 'counterparts = a\n' | rc "$TB" conv-routes-set --approve-looser) $(printf 'version = 9\nx y z w v -> kind=K channel=c timing=t\n' | rc "$TB" conv-routes-set --approve-looser)" "1 1 1"
eq "conv-routes-set: agent の pane からは断る" "$(FAKE_AGENTS='[{"pane_id":"wK:p1","name":"wk"}]' HERDR_PANE_ID=wK:p1 rc "$TB" conv-routes-set --approve-looser < "$CV/looser.conf")" "1"

# 振り分け表を DB に(設定 conv-routes)。tb config set/edit は断り、tb conv-routes-set の確かめを通す
unset TB_CONV_ROUTES
eq "config: conv-routes のキーがある(既定は無し)" "$("$TB" config list | awk '$1=="conv-routes"{print $2}')" "none"
eq "config set conv-routes: 断る(conv-routes-set を使う)" "$(rc "$TB" config set conv-routes < "$EX")" "1"
"$TB" conv-routes-set --approve-looser < "$EX" >/dev/null
eq "conv-routes-set: DB のこのセッションの行に書き、履歴が残る" "$("$TB" config list | awk '$1=="conv-routes"{print $2}' | cut -c1-3) $("$TB" config history conv-routes | cut -f3 | head -1)" "db: conv-routes-set:looser-approved"
eq "conv-route: DB の表で振り分ける" "$("$TB" conv-route "${R5[@]}" | cut -f2)" "kind=inquiry-answer"
"$TB" config unset conv-routes >/dev/null
eq "config unset conv-routes: 表を外すと、すべて未登録に戻る" "$("$TB" conv-route "${R5[@]}" | cut -f1,2)" $'unregistered\treason=no-table'
export TB_CONV_ROUTES=$CV/routes.conf
"$TB" conv-route "${R5[@]}" --json >"$CV/route.json"

# ---------- 下書きの指示 ----------
d=$("$TB" conv-draft --route "$CV/route.json" --note '急ぎではない仕様の問い合わせ' --ref slack:thread-ref --max-chars 120)
eq "conv-draft: チームの指示・共通の土台・依頼の区分が入る" \
  "$(grep -c '^チーム: inquiry-answer$' <<<"$d") $(grep -c '^# 共通の土台' <<<"$d") $(grep -c '^# チーム: 問い合わせへの回答' <<<"$d") $(grep -c '相手の区分: teammate / 経路の区分: direct / 時機: now' <<<"$d") $(grep -c '120 文字以内' <<<"$d") $(grep -c '要: 人の判断' <<<"$d")" \
  "1 1 1 1 1 0"
"$TB" conv-route --event inquiry --counterpart vendor --depth new --severity low --deadline none --json >"$CV/unreg.json"
d=$("$TB" conv-draft --route "$CV/unreg.json")
eq "conv-draft: 未登録の依頼は共通の土台だけで、人の判断と書く" "$(grep -c '^チーム: なし。共通の土台だけ' <<<"$d") $(grep -c '^要: 人の判断' <<<"$d") $(grep -c '^# 共通の土台' <<<"$d") $(grep -c '^## チーム' <<<"$d")" "1 1 1 0"
d=$("$TB" conv-draft --kind no-such-kind)
eq "conv-draft: チームの無い種別は共通の土台だけで、そう書く" "$(grep -c 'チーム: なし。共通の土台だけで下書きします — 種別 no-such-kind のチームはありません' <<<"$d") $(grep -c '^## チーム' <<<"$d")" "1 0"
d=$(TB_CONV_TEAMS=off "$TB" conv-draft --kind thanks)
eq "conv-draft: TB_CONV_TEAMS=off ならチームの層を止める" "$(grep -c 'TB_CONV_TEAMS=off' <<<"$d") $(grep -c '^## チーム' <<<"$d")" "1 0"
eq "conv-draft: --note に数字・URL・@・# は使えない" \
  "$(rc "$TB" conv-draft --kind thanks --note '件数は 3 件') $(rc "$TB" conv-draft --kind thanks --note 'https://example.com を見て') $(rc "$TB" conv-draft --kind thanks --note 'name@example') $(rc "$TB" conv-draft --kind thanks --note 'PR #5 の件') $(rc "$TB" conv-draft --kind thanks --note '受け取りのお礼')" \
  "2 2 2 2 0"
eq "conv-draft: --route と --kind は一緒に使えない・どちらかが要る" "$(rc "$TB" conv-draft --kind thanks --route "$CV/route.json") $(rc "$TB" conv-draft)" "2 2"
eq "conv-draft: --route は conv-route の出力だけ" "$(echo '{"kind":"x"}' | rc "$TB" conv-draft --route -) $(echo '{"status":"routed","kind":"Bad Kind"}' | rc "$TB" conv-draft --route -)" "2 2"

# 種別はファイルを足すだけで増やせる(コードの変更なし)
cp -R "$HERE/../../conversation" "$CV/dir"; export TB_CONV_DIR=$CV/dir
mkdir -p "$TB_CONV_DIR/kinds/apology"
printf '# チーム: お詫び(apology)\n\n- 何について詫びるかを最初に書く\n' >"$TB_CONV_DIR/kinds/apology/prompt.md"
printf 'P-ap1 | adopted | human-edit | 返事が遅れた | 遅れを詫びて答える | 答えで終える | first | 「重ね重ね」\nP-ap2 | candidate | human-edit | 予定の変更 | 変更を伝える | 次の予定で終える | first | -\n' >"$TB_CONV_DIR/kinds/apology/patterns.conf"
eq "conv-kinds: ファイルを足した種別にチームがある" "$("$TB" conv-kinds | grep '^apology' | cut -f2-4 | tr '\t' ' ')" "team=yes adopted=1 candidate=1"
d=$("$TB" conv-draft --kind apology)
eq "conv-draft: 足した種別のチームと採用済みのパターン(候補は渡さない)" "$(grep -c '^# チーム: お詫び' <<<"$d") $(grep -c '^- P-ap1: 場面=返事が遅れた' <<<"$d") $(grep -c 'P-ap2' <<<"$d")" "1 1 0"
eq "conv-patterns: 既定は採用済みだけ、--all で候補も" "$("$TB" conv-patterns apology | cut -f1) $("$TB" conv-patterns apology --all | cut -f1 | tr '\n' ' ')" "P-ap1 P-ap1 P-ap2 "
printf 'P-ap3 | adopted | agent-generated | 返事が遅れた | 詫びる | 答えで終える | first | -\nP-ap4 | adopted | human-edit | 依頼から三日 | 詫びる | 答えで終える | first | -\nP-ap5 | adopted | human-edit | 依頼から3日 | 詫びる | 答えで終える | first | -\nP-ap6 | adopted | human-edit | https://x の件 | 詫びる | 終える | first | -\nP-ap7 | adopted | bot | 遅れ | 詫びる | 終える | first | -\nP-ap8 | adopted | human-edit | 遅れ | 詫びる | 終える |\n' >>"$TB_CONV_DIR/kinds/apology/patterns.conf"
d=$("$TB" conv-draft --kind apology)
eq "conv-draft: agent-generated の adopted・数字・URL・不正な行は渡さない" "$(grep -cE 'P-ap[35678]' <<<"$d") $(grep -c 'P-ap4' <<<"$d")" "0 1"
eq "conv-lint: パターンの誤りを行ごとに出して 1" "$(rc "$TB" conv-lint) $("$TB" conv-lint | grep -c 'kinds/apology/patterns.conf')" "1 5"
rm -f "$TB_CONV_DIR/kinds/apology/prompt.md"
eq "conv-lint: prompt.md の無い種別を知らせる" "$("$TB" conv-lint | grep -c 'kinds/apology: prompt.md がありません')" "1"
eq "conv-draft: prompt.md を外すとチームなし" "$("$TB" conv-draft --kind apology | grep -c '^チーム: なし')" "1"

# 振り分け表と下書きの指示は独立: 片方を変えても、もう片方の結果は変わらない
before_r=$("$TB" conv-route "${R5[@]}" --json); before_d=$("$TB" conv-draft --kind thanks)
echo '- 追加の決まり' >>"$TB_CONV_DIR/kinds/inquiry-answer/prompt.md"
eq "独立: チームの指示を変えても振り分けは変わらない" "$("$TB" conv-route "${R5[@]}" --json)" "$before_r"
sed 's/^version = .*/version = 9/; s/timing=within-day/timing=next-business-day/' "$TB_CONV_ROUTES" | "$TB" conv-routes-set --approve-looser >/dev/null
eq "独立: 振り分け表を変えても下書きの指示は変わらない" "$("$TB" conv-draft --kind thanks)" "$before_d"
unset TB_CONV_DIR

# ---------- 声の役 ----------
printf '依頼の本文。これは長めの依頼の本文で、返事が要る内容です。\n' >"$CV/req.txt"
printf '短い下書き。\n' >"$CV/draft.txt"
printf 'とても長い下書き。とても長い下書き。とても長い下書き。とても長い下書き。とても長い下書き。\n' >"$CV/long.txt"
eq "conv-length: 依頼以下なら 0、長ければ 1" "$(rc "$TB" conv-length --draft "$CV/draft.txt" --request "$CV/req.txt") $(rc "$TB" conv-length --draft "$CV/long.txt" --request "$CV/req.txt")" "0 1"
eq "conv-length: 文字数(日本語はコードポイントで数える)" "$("$TB" conv-length --draft "$CV/draft.txt" --request "$CV/req.txt")" $'draft=6\trequest=29\tlonger=no'
v=$("$TB" conv-voice direct --draft "$CV/draft.txt")
eq "conv-voice: 役の指示と下書きだけ(ほかの役の指示・依頼の本文は入らない)" \
  "$(grep -c '^# 声の役: 率直' <<<"$v") $(grep -c '^短い下書き。$' <<<"$v") $(grep -c '共感\|受け手\|簡潔' <<<"$v") $(grep -c '依頼の本文' <<<"$v")" "1 1 0 0"
eq "conv-voice brevity: 依頼の長さが要る" "$(rc "$TB" conv-voice brevity --draft "$CV/draft.txt")" "2"
eq "conv-voice brevity: 長さの判定を tb が数えて渡す" "$("$TB" conv-voice brevity --draft "$CV/long.txt" --request "$CV/req.txt" | grep -c '「長さ: 依頼より長い」と書く')" "1"
eq "conv-voice: 無い役は断る" "$(rc "$TB" conv-voice loud --draft "$CV/draft.txt")" "2"
eq "conv-voice-check: 3 点まで" "$(printf -- '- a\n- b\n- c\n' | rc "$TB" conv-voice-check direct) $(printf -- '- a\n- b\n- c\n- d\n' | rc "$TB" conv-voice-check direct)" "0 1"
eq "conv-voice-check: 点が無い・行頭が違うものは断る" "$(printf '\n' | rc "$TB" conv-voice-check receiver) $(printf 'a\n' | rc "$TB" conv-voice-check receiver) $(printf -- '- なし\n' | rc "$TB" conv-voice-check receiver)" "1 1 0"
eq "conv-voice-check brevity: 長さの判定が必ず要る" "$(printf -- '- 挨拶を削る\n' | rc "$TB" conv-voice-check brevity) $(printf -- '長さ: 依頼より長くない\n- 挨拶を削る\n' | rc "$TB" conv-voice-check brevity)" "1 0"
eq "conv-voice-check brevity: 実際の長さと違えば断る" \
  "$(printf -- '長さ: 依頼より長くない\n- x\n' | rc "$TB" conv-voice-check brevity --draft "$CV/long.txt" --request "$CV/req.txt") $(printf -- '長さ: 依頼より長い\n- x\n' | rc "$TB" conv-voice-check brevity --draft "$CV/long.txt" --request "$CV/req.txt")" "1 0"
eq "conv-voice-check: 整えた出力" "$(printf -- '長さ: 依頼より長い\n- x\n・ y\n' | "$TB" conv-voice-check brevity | tr '\n' '|')" "長さ: 依頼より長い|- x|- y|"

# ---------- 文面は DB・記録に残らない ----------
canary=CANARYBODY$$
printf '%s 依頼の本文\n' "$canary" >"$CV/req2.txt"; printf '%s 下書き\n' "$canary" >"$CV/draft2.txt"
"$TB" conv-length --draft "$CV/draft2.txt" --request "$CV/req2.txt" >/dev/null 2>&1
for vv in direct empathetic receiver brevity; do "$TB" conv-voice "$vv" --draft "$CV/draft2.txt" --request "$CV/req2.txt" >/dev/null 2>&1; done
printf -- '- %s\n' "$canary" | "$TB" conv-voice-check direct >/dev/null 2>&1
"$TB" conv-draft --route "$CV/route.json" --request "$CV/req2.txt" >/dev/null 2>&1
"$TB" conv-route "${R5[@]}" --text "$canary" >/dev/null 2>&1
eq "文面は DB にも tb の記録にも書かない" "$(grep -rlF "$canary" "$TMP" 2>/dev/null | grep -vE '/conv/(req2|draft2)\.txt$' | wc -l | tr -d ' ')" "0"

export TB_DB=$_saved_db
if [[ -n $_saved_cd ]]; then export TB_CONV_DIR=$_saved_cd; else unset TB_CONV_DIR; fi
if [[ -n $_saved_cr ]]; then export TB_CONV_ROUTES=$_saved_cr; else unset TB_CONV_ROUTES; fi
[[ -z $_saved_pane ]] || export HERDR_PANE_ID=$_saved_pane
