# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- 役割の定義を DB に(config 表の role:<名前> / role-map): このセッションの行 > 共通の行 > roles/ のファイル
_saved_pane=${HERDR_PANE_ID-}; unset HERDR_PANE_ID
_saved_hs=${HERDR_SESSION-}; unset HERDR_SESSION
_saved_roles=${TB_ROLES-}; _saved_appr=${TB_APPROVALS-}; _saved_fa=${FAKE_AGENTS-}
export TB_DB=$TMP/rolesdb.db TB_ROLES=$TMP/rolesdb TB_APPROVALS=$TMP/rolesdb-appr.conf
rm -rf "$TB_ROLES"; mkdir -p "$TB_ROLES"
printf '共通\n' >"$TB_ROLES/_common.md"; printf 'worker(ファイル)\n' >"$TB_ROLES/worker.md"; printf 'hr(ファイル)\n' >"$TB_ROLES/hr.md"
printf 'w* = worker\nhr* = hr\n' >"$TB_ROLES/map.conf"
printf 'role:change = allow\n* = ask\n' >"$TB_APPROVALS"
RA() { HERDR_SESSION=sa "$TB" "$@"; }
RB() { HERDR_SESSION=sb "$TB" "$@"; }

eq "role-list: ファイルの役割と出どころ" "$(RA role-list | cut -f1 | tr '\n' ' ')" "_common @map hr worker "
eq "role-show: ファイルの本文を渡す" "$(RA role-show w1 | grep -c 'worker(ファイル)')" "1"

printf 'worker(sa の DB)\n' | RA role-write worker >/dev/null
eq "role-write: このセッションの行。ほかのセッションはファイルのまま" "$(RA role-show w1 | grep -c 'sa の DB') $(RB role-show w1 | grep -c 'ファイル')" "1 1"
eq "role-list: 出どころ" "$(RA role-list | awk '$1=="worker"{print $2}') $(RB role-list | awk '$1=="worker"{print $2}')" "db:sa file:$TB_ROLES/worker.md"
printf 'analyst(共通)\n' | RB role-write analyst --shared >/dev/null
printf 'a* = analyst\nw* = worker\nhr* = hr\n' | RA role-write @map >/dev/null
eq "role-write --shared / @map: 共通の役割と、セッションの対応" "$(RA role-show a1 | grep -c 'analyst(共通)') $(RB role-show a1 2>/dev/null | grep -c analyst) $(RB role-list | cut -f1 | tr '\n' ' ')" "1 0 _common @map analyst hr worker "
printf 'tmp(共通)\n' | RB role-write tmprole --shared >/dev/null
eq "role-unset: 人は DB の行を消せる(消すと無くなる)" "$(RB role-unset tmprole --shared) $(RB role-list | grep -c '^tmprole')" "tmprole: なし 0"
printf 'worker(sb の DB)\n' | RB role-write worker >/dev/null
eq "role-unset: このセッションの行を消すと roles/ のファイルに戻る" "$(RB role-unset worker | cut -d' ' -f1-2)" "worker: file:$TB_ROLES/worker.md"
eq "role-set: DB にだけある役割も使える" "$(RA role-set nobody analyst 2>&1 | grep -c '定義のある役割名')" "0"

# 人事役(agent)からは role:change が allow のときだけ。人事役の役割・人事役を付ける対応は書けない
export FAKE_AGENTS='[{"pane_id":"wH:p1","name":"hr1","agent_session":{"value":"sh"}},{"pane_id":"wW:p1","name":"w1","agent_session":{"value":"sw"}}]'
eq "role-write: 人事役は書ける(書いた人が残る)" "$(printf 'worker(hr が更新)\n' | HERDR_PANE_ID=wH:p1 RA role-write worker >/dev/null 2>&1; echo $?) $(sqlite3 "$TB_DB" "SELECT changed_by FROM config_changes WHERE key='role:worker' ORDER BY id DESC LIMIT 1;")" "0 hr1"
eq "role-write: 人事役でない agent は断る" "$(printf 'x\n' | HERDR_PANE_ID=wW:p1 rc RA role-write worker)" "1"
eq "role-unset: agent からは消せない" "$(HERDR_PANE_ID=wH:p1 rc RA role-unset worker)" "1"
eq "role-write: 人事役は人事役の役割を書けない" "$(printf 'x\n' | HERDR_PANE_ID=wH:p1 rc RA role-write hr)" "1"
eq "role-write: 人事役は人事役を付ける対応を書けない" "$(printf 'z* = worker, hr\n' | HERDR_PANE_ID=wH:p1 rc RA role-write @map)" "1"
printf 'role:change = ask\n' >"$TB_APPROVALS"
eq "role-write: role:change が ask なら人に頼む(20)" "$(printf 'x\n' | HERDR_PANE_ID=wH:p1 rc RA role-write worker) $(RA role-show w1 | grep -c 'hr が更新')" "20 1"
eq "role-show: 引数なしは自分の pane の agent" "$(HERDR_PANE_ID=wW:p1 RA role-show | grep -c 'hr が更新')" "1"
eq "role-write: 空の本文は断る" "$(printf '' | rc RA role-write worker)" "1"

# 取り込み: roles/sessions/<セッション>/ は、そのセッションの行にしてディレクトリごと退かす
mkdir -p "$TB_ROLES/sessions/sc"; printf 'worker(sc のファイル)\n' >"$TB_ROLES/sessions/sc/worker.md"; printf 'q* = worker\n' >"$TB_ROLES/sessions/sc/map.conf"
_xs=${XDG_STATE_HOME-}; export XDG_STATE_HOME=$TMP/rolesdb-xs TB_CONFIG_DIR=$TMP/rolesdb-cfg; mkdir -p "$TB_CONFIG_DIR"
out=$(RA config import --dry-run)
eq "config import --dry-run: セッションの役割を数えるだけ" "$(grep -c 'セッション sc の行として取り込みます' <<<"$out") $([[ -d $TB_ROLES/sessions/sc ]] && echo kept)" "2 kept"
RA config import >/dev/null
eq "config import: セッションの役割はそのセッションの行に入り、ディレクトリは退く" \
  "$(sqlite3 "$TB_DB" "SELECT key FROM config WHERE herdr_session='sc' ORDER BY key;" | tr '\n' ' ')$([[ -d $TB_ROLES/sessions/sc ]] && echo left || echo moved) $(ls "$XDG_STATE_HOME"/herdr/config-imported/*/roles-sessions/)" \
  "role-map role:worker moved sc"
eq "config import: 取り込んだ役割が効く" "$(HERDR_SESSION=sc "$TB" role-show q1 | grep -c 'sc のファイル')" "1"
unset TB_CONFIG_DIR; if [[ -n $_xs ]]; then export XDG_STATE_HOME=$_xs; else unset XDG_STATE_HOME; fi

if [[ -n $_saved_hs ]]; then export HERDR_SESSION=$_saved_hs; fi
export HERDR_PANE_ID=$_saved_pane TB_ROLES=$_saved_roles TB_APPROVALS=$_saved_appr FAKE_AGENTS=$_saved_fa
[[ -n $_saved_roles ]] || unset TB_ROLES; [[ -n $_saved_appr ]] || unset TB_APPROVALS; [[ -n $_saved_fa ]] || unset FAKE_AGENTS
