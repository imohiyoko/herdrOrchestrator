#!/usr/bin/env bash
# lib/herdr-env.sh と lib/herdr_env.py が同じ既定値を返すか突き合わせる
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
pass=0; fail=0
eq() { if [[ "$2" == "$3" ]]; then pass=$((pass+1)); echo "ok   $1"; else fail=$((fail+1)); echo "FAIL $1"; echo "   bash:   $2"; echo "   python: $3"; fi; }
sh_val() { env -u TB_DB -u HERDR_BIN bash -c "source '$HERE/../lib/herdr-env.sh'; echo \"\$$1\""; }
py_val() { env -u TB_DB -u HERDR_BIN python3 -c "import sys; sys.path.insert(0, '$HERE/../lib'); import herdr_env; print($1)"; }
eq "root"      "$(sh_val HERDR_ROOT)"          "$(py_val herdr_env.HERDR_ROOT)"
eq "herdr bin" "$(sh_val HERDR_DEFAULT_BIN)"   "$(py_val 'herdr_env.herdr_bin()')"
eq "tb db"     "$(sh_val HERDR_DEFAULT_TB_DB)" "$(py_val 'herdr_env.tb_db()')"
eq "schema"    "$(sh_val HERDR_SCHEMA_DIR)"    "$(py_val herdr_env.SCHEMA_DIR)"
eq "legacy db" "$(sh_val HERDR_LEGACY_TB_DB)"  "$(py_val herdr_env.LEGACY_TB_DB)"
eq "tb db: XDG_STATE_HOME" "$(XDG_STATE_HOME=/xs sh_val HERDR_DEFAULT_TB_DB)" "$(XDG_STATE_HOME=/xs py_val 'herdr_env.tb_db()')"
eq "tb db: XDG_STATE_HOME is the base" "$(XDG_STATE_HOME=/xs sh_val HERDR_DEFAULT_TB_DB)" "/xs/herdr/tasks.db"
eq "tb db: the default is outside the checkout" "$(env -u XDG_STATE_HOME HOME=/hh bash -c "source '$HERE/../lib/herdr-env.sh'; echo \$HERDR_DEFAULT_TB_DB")" "/hh/.local/state/herdr/tasks.db"
# スキーマの最新の版 = taskboard/schema/NNN_*.sql の最大の番号(herdrctl の store も同じファイルを埋め込む)
eq "schema version" "$(cd "$HERE/../taskboard/schema" && ls [0-9][0-9][0-9]_*.sql | sed 's/_.*//' | sort -n | tail -1 | sed 's/^0*//')" "$(py_val 'herdr_env.schema_version()')"
eq "TB_DB override (python)" "$(TB_DB=/x/y.db python3 -c "import sys; sys.path.insert(0, '$HERE/../lib'); import herdr_env; print(herdr_env.tb_db())")" "/x/y.db"
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT
sess() { env -u HERDR_SESSION -u HERDR_SOCKET_PATH HERDR_SESSION_FILE=$T/s "$@"; }
for v in 'staging' '  training  # memo' 'a b' ''; do
  printf '%s\n' "$v" >"$T/s"
  eq "session file: '$v'" "$(sess bash -c "source '$HERE/../lib/herdr-env.sh'; herdr_session_name")" \
    "$(sess python3 -c "import sys; sys.path.insert(0, '$HERE/../lib'); import herdr_env; print(herdr_env.session_name())")"
done
echo "pass=$pass fail=$fail"; [[ $fail -eq 0 ]]
