# 用意したラベル(設定 labels): 設定があれば add / update で付けられるのはそこに書いたものと tb 自身のラベルだけ。project は tkl だけを使う
export TB_LABELS=$TMP/labels.conf
rm -f "$TB_LABELS"
# 設定が無ければ自由に付けられる(今までどおり)
eq "labels-defined: no config → free labels" "$($TB show "$($TB add -p tkl -t free -l anything)" | jq -c .labels)" '["anything"]'
eq "labels-defined: --defined without config fails" "$(rc $TB labels --defined)" "1"
# 書式の検査(誤りがあれば何も書かない)
eq "labels-defined: bad label refused" "$(printf 'bad label\n' | rc $TB config set labels)" "1"
eq "labels-defined: duplicate refused" "$(printf 'ui\nui = again\n' | rc $TB config set labels)" "1"
eq "labels-defined: nothing written on error" "$([[ -e $TB_LABELS ]] && echo y || echo n)" "n"
printf '# 用意したラベル\nui = 画面\nbackend = サーバ側\n認証\nnotify  # 通知の規則用\n' | $TB config set labels >/dev/null
# 付けられるもの・付けられないもの
l1=$($TB add -p tkl -t a -l ui,認証)
eq "labels-defined: defined labels ok" "$($TB show "$l1" | jq -c .labels)" '["ui","認証"]'
eq "labels-defined: undefined label refused on add" "$(rc $TB add -p tkl -t b -l ui -l typo)" "2"
eq "labels-defined: refusal names the label and the list" "$($TB add -p tkl -t b -l typo 2>&1 | grep -c '用意されていないラベルです: typo(使えるのは: ui backend 認証 notify')" "1"
eq "labels-defined: undefined --add-label refused" "$(rc $TB update "$l1" --add-label typo)" "2"
eq "labels-defined: undefined --label refused" "$(rc $TB update "$l1" --label typo)" "2"
eq "labels-defined: refused update leaves labels" "$($TB show "$l1" | jq -c .labels)" '["ui","認証"]'
$TB update "$l1" --add-label backend --remove-label ui >/dev/null
eq "labels-defined: defined add + remove ok" "$($TB show "$l1" | jq -c .labels)" '["backend","認証"]'
# 外す・絞り込むのは制限しない(設定より前に付いたラベルも外せる)
fr=$($TB list -p tkl -l anything | cut -f1)
eq "labels-defined: filter by undefined label ok" "$(rc $TB list -p tkl -l anything)" "0"
$TB update "$fr" --remove-label anything >/dev/null
eq "labels-defined: remove undefined label ok" "$($TB show "$fr" | jq -c .labels)" '[]'
# tb 自身が付けるラベルは設定に書かなくても付けられる
eq "labels-defined: system labels allowed" "$($TB show "$($TB add -p tkl -t s -l deadline -l role:worker)" | jq -c .labels)" '["deadline","role:worker"]'
# --label '' で全部外すのは許す
$TB update "$l1" --label '' >/dev/null
eq "labels-defined: --label '' clears" "$($TB show "$l1" | jq -c .labels)" '[]'
# 一覧(書いた順。使われていないものは 0。説明つき)
$TB update "$l1" --label ui >/dev/null
$TB add -p tkl -t c -l ui >/dev/null
eq "labels-defined: --defined lists in order with counts" "$($TB labels --defined -p tkl)" "$(printf 'ui\t2\t2\t画面\nbackend\t0\t0\tサーバ側\n認証\t0\t0\t\nnotify\t0\t0\t')"
eq "labels-defined: --defined --json" "$($TB labels --defined -p tkl --json | head -1)" '{"label":"ui","open":2,"total":2,"description":"画面"}'
eq "labels-defined: config list shows key" "$($TB config list | grep -c '^labels ')" "1"
# 設定を消せばまた自由に付けられる
rm -f "$TB_LABELS"
eq "labels-defined: config removed → free again" "$(rc $TB add -p tkl -t d -l typo)" "0"
unset TB_LABELS
