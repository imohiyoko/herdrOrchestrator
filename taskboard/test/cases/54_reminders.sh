# shellcheck shell=bash
# run.sh から番号順に source される
# --- reminders(人が忘れているかもしれないもののまとめ。読み取り専用)
_rm_saved=$(env | grep -E '^(TB_DB|TB_CONFIG|TB_REPOS_FILE|TB_GH_BIN|FAKE_AGENTS)=' || true)
export TB_DB=$TMP/rem.db TB_CONFIG=$TMP/rem.conf TB_REPOS_FILE=$TMP/rem-repos.md TB_GH_BIN=$TMP/rem-gh
export TB_REMINDERS_DEADLINES=$TMP/rem-deadlines.conf TB_REMINDERS_TODAY=2026-03-10
export FAKE_AGENTS='[{"pane_id":"wW:p1","name":"worker1","agent_session":{"value":"sw"}}]'
printf 'rm=rm-conductor\n' >"$TB_CONFIG"
: >"$FAKE_LOG"
rsql() { sqlite3 "$TB_DB" "$@"; }
# 疑似 gh: pr list の結果は $TMP/rem-prs.json(無ければ空の配列)。呼ばれた回数を数える
cat >"$TB_GH_BIN" <<EOF
#!/usr/bin/env bash
echo "\$*" >>"$TMP/rem-gh.log"
cat "$TMP/rem-prs.json" 2>/dev/null || echo '[]'
EOF
chmod +x "$TB_GH_BIN"; : >"$TMP/rem-gh.log"

eq "reminders: empty → check says nothing new" "$(rc $TB reminders --check)" "3"
eq "reminders: check prints nothing" "$($TB reminders --check 2>&1)" ""
eq "reminders: empty list" "$($TB reminders | tail -1)" "忘れているかもしれないものはありません"
eq "reminders: no repos → gh not called" "$(wc -l <"$TMP/rem-gh.log" | tr -d ' ')" "0"

# 材料: 質問 1・ブロック 1(解決済み)・送信の承認待ち 1・停滞 1・期限ファイル 2(1 つは先すぎて載らない)
r1=$($TB add -p rm -t 'release prep'); r2=$($TB add -p rm -t 'stale work')
HERDR_PANE_ID=wW:p1 $TB comment "$r1" --kind question --text 'SECRET BODY should never appear' >/dev/null
HERDR_PANE_ID=wW:p1 $TB comment "$r1" --kind blocked --text 'blocked body' >/dev/null
env -u HERDR_PANE_ID $TB resolve C-2 >/dev/null
$TB claim "$r2" --owner worker1 >/dev/null
rsql "UPDATE tasks SET updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now','-30 hours') WHERE id='$r2';"
rsql "INSERT INTO outbox (herdr_session, from_name, from_pane, from_session, target, kind, text, status, created_at)
      VALUES ('default', 'conductor', 'p', 's', 'worker1', 'info', 'outbox body', 'pending', strftime('%Y-%m-%dT%H:%M:%SZ','now','-2 hours'));"
printf '%s\n' '# 期限' 'deadline = 2026-03-09 | cleanup branch' 'deadline = 2026-05-01 | far away' 'garbage line' 'deadline = 2026-13-40 | bad date' >"$TB_REMINDERS_DEADLINES"
seen_before=$(rsql "SELECT COUNT(*) FROM notify_seen;")
eq "reminders: new items → check exit 0" "$(rc $TB reminders --check)" "0"
eq "reminders: check writes nothing" "$(rsql "SELECT COUNT(*) FROM notify_seen;")" "$seen_before"
out=$($TB reminders --publish)
eq "reminders: first publish is the full list" "$(head -1 <<<"$out" | sed 's/.*\/ //')" "全件]"
eq "reminders: header row" "$(sed -n 2p <<<"$out")" "優先 | 何が | 誰の番 | 期限 | 参照 | 経過"
eq "reminders: priority order" "$(sed -n '3,$p' <<<"$out" | cut -d'|' -f1,5 | tr -d ' ' | tr '\n' ' ')" "急ぎ|deadline2026-03-09 人の番|O-1 人の番|C-1 停滞|$r2 "
eq "reminders: question row" "$(grep 'C-1' <<<"$out")" "人の番 | 質問: $r1 release prep | 人 | - | C-1 | 0m"
eq "reminders: stall owner and age" "$(grep '停滞' <<<"$out" | cut -d'|' -f3,6 | tr -d ' ')" "worker1|1d"
eq "reminders: no bodies" "$(grep -c 'BODY\|body' <<<"$out")" "0"
eq "reminders: resolved and far items left out" "$(grep -c 'C-2\|far away\|bad date' <<<"$out")" "0"
eq "reminders: only its own seen rows" "$(rsql "SELECT group_concat(key, ' ') FROM (SELECT key FROM notify_seen ORDER BY key);")" "reminders:full-day reminders:last reminders:state"
eq "reminders: last row for the Web UI" "$(rsql "SELECT value FROM notify_seen WHERE key='reminders:last';" | jq -r '[.items[].ref] | join(",")')" "deadline 2026-03-09,O-1,C-1,$r2"
eq "reminders: rows untouched" "$(rsql "SELECT COUNT(*) FROM comments WHERE resolved_at IS NULL;") $(rsql "SELECT status FROM outbox;")" "1 pending"

# 何も変わらない → check は 3、publish は継続の 1 行
eq "reminders: nothing new → check 3" "$(rc $TB reminders --check)" "3"
out=$($TB reminders --publish)
eq "reminders: unchanged collapsed" "$out" "$(printf '%s\n' "$(head -1 <<<"$out")" "継続: deadline 2026-03-09(1 日目), O-1(1 日目), C-1(1 日目), $r2(1 日目)")"
[[ $(head -1 <<<"$out") == *"新規 0・変化 0・継続 4]" ]] && ok "reminders: counts in header" || { ng "reminders: counts in header"; echo "   $out"; }
eq "reminders: same state, same summary" "$($TB reminders --publish | sed 1d)" "$(sed 1d <<<"$out")"

# 新しい項目
HERDR_PANE_ID=wW:p1 $TB comment "$r2" --kind question --text 'another' >/dev/null
eq "reminders: new item → check 0" "$(rc $TB reminders --check)" "0"
out=$($TB reminders --publish)
eq "reminders: only the new item gets a row" "$(sed -n '3,$p' <<<"$out" | grep -c ' | ')" "1"
eq "reminders: new item marked" "$(grep 'C-3' <<<"$out" | cut -d'|' -f2 | sed 's/^ //; s/ $//')" "質問: $r2 stale work (新規)"
eq "reminders: others still collapsed" "$(grep -c '^継続: deadline' <<<"$out")" "1"

# 状態の変化(停滞の更新時刻が変わる → 別の署名)と、期限の段階の変化(日付が進む)
rsql "UPDATE tasks SET updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now','-26 hours') WHERE id='$r2';"
out=$($TB reminders --publish)
eq "reminders: changed state shows again" "$(grep '停滞' <<<"$out" | cut -d'|' -f2 | sed 's/^ //; s/ $//')" "停滞: $r2 stale work (変化)"

# 消えた項目: 承認待ちが片付いた → 解消として数え、記録から消す
rsql "UPDATE outbox SET status='sent';"
eq "reminders: disappearing item → check 0" "$(rc $TB reminders --check)" "0"
out=$($TB reminders --publish)
[[ $(head -1 <<<"$out") == *"・解消 1]" ]] && ok "reminders: removed counted" || { ng "reminders: removed counted"; echo "   $out"; }
eq "reminders: removed from state" "$(rsql "SELECT value FROM notify_seen WHERE key='reminders:state';" | jq -r 'has("o:1")')" "false"
eq "reminders: then quiet again" "$(rc $TB reminders --check)" "3"

# 1 日 1 回の全件: 日付が変わると check が 0、publish は全件で、継続の日数が進む
eq "reminders: next day → check 0" "$(TB_REMINDERS_TODAY=2026-03-11 rc $TB reminders --check)" "0"
out=$(TB_REMINDERS_TODAY=2026-03-11 $TB reminders --publish)
eq "reminders: daily full list" "$(head -1 <<<"$out" | sed 's/.*\/ //')" "全件]"
eq "reminders: full list has every item" "$(sed -n '3,$p' <<<"$out" | grep -c ' | ')" "4"
out=$(TB_REMINDERS_TODAY=2026-03-11 $TB reminders --publish)
[[ $out == *"C-1(2 日目)"* ]] && ok "reminders: day N" || { ng "reminders: day N"; echo "   $out"; }
eq "reminders: one full list per day" "$(TB_REMINDERS_TODAY=2026-03-11 rc $TB reminders --check)" "3"

# 期限の列(別の変更で tasks に due を足す)。列があれば読む。done・期限なしは載せない。境界: 昨日=期限切れ・今日・明日=急ぎ、それ以降=予定
rsql "ALTER TABLE tasks ADD COLUMN due TEXT;"
d1=$($TB add -p rm -t 'due yesterday'); d2=$($TB add -p rm -t 'due today'); d3=$($TB add -p rm -t 'due tomorrow'); d4=$($TB add -p rm -t 'due in 3 days'); d5=$($TB add -p rm -t 'done already')
rsql "UPDATE tasks SET due='2026-03-10' WHERE id='$d1'; UPDATE tasks SET due='2026-03-11' WHERE id='$d2'; UPDATE tasks SET due='2026-03-12' WHERE id='$d3';
      UPDATE tasks SET due='2026-03-14' WHERE id='$d4'; UPDATE tasks SET due='2026-03-01', status='done' WHERE id='$d5';"
j=$(TB_REMINDERS_TODAY=2026-03-11 $TB reminders --json)
eq "reminders: due stages" "$(jq -r '[.items[] | select(.key | startswith("due:T-")) | "\(.ref):\(.p)"] | join(" ")' <<<"$j")" "$d1:1 $d2:1 $d3:1 $d4:4"
eq "reminders: task without due or done never listed" "$(jq -r '[.items[] | select(.ref == "'"$r1"'" or .ref == "'"$d5"'")] | length' <<<"$j")" "0"
eq "reminders: show does not write" "$(rsql "SELECT value FROM notify_seen WHERE key='reminders:state';" | jq -r 'has("due:'"$d1"'")')" "false"
TB_REMINDERS_TODAY=2026-03-11 $TB reminders --publish >/dev/null
out=$(TB_REMINDERS_TODAY=2026-03-12 $TB reminders --publish)
eq "reminders: stage change (today → overdue) shown on full list" "$(grep "$d2" <<<"$out" | cut -d'|' -f1,4 | tr -d ' ')" "急ぎ|2026-03-11"
rsql "UPDATE tasks SET due=NULL;"

# 保留した送信の受信箱(別の変更で足す表)。表があれば未読だけ読む。本文は出さない。別のセッションの行は読まない
rsql "CREATE TABLE inbox (id INTEGER PRIMARY KEY AUTOINCREMENT, herdr_session TEXT NOT NULL DEFAULT '', outbox_id INTEGER NOT NULL, recipient TEXT NOT NULL,
        from_name TEXT NOT NULL, kind TEXT NOT NULL, task_id TEXT, text TEXT NOT NULL, rule TEXT NOT NULL, created_at TEXT NOT NULL, read_at TEXT);
      INSERT INTO inbox (herdr_session, outbox_id, recipient, from_name, kind, text, rule, created_at, read_at) VALUES
        ('default', 1, 'assistant', 'conductor', 'info', 'held body', 'r', '2026-03-12T00:00:00Z', NULL),
        ('default', 2, 'assistant', 'conductor', 'info', 'read body', 'r', '2026-03-12T00:00:00Z', '2026-03-12T01:00:00Z'),
        ('other', 3, 'assistant', 'conductor', 'info', 'other body', 'r', '2026-03-12T00:00:00Z', NULL);"
j=$(TB_REMINDERS_TODAY=2026-03-12 $TB reminders --json)
eq "reminders: unread held messages" "$(jq -r '[.items[] | select(.key | startswith("inbox:")) | "\(.ref) \(.who) \(.what)"] | join(",")' <<<"$j")" "I-1 assistant 未読の保留メッセージ: conductor → assistant (info)"
eq "reminders: no held body" "$(grep -c 'body' <<<"$j")" "0"
rsql "DROP TABLE inbox;"

# レビュー待ちの PR(許可リストのリポジトリだけ。draft・承認済みは載せない)と、秘密らしい値を伏せる
printf '%s\n' '- org/repo' >"$TB_REPOS_FILE"
cat >"$TMP/rem-prs.json" <<'J'
[{"number":7,"title":"add feature","isDraft":false,"reviewDecision":"REVIEW_REQUIRED","createdAt":"2026-03-09T00:00:00Z"},
 {"number":8,"title":"draft","isDraft":true,"reviewDecision":"","createdAt":"2026-03-09T00:00:00Z"},
 {"number":9,"title":"approved","isDraft":false,"reviewDecision":"APPROVED","createdAt":"2026-03-09T00:00:00Z"},
 {"number":10,"title":"token ghp_abcdefghijklmnopqrstuvwxyz0123","isDraft":false,"reviewDecision":"","createdAt":"2026-03-09T00:00:00Z"}]
J
out=$(TB_REMINDERS_TODAY=2026-03-12 $TB reminders --publish)
eq "reminders: open PRs waiting for review" "$(grep -o 'org/repo#[0-9]*' <<<"$out" | tr '\n' ' ')" "org/repo#10 org/repo#7 "
eq "reminders: secret redacted" "$(grep -c 'ghp_' <<<"$out") $(grep -c '伏せた秘密' <<<"$out") $(tail -1 <<<"$out" | cut -d: -f1)" "0 1 (秘密らしい値を伏せました"
eq "reminders: secret redacted in the Web UI row" "$(rsql "SELECT value FROM notify_seen WHERE key='reminders:last';" | grep -c ghp_)" "0"
out=$(TB_REMINDERS_TODAY=2026-03-12 $TB reminders --publish)
eq "reminders: secret in a continuing item is redacted too" "$(grep -c 'ghp_' <<<"$out") $(TB_REMINDERS_TODAY=2026-03-12 $TB reminders --json | grep -c ghp_) $(rsql "SELECT value FROM notify_seen WHERE key='reminders:last';" | grep -c ghp_)" "0 0 0"
eq "reminders: gh read-only listing" "$(tail -1 "$TMP/rem-gh.log")" "pr list --repo org/repo --state open --limit 30 --json number,title,isDraft,reviewDecision,createdAt"
eq "reminders: bad flag" "$(rc $TB reminders --nope)" "2"
eq "reminders: bad today" "$(TB_REMINDERS_TODAY=x rc $TB reminders)" "2"

unset TB_REMINDERS_DEADLINES TB_REMINDERS_TODAY TB_REPOS_FILE TB_GH_BIN
while IFS= read -r _l; do [[ -n $_l ]] && export "$_l"; done <<<"$_rm_saved"
unset _rm_saved _l
