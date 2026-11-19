# conversation — 会話チーム(下書きの層)と振り分け表

人が読んで直してから送る返事(レビュー依頼への返事・問い合わせへの回答・催促・お礼・お断りなど)の**下書き**を作るための置き場です。
送るのは人です。ここにあるものは送信・承認・状態の変更をしません(issue #95 / #96)。

2 つの層に分けています。間で受け渡すのは区分の名前・参照・一般化した説明だけで、文面は渡しません。

| 層 | 決めること | 入力 | 出力 | 持ち主 |
|---|---|---|---|---|
| 振り分け(`tb conv-route`) | 誰に・いつ・どこで・どの種別か | 構造化した依頼(event / counterpart / depth / severity / deadline の名前だけ) | `{kind, counterpart, channel, timing, severity}` か「未登録(人の判断)」 | 設定 `conv-routes`(人が書く決定論の表) |
| 下書き(`tb conv-draft`) | 何を、どう言うか | 振り分けの結果 + 一般化した状況(`--note`)+ 参照(`--ref`) | 下書きの指示(共通の土台 + 種別のチーム + 採用済みのパターン) | このディレクトリのファイル |

振り分け表は DB の設定、チームの指示はこのディレクトリのファイルなので、片方を変えてももう片方は変わりません。

## ファイル

| ファイル | 中身 |
|---|---|
| `_base.md` | 共通の土台。すべての種別の下書きで使う。チームが無い種別・未登録の依頼は、これだけで下書きする |
| `voices/<役>.md` | 声の役(`direct` 率直 / `empathetic` 共感 / `receiver` 受け手 / `brevity` 簡潔)。下書きを読み、1〜3 点を返す |
| `kinds/<種別>/prompt.md` | 種別ごとのチームの指示と見る点。**このファイルがあればチームあり** |
| `kinds/<種別>/patterns.conf` | 種別ごとのパターンの台帳 |

初期の種別: `inquiry-answer`(問い合わせへの回答)/ `review-reply`(レビュー依頼への返事)/ `reminder`(催促)/ `thanks`(お礼・受け取り)/ `refusal`(お断り)。

### 種別を足す

`kinds/<種別>/prompt.md`(と、あれば `patterns.conf`)を置くだけです。コードは変えません。種別の名前は英小文字で始まる 40 文字までの英小文字・数字・`-`。
振り分け表の右辺 `kind=<種別>` で指すと、その種別に振り分けられます。足したら `tb conv-lint` で確かめます。

## パターンの台帳(`patterns.conf`)

1 行 1 パターン、`|` 区切りの 8 列です。`#` で始まる行は注釈。

```
id | state | evidence | situation | intent | ending | conclusion | avoid
P-rm1 | adopted | human-edit | 期限の近い依頼の確認 | してほしいことと期限を伝える | 済んでいれば不要と添える | first | 「至急」「まだですか」
```

| 列 | 値 |
|---|---|
| `id` | `P-<英数字と ->`(種別の中で一意) |
| `state` | `candidate`(候補)/ `adopted`(採用。人だけが付ける) |
| `evidence` | 根拠の種類: `human-edit`(人の直し)/ `human-written`(人が書いた文)/ `other-source`(ほかの資料)/ `agent-generated`(agent が作ったもの) |
| `situation` / `intent` / `ending` | 場面 / 意図 / 終わり方(一般化した説明) |
| `conclusion` | 結論の位置: `first`(先頭)/ `last`(末尾) |
| `avoid` | 避ける言い回し(無ければ `-`) |

- 下書きに渡すのは `adopted` だけ。`agent-generated` を根拠に `adopted` にはできません(自分の癖を強めないため。`tb conv-lint` が弾き、下書きにも渡しません)
- 名前・番号・URL・件数・日付は書きません。数字・URL・`@`・`#` を含む行は `tb conv-lint` が弾き、下書きにも渡しません
- 同梱の行はすべて `candidate | agent-generated` の例です。人が確かめて採用するときは、`state` を `adopted` に、`evidence` を実際の根拠に書き換えます
- 件数(何回同じ直しが出たか)はここには持ちません(下書きと最終文の組の記録 #97 の側で持ちます)

## 振り分け表(設定 `conv-routes`)

例: [`taskboard/config/conv-routes.conf.example`](../taskboard/config/conv-routes.conf.example)。`.example` は既定値になりません(表が無ければ、すべて未登録)。

```
version = 2
counterparts = teammate manager other-team customer
# <event> <counterpart> <depth> <severity> <deadline> -> kind=<種別> channel=<経路の区分> timing=<時機> [severity=<重大度>|keep]
review-request  teammate,manager  *  low,normal  *  -> kind=review-reply  channel=same-thread  timing=within-day
```

- 左辺の各欄は `*`・名前・カンマ区切りの名前のどれか。**完全一致だけ**で比べ、上から見て最初に一致した行で決まります
- `counterparts` に無い相手の区分、どの行にも一致しない依頼は**推測しません**。結果は `unregistered`(`needs_human`)で、下書きは共通の土台だけになり「要: 人の判断」と書かれます
- `severity` を書かなければ依頼の重大度をそのまま返します(`keep`)
- 同じ依頼にはいつも同じ結果を返します(時刻・乱数・文面を使わない)。`tb conv-route` は 5 つの区分の名前しか受け付けず、本文や自由記述を渡す引数はありません(`--stdin` の JSON もこの 5 つのキーだけ)

### 表を変える(人だけ)

```bash
tb conv-routes-set --dry-run < conv-routes.conf     # 何が変わるかだけを見る
tb conv-routes-set < conv-routes.conf               # 厳しくするだけ(振り分け → 未登録)ならそのまま書ける
tb conv-routes-set --approve-looser < conv-routes.conf   # 緩める(未登録 → 振り分け)・振り分け先を変える変更
tb config history conv-routes                        # 変更の履歴
tb config unset conv-routes                          # 表を外す(すべて未登録に戻る)
```

- `version` は今の表より大きくします
- tb が今の表と新しい表を、表に出てくる名前の全組み合わせ(+ どれにも一致しない値)で比べます。未登録だった依頼が振り分けられる・振り分け先が変わる組み合わせがあれば「緩める変更」で、`--approve-looser` が無ければ書かずに終了コード 20
- `tb config set/edit conv-routes` は使えません(この確かめを通らないため)

## 下書きの流れ

```bash
tb conv-route --event review-request --counterpart teammate --depth known --severity normal --deadline none --json > route.json
tb conv-draft --route route.json --note '急ぎではないレビュー依頼。今日は見られない' --ref 'slack:<スレッドの参照>' --request request.txt
#   → 下書きを書く(依頼より長くしない)
tb conv-length --draft draft.txt --request request.txt   # 1 なら依頼より長い
for v in direct empathetic receiver brevity; do
  tb conv-voice $v --draft draft.txt --request request.txt   # → 役ごとに別の agent(か別の会話)に渡す。ほかの役の出力は渡さない
done
tb conv-voice-check brevity --draft draft.txt --request request.txt < brevity-out.txt   # 形の確かめ(多くても 3 点・長さの判定)
```

- 声の役には、その役の指示と下書きだけを渡します(`tb conv-voice` の出力)。ある役の言い方がほかの役を引っ張らないように、役ごとに別の会話で読ませます
- 最終の文面は人が決めます
- `--note` は一般化した説明だけ(数字・URL・`@`・`#` は断られます)。元の文面は `--ref` の参照先で読み、写しません
- ここのコマンドは文面を DB・コメント・記録に書きません(読むのはファイルと標準入力、出すのは標準出力だけ)

## 止める(ロールバック)

- `TB_CONV_TEAMS=off`: チームの層を止めます。どの種別も共通の土台だけで下書きします(1 つの汎用の下書き役に戻る)
- `tb config unset conv-routes`(と `--shared`): 振り分け表を外します。すべての依頼が未登録になり、共通の土台だけで下書きされます
- パターンはただのファイルなので、消せば消えます
