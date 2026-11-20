# shellcheck shell=bash
# run.sh から番号順に source される
# --- 再発防止: 「$名前」の直後に非 ASCII 文字が続く展開を入れない。
#     bash 3.2 は変数名の直後の全角文字を名前の一部に読み、set -u の下で unbound variable になる。${名前} と書く
# grep の実装(BSD / GNU / ugrep)や他のケースが PATH に置く別の grep に左右されないよう、判定は perl のバイト照合で行う
FW_ROOT=$(cd "$HERE/../.." && pwd -P)
fw_scan() { perl -ne 'print "$ARGV:$.:$_" if /\$[A-Za-z_][A-Za-z0-9_]*[\x80-\xFF]/' "$@"; }
fw_hits=$(cd "$FW_ROOT" && find . -name .git -prune -o -type f ! -name '*.go' ! -name '*.md' ! -path '*/test/*' ! -path '*/tests/*' ! -path '*/__pycache__/*' -print0 | xargs -0 perl -ne 'print "$ARGV:$.:$_" if /\$[A-Za-z_][A-Za-z0-9_]*[\x80-\xFF]/')
eq "fullwidth-vars: no \$name directly followed by a non-ASCII character" "$fw_hits" ""

# 検出の自己確認(判定が空振りで通ってしまうのを防ぐ): 悪い例は拾い、${ } の例は拾わない
eq "fullwidth-vars: the check catches the bad form" "$(printf '%s\n' 'echo "a: $x。b"' | fw_scan | wc -l | tr -d ' ')" "1"
eq "fullwidth-vars: the check ignores the braced form" "$(printf '%s\n' 'echo "a: ${x}。b"' | fw_scan | wc -l | tr -d ' ')" "0"

# bash 3.2(macOS の /bin/bash)があれば、波括弧つきなら set -u でも落ちないことを確かめる
if [[ -x /bin/bash ]] && [[ $(/bin/bash -c 'echo "${BASH_VERSINFO[0]}"') == 3 ]]; then
  eq "fullwidth-vars: bash 3.2 expands the braced form under set -u" "$(/bin/bash -c 'set -u; v=ok; echo "(base: ${v}。次へ)"' 2>&1)" "(base: ok。次へ)"
fi
