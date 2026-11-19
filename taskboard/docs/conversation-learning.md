# 会話チームの学習と段階的な自動化(#97 / #98)

会話チーム(#95)と振り分け表(#96)の上に乗る 2 つの仕組み。下書きを作る側(`lib/conv.sh`、`conversation/`)は、
ここで作るものを `conversation/kinds/<kind>/patterns.conf` の adopted 行としてしか読まない。
なので、学習の記録を丸ごと消しても下書きはそれまでに採用したパターンで続く。

## 1. 下書きと送った文の組(#97)

人が実際に送ったあとに、人が自分の端末で記録する。

```
tb conv-pair-add --kind thanks --draft-file draft.txt --final-file sent.txt \
  --situation "依頼の完了の連絡" --intent "受け取ったと伝える" --posted \
  [--points-offered 3 --points-accepted 1]   # 声の役割の指摘の数と、人が受け入れた数(段階 1 の測定用)
```

- `--posted` が無ければ記録しない。agent の pane からは記録できない(`require_human`)。
- 置き場は DB でも checkout でもなく `$TB_CONV_STATE_DIR`(既定 `~/.local/state/herdr/conv`)。ディレクトリは 700、ファイルは 600。
  checkout の中を指していれば断る(版の管理の下に本文を置かない)。
- `meta.json` に種別・一般化した状況と意図(名前・番号・URL を消し、日付は年月まで)・編集の割合
  (文字単位の編集距離 ÷ 長い方の文字数)を置く。本文は `draft.txt` / `final.txt` にだけある。
- 下書きが空なら「人が一から書いた」組(human_written)。

## 2. 候補の台帳(#97)

人の手直し 1 つを、候補(変更の向き)の証拠 1 つとして足す。説明は「何をどう変えたか」で、本文は写さない。

```
tb conv-candidate-add --kind thanks --direction conclusion-first --desc "結論を先に置く" \
  --evidence human-edit --pair pair-20261119062000-ab12 [--conclusion first --ending ... --avoid ...] [--permissive]
```

| 証拠の種類 | 要るもの | 数えるか |
|---|---|---|
| human-edit | 手直しのある組(編集の割合 > 0、一から書いたものではない) | 数える |
| human-written | 人が一から書いた組 | 数える |
| other-source | `--ref`(出どころの一般化した説明) | 数える |
| agent-generated | なし(agent の下書きや声の役割の指摘) | **数えない** |

- 件数は「別々の状況」の数。同じ状況の組を何度足しても 1。同じ証拠の二重登録は無視する。
- `conv-learn` の `threshold`(既定 3、2 未満は 2)以上で `proposed`。1 回の手直しは `candidate` 止まり。
- 採用(`tb conv-candidate-adopt`)は人だけ、`proposed` だけ。許す方向の変更(`--permissive` が一度でも付いた候補)は
  `--approve-permissive` が要る。
- 採用の前に、状況・意図・結び・避けることを検査する(`convtext.py scan`): URL・メール・@名前・チケットやレビューの番号・数字・
  英字の大文字で始まる語(名前の疑い)・`deny =` の語・長すぎる引用。置き換えの印(`<name>` `<n>` など)が残っていても断る。
  通ったら `patterns.conf` に `P-<向き(20 字まで)> | adopted | <evidence> | situation | intent | ending | conclusion | avoid` の 1 行を足す。
- 台帳に残す文(説明など)は、足すときに sanitize する(名前・番号・URL・日付・数字を印に置き換える)。

置き場:

```
$TB_CONV_STATE_DIR/
  pairs/<kind>/<pair-id>/{draft.txt,final.txt,meta.json}
  ledger/<kind>.tsv            向き 状態 許す方向か 状況 意図 結び 結論 避けること 説明 作成 更新 パターンID
  ledger/<kind>.evidence.tsv   向き 証拠の種類 組かref 状況のキー 日時 誰が
  stage-log.tsv                段階の変更(下)
  incidents.tsv                事故の記録(下)
```

戻し方: `tb conv-learn-purge [--kind K] --yes`(組と台帳を消す。patterns.conf の採用済みの行と段階の記録は残る)。

## 3. 段階(#98)

| 段階 | すること | 上げる条件(conv-stages の既定値) | 戻し方 |
|---|---|---|---|
| 0 | 下書きだけ。人が読み、直し、送る | - | - |
| 1 | 声の役割が短い指摘を返す | 組の数 `min-pairs.1`(20) | `tb conv-stage-set <kind> 0` |
| 2 | 下書きとして保存する(送らない) | `draft-save = available`、直近 `window-days`(28)日の組が `min-samples.2`(20)件以上で編集の割合の平均が `max-edit-ratio.2`(0.15)以下、採用したパターン `min-adopted.2`(3)以上、事故の無い日数 `quiet-days.2`(21) | `tb conv-stage-set <kind> 1` |
| 3 | `stage3 =` の 種別:相手の区分 だけ、1 通ごとに人が 1 回承認すれば送れる | 段階 2 に `quiet-days.3`(42)日以上、その間事故なし、一覧に種別がある | `stage3 =` から消す(段階 2 の扱いに戻る)か `tb conv-stage-set <kind> 2` |

- 段階を変えるのは人だけ(`tb conv-stage-set <kind> <段階> --reason R`)。上げるのは 1 段ずつ。条件を満たさないときは断り、
  人が判断して上げるなら `--override`(そのことも記録する)。変えるたびに、その時の測った値を `stage-log.tsv` に残す(`tb conv-stage-log`)。
- **設定で決まる上限は `--override` でも越えない**: `high =` の種別(問題の指摘・断り・組織の外)は段階 1 まで。
  `draft-save` が `available` でなければ段階 2 にしない(メッセージの道具に下書き保存が無ければ段階 2 は飛ばす)。
  段階 3 は `stage3 =` の一覧にある種別だけ。
- 全部を 1 回で戻す: `tb conv-stage-reset [--to 0|1] --reason R`。データは消さない。
- 事故: `tb conv-incident <kind> --note N`(人だけ)。段階は変えず、事故の無い日数が 0 からになる。

### 判定(`tb conv-gate`)

agent は保存・送信の前に必ず聞く。決まった表だけで判定し、model の判断は入れない。

```
tb conv-gate <kind> --action draft|review|save-draft|post [--counterpart C] [--severity low|normal|high] \
  [--source-vis private|team|org|external --dest-vis private|team|org|external]
```

- 1 行目 `allow|ask|deny`、終了コード 0 / 20 / 4。
- 効く段階 = min(設定の段階, 上限)。上限は `high =` の種別・`--severity high`・`--dest-vis external` のどれかで 1。
- `save-draft` と `post` は情報の流れを確かめる: 送り先を見られる範囲が元の情報を見られる範囲より広ければ deny
  (`--source-vis` / `--dest-vis` が無ければ deny)。
- `post` は段階 3 で一覧に載っていても `ask`(この 1 通を人が承認)。`allow` は返さない。送る・承認する・状態を変えるのは、どの段階でも人。

## 測る値(`tb conv-stage --json`)

`pairs`(組の数)、`window_samples` / `window_edit_ratio`(直近の組の数と編集の割合の平均)、`adopted_patterns`、
`quiet_days`(最後の事故から、無ければ最初の組から)、`days_at_stage`、`role_acceptance`(声の役割の指摘を人が受け入れた割合)。
どれも組の meta と台帳と記録から数え、本文は読まない。
