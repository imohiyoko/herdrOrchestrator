# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- tb deadlines-import(期限ファイルの行をチケットにする)の使い方・文面・終了コード・作るチケット(bash と Go で同じ。期待値は bash の出力から作った)。project は dlc / dlc2 だけを使う
_dl_saved=$(declare -p HERDR_PANE_ID TB_REMINDERS_DEADLINES TB_SYSTEM_LABELS TB_LABELS 2>/dev/null || true)
unset HERDR_PANE_ID TB_LABELS
export TB_REMINDERS_DEADLINES=$TMP/dl-deadlines.conf TB_SYSTEM_LABELS=$TMP/dl-system-labels.conf; rm -f "$TB_REMINDERS_DEADLINES" "$TB_SYSTEM_LABELS"
dlerr() { "$@" 2>&1; echo "rc=$?"; }
dlrows() { sqlite3 "$TB_DB" "SELECT id, project, title, type, due, labels, IFNULL(owner_agent,'NULL'), status FROM tasks WHERE project IN ('dlc','dlc2') ORDER BY id;"; }

eq "deadlines-import: unknown" "$(dlerr $TB deadlines-import --bogus)" $'tb: deadlines-import [-p project(既定 deadline)] [--dry-run]\n(tb help で使い方)\nrc=2'
eq "deadlines-import: -p needs a value" "$(dlerr $TB deadlines-import -p)" $'tb: -p に値が必要です\n(tb help で使い方)\nrc=2'
eq "deadlines-import: bad project" "$(dlerr $TB deadlines-import -p 'a-b')" $'tb: project は英数字と _ のみ: a-b\n(tb help で使い方)\nrc=2'
eq "deadlines-import: agent pane" "$(HERDR_PANE_ID=w1:p1 FAKE_AGENTS='[{"pane_id":"w1:p1","name":"w1","agent_status":"idle","agent_session":{"value":"s1"}}]' dlerr $TB deadlines-import -p dlc)" $'tb: tb deadlines-import は人が自分の端末で実行します(agent \'w1\' の pane からは実行できません)\nrc=1'
eq "deadlines-import: no file" "$(dlerr $TB deadlines-import -p dlc | sed "s|$TMP|TMP|")" $'期限ファイルがありません: TMP/dl-deadlines.conf\nrc=0'
printf '%s\n' '# 期限' 'deadline = 2026-04-01 | 申請  # 書類' 'deadline=2026-04-02|更新' 'deadline = 2026-02-30 | 存在しない日' 'deadline = 2026-04-03 |   ' 'other = 2026-04-04 | 別のキー' 'deadline = 2026-04-01 | 申請' '  deadline =  2026-04-05 |  前後の空白  ' >"$TB_REMINDERS_DEADLINES"
eq "deadlines-import: dry-run" "$(dlerr $TB deadlines-import -p dlc --dry-run)" $'add(dry-run): 2026-04-01 | 申請\nadd(dry-run): 2026-04-02 | 更新\nskip(日付が正しくない): 2026-02-30 | 存在しない日\nadd(dry-run): 2026-04-01 | 申請\nadd(dry-run): 2026-04-05 | 前後の空白\n作成 4 件 / 取り込み済み 0 件(dry-run: 何も書いていません)\nrc=0'
eq "deadlines-import: dry-run wrote nothing" "$(dlrows)" $''
$TB add -p dlc2 -t 更新 --due 2026-04-02 >/dev/null
eq "deadlines-import: import" "$(dlerr $TB deadlines-import --project dlc)" $'add T-dlc-001: 2026-04-01 | 申請\nskip(取り込み済み T-dlc2-001): 2026-04-02 | 更新\nskip(日付が正しくない): 2026-02-30 | 存在しない日\nskip(取り込み済み T-dlc-001): 2026-04-01 | 申請\nadd T-dlc-002: 2026-04-05 | 前後の空白\n作成 2 件 / 取り込み済み 2 件\nrc=0'
eq "deadlines-import: rows" "$(dlrows)" $'T-dlc-001|dlc|申請|task|2026-04-01|["deadline"]|NULL|todo\nT-dlc-002|dlc|前後の空白|task|2026-04-05|["deadline"]|NULL|todo\nT-dlc2-001|dlc2|更新|task|2026-04-02|[]|NULL|todo'
eq "deadlines-import: again" "$(dlerr $TB deadlines-import -p dlc)" $'skip(取り込み済み T-dlc-001): 2026-04-01 | 申請\nskip(取り込み済み T-dlc2-001): 2026-04-02 | 更新\nskip(日付が正しくない): 2026-02-30 | 存在しない日\nskip(取り込み済み T-dlc-001): 2026-04-01 | 申請\nskip(取り込み済み T-dlc-002): 2026-04-05 | 前後の空白\n作成 0 件 / 取り込み済み 4 件\nrc=0'
printf 'deadline = 期日\n' >"$TB_SYSTEM_LABELS"
printf '%s\n' 'deadline = 2026-05-01 | 名前を変えたラベル' >>"$TB_REMINDERS_DEADLINES"
eq "deadlines-import: label from system-labels" "$(dlerr $TB deadlines-import -p dlc | tail -2; sqlite3 "$TB_DB" "SELECT labels FROM tasks WHERE title='名前を変えたラベル';")" $'作成 1 件 / 取り込み済み 4 件\nrc=0\n["期日"]'
printf '%s\n' "deadline = 2026-05-02 | $(printf 'x%.0s' $(seq 1 300))" >"$TB_REMINDERS_DEADLINES"
eq "deadlines-import: long title" "$(dlerr $TB deadlines-import -p dlc)" $'add T-dlc-004: 2026-05-02 | xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n作成 1 件 / 取り込み済み 0 件\nrc=0'
printf 'deadline = bad,label\n' >"$TB_SYSTEM_LABELS"; printf '%s\n' 'deadline = 2026-05-03 | ラベルが不正' >"$TB_REMINDERS_DEADLINES"
eq "deadlines-import: broken system-labels" "$(dlerr $TB deadlines-import -p dlc | sed "s|$TMP|TMP|")" $'tb: 設定 system-labels に不正な行があるので組み込みのラベルを使います(tb config edit system-labels で直す)\nadd T-dlc-005: 2026-05-03 | ラベルが不正\n作成 1 件 / 取り込み済み 0 件\nrc=0'
rm -f "$TB_SYSTEM_LABELS"; printf 'ops = 運用\n' >"$TMP/dl-labels.conf"; printf '%s\n' 'deadline = 2026-05-04 | 定義に無いラベル' >"$TB_REMINDERS_DEADLINES"
eq "deadlines-import: labels defined" "$(TB_LABELS=$TMP/dl-labels.conf dlerr $TB deadlines-import -p dlc | sed "s|$TMP|TMP|")" $'add T-dlc-006: 2026-05-04 | 定義に無いラベル\n作成 1 件 / 取り込み済み 0 件\nrc=0'
eq "deadlines-import: last rows" "$(dlrows | tail -2 | cut -d'|' -f1,3,6)" $'T-dlc-006|定義に無いラベル|["deadline"]\nT-dlc2-001|更新|[]'

unset HERDR_PANE_ID TB_REMINDERS_DEADLINES TB_SYSTEM_LABELS TB_LABELS
eval "$_dl_saved"
