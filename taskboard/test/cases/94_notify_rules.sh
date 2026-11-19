# shellcheck shell=bash
# 通知の規則(notify-rules。lib/notifyrules.sh。ADR 0003 の段 2)。別の DB で流し、触った変数は最後に戻す
_saved_pane=${HERDR_PANE_ID-}; unset HERDR_PANE_ID
_nr_old_db=$TB_DB
export TB_DB=$TMP/nrules.db TB_NOTIFY_CONF=$TMP/nr-notify.conf TB_NOTIFY_DIR=$TMP/nr-out TB_NOTIFY_RULES=$TMP/nr-rules.conf
printf 'enabled = on\nsinks_default = file\ndedupe_sec = 0\n' >"$TB_NOTIFY_CONF"
rm -f "$TB_NOTIFY_RULES"
nrsql() { sqlite3 "$TB_DB" "$@"; }
nrcount() { nrsql "SELECT COUNT(*) FROM notify_events WHERE type='ticket';"; }
nrlast() { nrsql "SELECT sinks || ' | ' || text FROM notify_events WHERE type='ticket' ORDER BY id DESC LIMIT 1;"; }

# 規則が無ければ何も積まない(読み位置は進む: 規則を足したあと、足す前のイベントには当てない)
a=$($TB add -p nr -t "規則なし" -l notify)
eq "rules: none → nothing" "$(nrcount)" "0"
eq "rules: cursor kept without rules" "$(nrsql "SELECT value FROM notify_seen WHERE key='rules:last';")" "$(nrsql "SELECT MAX(id) FROM task_events;")"
eq "rules: show says none" "$($TB notify rules | grep -c '規則なし')" "1"

# 規則の検査(tb config set は誤りがあれば書かない。ファイルを明示しているときはそのファイル)
printf 'bogus -> slack\n' | $TB config set notify-rules >/dev/null 2>&1; r=$?
eq "rules: unknown kind rejected on set" "$r $([[ -f $TB_NOTIFY_RULES ]] && echo written || echo none)" "1 none"
for bad in 'label_added label=notify' 'label_added label=a -> mail' 'label_added foo=x -> slack' 'status type>=bug -> slack' \
           "label_added label=a'b -> slack" 'status priority=urgent -> slack' 'status -> ' 'a -> b -> c'; do
  eq "rules: invalid '$bad'" "$(printf '%s\n' "$bad" | rc $TB config set notify-rules)" "1"
done
cat >"$TMP/nr-src" <<'R'
# コメントと空行は飛ばす

label_added label=notify          -> file webui
created type=bug priority>=high   -> file slack     # 末尾のコメント
status new=done project=nr        -> conductor
label_added label=urgent actor=human -> file
comment new=question              -> webui
R
$TB config set notify-rules <"$TMP/nr-src" >/dev/null
eq "rules: valid rules written" "$($TB notify rules | grep -c '^[0-9]* 行目')" "5"

# ラベルで通知(誰が付けても出る)。届け先は規則のもの。本文に ID・題・イベント
$TB update "$a" --add-label x >/dev/null
eq "rules: unrelated label → nothing" "$(nrcount)" "0"
$TB update "$a" --remove-label notify >/dev/null; $TB update "$a" --add-label notify >/dev/null
eq "rules: label_added notify → one" "$(nrcount)" "1"
eq "rules: sinks and text" "$(nrlast)" "file webui | [チケット] $a 規則なし — ラベル +notify / medium / task / 担当 -"
eq "rules: written to the file sink" "$(cat "$TB_NOTIFY_DIR"/*.md | grep -c "ラベル +notify")" "1"
export HERDR_PANE_ID=wW:p1
$TB update "$a" --remove-label notify >/dev/null; $TB update "$a" --add-label notify >/dev/null
eq "rules: label added by an agent also fires" "$(nrcount)" "2"
# actor=human は人(pane の外)だけ
$TB update "$a" --add-label urgent >/dev/null
eq "rules: actor=human skips agent" "$(nrcount)" "2"
unset HERDR_PANE_ID
$TB update "$a" --remove-label urgent >/dev/null; $TB update "$a" --add-label urgent >/dev/null
eq "rules: actor=human matches human" "$(nrcount)" "3"

# 種別・優先度の条件(>=)。作成時のラベルでも当たる
$TB add -p nr -t "低い bug" --type bug --priority low >/dev/null
eq "rules: priority>=high excludes low" "$(nrcount)" "3"
b=$($TB add -p nr -t "高い bug" --type bug --priority highest -l notify)
eq "rules: one event per ticket event, sinks merged" "$(nrcount)" "5"
eq "rules: slack sink → pending" "$(nrsql "SELECT slack_status FROM notify_events WHERE type='ticket' AND sinks LIKE '%slack%';")" "pending"

# 状態 + project、コメント
$TB done "$b" -d out.md -m ok >/dev/null
eq "rules: status new=done → conductor" "$(nrlast)" "conductor | [チケット] $b 高い bug — 状態 todo → done / highest / bug / 担当 -"
export HERDR_PANE_ID=wW:p1
FAKE_STATUS=idle $TB comment "$a" --kind question --text "本文は通知に載せない(規則の分)" >/dev/null 2>&1
unset HERDR_PANE_ID
eq "rules: comment kind" "$(nrlast)" "webui | [チケット] $a 規則なし — 報告(question) / medium / task / 担当 -"

# 試す(積まない)・読み位置
eid=$($TB history "$a" --json | jq -r 'select(.kind=="label_added" and .new=="notify") | .id' | tail -1)
n=$(nrcount)
eq "rules: test shows sinks and lines" "$($TB notify rules test "$eid")" "$eid: 届け先 file webui(当たった行: 3)。試すだけで、何も積みません"
eq "rules: test enqueues nothing" "$(nrcount)" "$n"
eq "rules: test of a non-matching event" "$($TB notify rules test "$($TB history "$a" --json | jq -r 'select(.kind=="created") | .id')" | grep -c 'どの規則にも当たりません')" "1"
eq "rules: test bad id" "$(rc $TB notify rules test 12)" "2"
eq "rules: cursor is the last event" "$($TB notify rules run)" "notify rules: 読み位置 $($TB events --limit 1 --json | jq -r .id)"

# off のあいだは積まない。窓より古いイベントは on にしても出さない
TB_NOTIFY_ENABLED=off $TB update "$a" --remove-label notify >/dev/null; TB_NOTIFY_ENABLED=off $TB update "$a" --add-label notify >/dev/null
eq "rules: off → nothing" "$(nrcount)" "$n"
nrsql "UPDATE task_events SET created_at='2000-01-01T00:00:00Z' WHERE id > (SELECT CAST(value AS INTEGER) FROM notify_seen WHERE key='rules:last');"
$TB notify rules run >/dev/null
eq "rules: old backlog is skipped when back on" "$(nrcount)" "$n"
$TB update "$a" --remove-label notify >/dev/null; $TB update "$a" --add-label notify >/dev/null
eq "rules: new events after backlog still fire" "$(nrcount)" "$((n + 1))"

# Web UI の緊急通知と digest の届け先は、それぞれの読みで拾う(digest は指揮役へ 1 回だけ)
: >"$FAKE_LOG"
FAKE_STATUS=idle $TB digest >/dev/null 2>&1
eq "rules: conductor sink goes into the digest" "$(grep '^prompt conductor' "$FAKE_LOG" | grep -c "$b 通知の規則")" "1"
: >"$FAKE_LOG"; FAKE_STATUS=idle $TB digest >/dev/null 2>&1
eq "rules: conductor item is sent once" "$(grep -c "$b 通知の規則" "$FAKE_LOG")" "0"

# 条件の無い規則(種類と届け先だけ)
printf 'priority -> stdout\n' >"$TB_NOTIFY_RULES"
n=$(nrcount); $TB update "$a" --priority low >/dev/null
eq "rules: rule without conditions" "$(nrcount) $(nrlast)" "$((n + 1)) stdout | [チケット] $a 規則なし — 優先度 medium → low / low / task / 担当 -"
eq "rules: show a rule without conditions" "$($TB notify rules | grep '^1 行目')" "$(printf '1 行目\tpriority -> stdout')"
# 期限のイベント(tb due-scan)も規則に当たる
printf 'overdue -> webui\n' >"$TB_NOTIFY_RULES"
od=$($TB add -p nr -t "期限切れ" --due 2026-01-01)
n=$(nrcount)
TB_TODAY=2026-01-05 $TB due-scan >/dev/null
eq "rules: overdue from due-scan fires" "$(nrcount) $(nrlast)" "$((n + 1)) webui | [チケット] $od 期限切れ — 期限切れ(期日 2026-01-01) / medium / task / 担当 -"

# 規則のファイルを手で壊しても、正しい行だけが効く(誤りは show に出る)
printf 'label_added label=notify -> file\nnope -> file\n' >"$TB_NOTIFY_RULES"
eq "rules: broken line shown, good line kept" "$($TB notify rules | grep -c '誤りのある行') $($TB notify rules | grep -c '^[0-9]* 行目')" "1 1"

[[ -z $_saved_pane ]] || export HERDR_PANE_ID=$_saved_pane
export TB_DB=$_nr_old_db
unset TB_NOTIFY_CONF TB_NOTIFY_DIR TB_NOTIFY_RULES
