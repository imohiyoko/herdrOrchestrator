# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態を使うものがある)
# --- 構成: コマンドと help の対応、herdr を呼ぶ場所
LIB=$HERE/../lib
# コマンドは herdrctl tb --commands の 1 語目(<サービス>.<動詞> の形は除く)
cmds=$("${TB_HERDRCTL:-herdrctl}" tb --commands | tr ' ' '\n' | grep -v '\.' | grep . | sort -u)
# 古い名前は、新しい名前の下の「  別名(...): tb <古い名前> ...」の行に書けばよい(.claude/skills/tb-command-naming)
missing=""; for c in $cmds; do grep -qE "^tb $c( |$)" "$LIB/help.txt" || grep -qE "^  別名[^:]*:.* tb $c( |$)" "$LIB/help.txt" || missing+=" $c"; done
eq "layout: every command is in help.txt" "$missing" ""
mkdir -p "$TMP/pathbin"; ln -sf "$(cd "$HERE/../bin" && pwd)/tb" "$TMP/pathbin/tb"
eq "layout: works through a symlink on PATH" "$("$TMP/pathbin/tb" help | head -1 | cut -c1-6)" "tb add"
eq "layout: unknown command → 2" "$(rc $TB no-such-command) $(rc $TB 'cmd_add')" "2 2"
# tb の中で herdr を動かすのは herdr アダプタ(internal/kernel/herdr)だけ(scheduler の tick は別の部品)
eq "layout: only the herdr adapter runs herdr" "$(grep -rlE --include='*.go' --exclude='*_test.go' 'HERDR_BIN' "$HERE/../../internal" | grep -v '/internal/sched/' | sed "s|.*/internal/||" | tr '\n' ' ')$(grep -v "^ *#" "$HERE/../bin/tb" | grep -c HERDR_BIN)" "kernel/herdr/herdr.go 0"
