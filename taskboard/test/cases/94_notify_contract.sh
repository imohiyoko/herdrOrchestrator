# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- tb notify の出力・文面・整形の細かいところ(bash と Go で同じ。期待値は bash の出力から作った)
_saved_pane=${HERDR_PANE_ID-}; unset HERDR_PANE_ID
_nc_old_db=$TB_DB _nc_old_agents=${FAKE_AGENTS-}
export TB_DB=$TMP/ncontract.db TB_NOTIFY_CONF=$TMP/nc-notify.conf TB_NOTIFY_DIR=$TMP/nc-out TB_REPOS_FILE=$TMP/nc-repos.md TB_CURL=$TMP/nc-curl TB_NOTIFY_RULES=$TMP/nc-rules.conf
export FAKE_AGENTS='[{"pane_id":"wW:p1","name":"worker1","agent_session":{"value":"sw"}},{"pane_id":"wN:p1","name":"notifier","agent_session":{"value":"sn"}}]'
for _v in $(env | sed -n 's/^\(TB_NOTIFY_[A-Z_]*\)=.*/\1/p'); do case $_v in TB_NOTIFY_CONF|TB_NOTIFY_DIR|TB_NOTIFY_RULES) ;; *) unset "$_v";; esac; done
rm -f "$TB_NOTIFY_CONF" "$TB_NOTIFY_RULES"
ncsql() { sqlite3 "$TB_DB" "$@"; }
ncts() { sed -E 's/[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]{8}Z/TS/g'; }
ncerr() { "$@" 2>&1; echo "rc=$?"; }
nctext() { ncsql "SELECT text FROM notify_events WHERE ref='$1';"; }
$TB add -p nc -t "通知の契約" >/dev/null

# ---- status(既定・設定ファイル・環境変数)
eq "status: defaults" "$($TB notify status)" $'enabled: off (default)\ndefault_sinks = file stdout slack\n  sinks[warning]: file stdout\nslack_via: agent\nslack_target: 未設定(値は表示しません)\nnotifier_agent: notifier\nwebhook_file: 未設定\nrate: 6/min, 60/hour(超えた分は 1 通にまとめる)  max_len: 600  dedupe_sec: 600\nhash_check: on  ack_timeout_min: 5  ack_retries: 3\nmask: email=on ip=off repo=off account=off host=off  strip_raw_logs: on\nsecret filter: 常に有効(秘密らしい値を含むイベントは外へ出さず、file にだけ伏せて残す)\nevents: なし'
eq "status --json: defaults" "$($TB notify status --json)" $'{"enabled":"off","slack_via":"agent","slack_target":"unset","notifier_agent":"notifier","webhook_file":"未設定","default_sinks":"file stdout slack","rate_per_min":6,"rate_per_hour":60,"max_len":600,"slack_status_counts":""}'
eq "notify with no subcommand = status" "$($TB notify | head -1)" $'enabled: off (default)'
cat >"$TMP/nc-notify.conf" <<'C'
# 設定の例
enabled = yes
rate_per_min = 007
max_len = 30
bogus = 1
sinks_question = file,stdout,file
sinks_bogus = file
sinks_alert_raised = file mail
slack_target = bad target
notifier_agent = nt-1
webhook_file = /nonexistent/wh
mask_ip = maybe
no equals sign
=
C
eq "status: conf with invalid lines" "$($TB notify status)" $'enabled: on (conf)\ndefault_sinks = file stdout slack\n  sinks[question]: file stdout\n  sinks[warning]: file stdout\nslack_via: agent\nslack_target: 未設定(値は表示しません)\nnotifier_agent: nt-1\nwebhook_file: ファイルが無い\nrate: 7/min, 60/hour(超えた分は 1 通にまとめる)  max_len: 600  dedupe_sec: 600\nhash_check: on  ack_timeout_min: 5  ack_retries: 3\nmask: email=on ip=off repo=off account=off host=off  strip_raw_logs: on\nsecret filter: 常に有効(秘密らしい値を含むイベントは外へ出さず、file にだけ伏せて残す)\nevents: なし'
eq "status: env overrides (sinks, ints, invalid values ignored)" "$(TB_NOTIFY_SINKS_DEFAULT='slack, file' TB_NOTIFY_RATE_PER_HOUR=0010 TB_NOTIFY_MAX_LEN=39 TB_NOTIFY_ENABLED= TB_NOTIFY_SINKS_WARNING= $TB notify status)" $'enabled: on (conf)\ndefault_sinks = slack file\n  sinks[question]: file stdout\n  sinks[warning]: file stdout\nslack_via: agent\nslack_target: 未設定(値は表示しません)\nnotifier_agent: nt-1\nwebhook_file: ファイルが無い\nrate: 7/min, 10/hour(超えた分は 1 通にまとめる)  max_len: 600  dedupe_sec: 600\nhash_check: on  ack_timeout_min: 5  ack_retries: 3\nmask: email=on ip=off repo=off account=off host=off  strip_raw_logs: on\nsecret filter: 常に有効(秘密らしい値を含むイベントは外へ出さず、file にだけ伏せて残す)\nevents: なし'
eq "status --json: env and conf" "$(TB_NOTIFY_SLACK_TARGET=C1 TB_NOTIFY_SLACK_VIA=webhook $TB notify status --json)" $'{"enabled":"on","slack_via":"webhook","slack_target":"set","notifier_agent":"nt-1","webhook_file":"ファイルが無い","default_sinks":"file stdout slack","rate_per_min":7,"rate_per_hour":60,"max_len":600,"slack_status_counts":""}'
rm -f "$TB_NOTIFY_CONF"
printf 'https://example.invalid/x\n' >"$TMP/nc-wh"; chmod 644 "$TMP/nc-wh"; ln -sf "$TMP/nc-wh" "$TMP/nc-whl"
eq "status: webhook_file states" "$(for f in '' "$TMP/none" "$TMP/nc-wh"; do TB_NOTIFY_WEBHOOK_FILE=$f $TB notify status | grep '^webhook_file'; done; chmod 600 "$TMP/nc-wh"; TB_NOTIFY_WEBHOOK_FILE=$TMP/nc-wh $TB notify status | grep '^webhook_file'; TB_NOTIFY_WEBHOOK_FILE=$TMP/nc-whl $TB notify status | grep '^webhook_file')" $'webhook_file: 未設定\nwebhook_file: ファイルが無い\nwebhook_file: 権限が 0600 ではない\nwebhook_file: ok(0600)\nwebhook_file: 権限が 0600 ではない'

# ---- on / off(設定 notify のその行だけを書き換える)
printf '# 先頭のコメント\nenabled = off\nrate_per_min = 3\n enabled=on\n#enabled = x\nmax_len = 80\n' >"$TB_NOTIFY_CONF"
eq "on: rewrites the first enabled line, drops later ones" "$($TB notify on; cat "$TB_NOTIFY_CONF")" $'notify: enabled = on\n(外へ出る前に tb notify status と tb notify test で確かめてください)\n# 先頭のコメント\nenabled = on\nrate_per_min = 3\n#enabled = x\nmax_len = 80'
eq "off: output" "$($TB notify off)" $'notify: enabled = off'
rm -f "$TB_NOTIFY_CONF"
eq "on: no conf yet → one line" "$($TB notify on >/dev/null; cat "$TB_NOTIFY_CONF")" $'enabled = on'
: >"$TB_NOTIFY_CONF"
eq "on: empty conf → one line" "$($TB notify on >/dev/null; cat "$TB_NOTIFY_CONF")" $'enabled = on'
eq "human only: on/off/test/flush from an agent pane" "$(for s in on off test flush; do HERDR_PANE_ID=wW:p1 ncerr $TB notify $s; done)" $'tb: tb notify on は人が自分の端末で実行します(agent \'worker1\' の pane からは実行できません)\nrc=1\ntb: tb notify off は人が自分の端末で実行します(agent \'worker1\' の pane からは実行できません)\nrc=1\ntb: tb notify test は人が自分の端末で実行します(agent \'worker1\' の pane からは実行できません)\nrc=1\ntb: tb notify flush は人が自分の端末で実行します(agent \'worker1\' の pane からは実行できません)\nrc=1'
eq "error: tb notify on x" "$(ncerr $TB notify on x)" $'tb: notify on(引数なし)\n(tb help で使い方)\nrc=2'
eq "error: tb notify off x" "$(ncerr $TB notify off x)" $'tb: notify off(引数なし)\n(tb help で使い方)\nrc=2'
eq "error: tb notify test x" "$(ncerr $TB notify test x)" $'tb: notify test(引数なし)\n(tb help で使い方)\nrc=2'
eq "error: tb notify flush x" "$(ncerr $TB notify flush x)" $'tb: notify flush(引数なし)\n(tb help で使い方)\nrc=2'
eq "error: tb notify dry-run x" "$(ncerr $TB notify dry-run x)" $'tb: notify dry-run(引数なし)\n(tb help で使い方)\nrc=2'
eq "error: tb notify bogus" "$(ncerr $TB notify bogus)" $'tb: notify: 不明なサブコマンド bogus(status|on|off|test|dry-run|tail|ack|flush|events|rules)\n(tb help で使い方)\nrc=2'
eq "error: tb notify hash" "$(ncerr $TB notify hash)" $'tb: notify hash <本文>\n(tb help で使い方)\nrc=2'
eq "error: tb notify hash a b" "$(ncerr $TB notify hash a b)" $'tb: notify hash <本文>\n(tb help で使い方)\nrc=2'
eq "error: tb notify event" "$(ncerr $TB notify event)" $'tb: notify event incident_opened|incident_closed <ID> [--title T] [--severity S]\n(tb help で使い方)\nrc=2'
eq "error: tb notify event incident_opened" "$(ncerr $TB notify event incident_opened)" $'tb: notify event incident_opened|incident_closed <ID> [--title T] [--severity S]\n(tb help で使い方)\nrc=2'
eq "error: tb notify event bogus x" "$(ncerr $TB notify event bogus x)" $'tb: notify event: 種別は incident_opened|incident_closed\n(tb help で使い方)\nrc=2'
eq "error: tb notify event incident_opened x --bogus" "$(ncerr $TB notify event incident_opened x --bogus)" $'tb: notify event: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "error: tb notify event incident_opened x --title" "$(ncerr $TB notify event incident_opened x --title)" $'tb: --title に値が必要です\n(tb help で使い方)\nrc=2'
eq "error: tb notify events --bogus" "$(ncerr $TB notify events --bogus)" $'tb: notify events [--status S] [--limit N] [--json]\n(tb help で使い方)\nrc=2'
eq "error: tb notify events --limit 12345" "$(ncerr $TB notify events --limit 12345)" $'tb: events: --limit は整数\n(tb help で使い方)\nrc=2'
eq "error: tb notify events --limit" "$(ncerr $TB notify events --limit)" $'tb: --limit に値が必要です\n(tb help で使い方)\nrc=2'
eq "error: tb notify events --status a b" "$(ncerr $TB notify events --status 'a b')" $'tb: events: --status が不正\n(tb help で使い方)\nrc=2'
eq "error: tb notify tail -x" "$(ncerr $TB notify tail -x)" $'tb: notify tail [-n N] [-f]\n(tb help で使い方)\nrc=2'
eq "error: tb notify tail -n x" "$(ncerr $TB notify tail -n x)" $'tb: tail: -n は整数\n(tb help で使い方)\nrc=2'
eq "error: tb notify tail -n" "$(ncerr $TB notify tail -n)" $'tb: -n に値が必要です\n(tb help で使い方)\nrc=2'
eq "error: tb notify ack" "$(ncerr $TB notify ack)" $'tb: notify ack N-<n> <hash> [--id <投稿の ID>]\n(tb help で使い方)\nrc=2'
eq "error: tb notify ack N-1" "$(ncerr $TB notify ack N-1)" $'tb: notify ack N-<n> <hash> [--id <投稿の ID>]\n(tb help で使い方)\nrc=2'
eq "error: tb notify ack x 0123456789ab" "$(ncerr $TB notify ack x 0123456789ab)" $'tb: イベント ID は N-<数字>: x\n(tb help で使い方)\nrc=2'
eq "error: tb notify ack N-1 xyz" "$(ncerr $TB notify ack N-1 xyz)" $'tb: ack: hash は 12 桁の 16 進(tb notify hash で出す)\n(tb help で使い方)\nrc=2'
eq "error: tb notify ack N-1 0123456789ab --id a b" "$(ncerr $TB notify ack N-1 0123456789ab --id 'a b')" $'tb: ack: --id は英数字と . _ : / - の 80 字まで\n(tb help で使い方)\nrc=2'
eq "error: tb notify ack N-1 0123456789ab --bogus" "$(ncerr $TB notify ack N-1 0123456789ab --bogus)" $'tb: notify ack: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "error: tb notify ack N-1 0123456789ab --id" "$(ncerr $TB notify ack N-1 0123456789ab --id)" $'tb: --id に値が必要です\n(tb help で使い方)\nrc=2'
eq "error: tb notify ack N-999 0123456789ab" "$(ncerr $TB notify ack N-999 0123456789ab)" $'tb: イベントが見つかりません: N-999\nrc=1'
eq "error: tb notify rules bogus" "$(ncerr $TB notify rules bogus)" $'tb: notify rules [show | test E-<n> | run]\n(tb help で使い方)\nrc=2'
eq "error: tb notify rules show x" "$(ncerr $TB notify rules show x)" $'tb: notify rules [show]\n(tb help で使い方)\nrc=2'
eq "error: tb notify rules test" "$(ncerr $TB notify rules test)" $'tb: notify rules test E-<n>\n(tb help で使い方)\nrc=2'
eq "error: tb notify rules test E-x" "$(ncerr $TB notify rules test E-x)" $'tb: notify rules test E-<n>\n(tb help で使い方)\nrc=2'
eq "error: tb notify rules test E-999" "$(ncerr $TB notify rules test E-999)" $'tb: イベントが見つかりません: E-999\nrc=1'
eq "error: tb notify rules run x" "$(ncerr $TB notify rules run x)" $'tb: notify rules run\n(tb help で使い方)\nrc=2'

# ---- 整形・伏せ字・秘匿フィルタ(本文は DB の text)
export TB_NOTIFY_ENABLED=on TB_NOTIFY_DEDUPE_SEC=0
ncev() { $TB notify event incident_opened "$@" >/dev/null; }
ncev f1 --title 'x 123456789012 / 1234567890123 / a12345678901234b / 99999999 1111222233334444'
ncev f2 --title 'db1.internal a.b.c.local x-y.lan web.corp s3.ap-1.amazonaws.com foo.localx'
ncev f3 --title '1.2.3.4 と 10.20.30.40.50 と 999.1.1.1' --severity 'SEV 1'
ncev f4 --title $'タブ\tと CR\rと\x7f DEL と\x02制御  空白   の連続'
ncev f5 --title $'1 行目\n\n   \n  2 行目  \n3 行目'
ncev f6 --title $'```\ncode\n```\n[12:00:01] log\n2024-01-01 10:00 log\n本文だけ\n```\n閉じない'
ncev f7 --title '@everyone @here @channel <b> a@b.co x.y+z@sub.example.com'
printf -- '- acme/widgets\n- bad repo\n  -  other/repo  \n' >"$TB_REPOS_FILE"
TB_NOTIFY_MASK_IP=on TB_NOTIFY_MASK_REPO=1 TB_NOTIFY_MASK_ACCOUNT=true TB_NOTIFY_MASK_HOST=yes TB_NOTIFY_MASK_EMAIL=no TB_NOTIFY_STRIP_RAW_LOGS=off ncev f8 --title $'acme/widgets other/repo 1.2.3.4 123456789012 db.local a@b.co\n```\nkept'
TB_NOTIFY_MAX_LEN=40 ncev f9 --title "$(printf 'いろは%.0s' $(seq 1 30))"
eq "format: f1" "$(nctext f1)" $'[障害] f1 開始: x 123456789012 / 1234567890123 / a12345678901234b / 99999999 1111222233334444'
eq "format: f2" "$(nctext f2)" $'[障害] f2 開始: db1.internal a.b.c.local x-y.lan web.corp s3.ap-1.amazonaws.com foo.localx'
eq "format: f3" "$(nctext f3)" $'[障害] f3 開始 SEV 1: 1.2.3.4 と 10.20.30.40.50 と 999.1.1.1'
eq "format: f4" "$(nctext f4)" $'[障害] f4 開始: タブ と CR と DEL と制御 空白 の連続'
eq "format: f5" "$(nctext f5)" $'[障害] f5 開始: 1 行目 / 2 行目'
eq "format: f6" "$(nctext f6)" $'[障害] f6 開始: (コードは省略) / 本文だけ'
eq "format: f7" "$(nctext f7)" $'[障害] f7 開始: ＠everyone ＠here ＠channel ‹b› a***@*** a***@***'
eq "format: f8" "$(nctext f8)" $'[障害] f8 開始: [repo] [repo] 1.*.*.* ********9012 [host] a@b.co / ```'
eq "format: f9" "$(nctext f9)" $'[障害] f9 開始: いろはいろはいろはいろはいろはいろはいろはいろはいろはい'
S1="AKIA""ABCDEFGHIJKLMNOP"; S2="-----BEGIN ""OPENSSH PRIVATE KEY-----"; S3="xoxb-""1234567890-abc"; S4="sk-""abcdefghijklmnopqrstu"; S5="AIza""Sy0123456789abcdefghijklmnopqrstu"
S6="eyJ""hbGciOiJIUzI1.eyJzdWIiOiIxMjM0.SflKxwRJSM"; S7="hooks.slack"".com/services/T0000/B000"; S8="Bearer ""abcdefghijklmnop1234"; S9="api_""key: abcdef123"; S10="ZXhhbXBsZTEyMzQ1Njc4OWFiY2RlZkdISUpLTE1O"
S11="0123456789abcdef""0123456789ABCDEF"; S12="github_pat_""ABCDEFGHIJ0123456789_x"; S13="ghp_""abcdefghij0123456789"
i=0; for s in "$S1" "$S2" "$S3" "$S4" "$S5" "$S6" "$S7" "$S8" "$S9" "$S10" "$S11" "$S12" "$S13"; do i=$((i + 1)); ncev "s$i" --title "前 $s 後"; done
eq "secrets: each pattern blocks, reason and redacted text" "$(ncsql "SELECT ref||'|'||blocked||'|'||COALESCE(blocked_reason,'-')||'|'||sinks||'|'||text FROM notify_events WHERE ref GLOB 's[0-9]*' ORDER BY id;")" $'s1|1|AWS アクセスキー ID|file stdout|[障害] s1 開始: 前 [伏せた秘密] 後\ns2|1|秘密鍵のヘッダ|file stdout|[障害] s2 開始: 前 [伏せた秘密] 後\ns3|1|トークン(API キーなどの形式)|file stdout|[障害] s3 開始: 前 [伏せた秘密] 後\ns4|1|トークン(API キーなどの形式)|file stdout|[障害] s4 開始: 前 [伏せた秘密] 後\ns5|1|トークン(API キーなどの形式), 長い英数字の連なり|file stdout|[障害] s5 開始: 前 [伏せた秘密] 後\ns6|1|トークン(API キーなどの形式)|file stdout|[障害] s6 開始: 前 [伏せた秘密] 後\ns7|1|トークン(API キーなどの形式)|file stdout|[障害] s7 開始: 前 [伏せた秘密] 後\ns8|1|Bearer トークン|file stdout|[障害] s8 開始: 前 [伏せた秘密] 後\ns9|1|キー=値の形の秘密|file stdout|[障害] s9 開始: 前 [伏せた秘密] 後\ns10|1|長い英数字の連なり|file stdout|[障害] s10 開始: 前 [伏せた秘密] 後\ns11|1|長い英数字の連なり|file stdout|[障害] s11 開始: 前 [伏せた秘密] 後\ns12|1|トークン(API キーなどの形式)|file stdout|[障害] s12 開始: 前 [伏せた秘密] 後\ns13|1|トークン(API キーなどの形式)|file stdout|[障害] s13 開始: 前 [伏せた秘密] 後'
eq "secrets: warnings for each" "$(ncsql "SELECT text FROM notify_events WHERE type='warning' ORDER BY id;" | sed -E 's/N-[0-9]+/N-x/g' | sort | uniq -c | sed 's/^ *//')" $'1 [通知の警告] N-x(incident_opened)は秘密らしい値(AWS アクセスキー ID)を含むため、外へ出していません。file には伏せて残しました\n1 [通知の警告] N-x(incident_opened)は秘密らしい値(Bearer トークン)を含むため、外へ出していません。file には伏せて残しました\n1 [通知の警告] N-x(incident_opened)は秘密らしい値(キー=値の形の秘密)を含むため、外へ出していません。file には伏せて残しました\n6 [通知の警告] N-x(incident_opened)は秘密らしい値(トークン(API キーなどの形式))を含むため、外へ出していません。file には伏せて残しました\n1 [通知の警告] N-x(incident_opened)は秘密らしい値(トークン(API キーなどの形式), 長い英数字の連なり)を含むため、外へ出していません。file には伏せて残しました\n1 [通知の警告] N-x(incident_opened)は秘密らしい値(秘密鍵のヘッダ)を含むため、外へ出していません。file には伏せて残しました\n2 [通知の警告] N-x(incident_opened)は秘密らしい値(長い英数字の連なり)を含むため、外へ出していません。file には伏せて残しました'
ncev s20 --title "token=abcdefgh token=abcdefghijkl $S13 ${S13}xyz" --severity "$S1"
eq "secrets: longest hit first, severity redacted too" "$(ncsql "SELECT COALESCE(severity,'-')||'|'||text FROM notify_events WHERE ref='s20';")" $'[伏せた秘密]|[障害] s20 開始 [伏せた秘密]: [伏せた秘密] [伏せた秘密] [伏せた秘密] [伏せた秘密]'
ncev "$S1" --title 'ref に秘密'
eq "secrets: secret in the ref is redacted" "$(ncsql "SELECT ref||'|'||text FROM notify_events WHERE type='incident_opened' ORDER BY id DESC LIMIT 1;")" $'[伏せた秘密]|[障害] [伏せた秘密] 開始: ref に秘密'
ncev ok1 --title 'reports/inc001/observations-and-findings-for-the-week.md と abcdefghijklmnopqrstuvwxyzabcdefgh と 0123456789abcdef0123456789abcde'
eq "secrets: lowercase-only runs and 31 hex are not secrets" "$(ncsql "SELECT blocked||' '||text FROM notify_events WHERE ref='ok1';")" $'0 [障害] ok1 開始: reports/inc001/observations-and-findings-for-the-week.md と abcdefghijklmnopqrstuvwxyzabcdefgh と 0123456789abcdef01234567'
ncev test-ref --title "$S13" --severity x
eq "file sink: line format (blocked mark)" "$(grep -E 'N-[0-9]+ \[incident_opened\]' "$TB_NOTIFY_DIR/$(date +%F).md" | tail -2 | ncts | sed -E 's/N-[0-9]+/N-x/')" $'- TS N-x [incident_opened] [障害] ok1 開始: reports/inc001/observations-and-findings-for-the-week.md と abcdefghijklmnopqrstuvwxyzabcdefgh と 0123456789abcdef01234567\n- TS N-x [incident_opened] [外へは出していない: 秘密を検出] [障害] test-ref 開始 x: [伏せた秘密]'
eq "stdout sink: line format" "$(tail -2 "$TB_NOTIFY_DIR/stdout.log" | ncts | sed -E 's/N-[0-9]+/N-x/')" $'TS N-x [障害] test-ref 開始 x: [伏せた秘密] [外へは出していない: 秘密を検出]\nTS N-x [通知の警告] N-41(incident_opened)は秘密らしい値(トークン(API キーなどの形式))を含むため、外へ出していません。file には伏せて残しました'
eq "hash" "$($TB notify hash '[障害] x 開始: あ')" $'0323f4faf815'

# ---- events / tail / dry-run
ncsql "UPDATE notify_events SET slack_status='acked' WHERE ref='f1'; UPDATE notify_events SET slack_status='awaiting-ack' WHERE ref='f2';"
eq "events: text, newest first, --limit" "$($TB notify events --limit 0003 | ncts)" $'N-42\twarning\tnone\t-\tTS\t[通知の警告] N-41(incident_opened)は秘密らしい値(トークン(API キーなどの形式))を含むため、外へ出していません。file には伏せて残しました\nN-41\tincident_opened\tnone\tblocked\tTS\t[障害] test-ref 開始 x: [伏せた秘密]\nN-40\tincident_opened\tpending\t-\tTS\t[障害] ok1 開始: reports/inc001/observations-and-findings-for-the-week.md と abcdefghijklmnopqrstuvwxyzabcdefgh と 0123456789abcdef01234567'
eq "events --status acked" "$($TB notify events --status acked | ncts)" $'N-1\tincident_opened\tacked\t-\tTS\t[障害] f1 開始: x 123456789012 / 1234567890123 / a12345678901234b / 99999999 1111222233334444'
eq "events --json --limit 2" "$($TB notify events --json --limit 2 | ncts | sed -E 's/"hash":"[0-9a-f]{12}"/"hash":"H"/')" $'{"id":"N-42","type":"warning","ref":"N-41","severity":null,"text":"[通知の警告] N-41(incident_opened)は秘密らしい値(トークン(API キーなどの形式))を含むため、外へ出していません。file には伏せて残しました","hash":"H","blocked":0,"blocked_reason":null,"sinks":"file stdout","dedupe_key":"warning:N-41","file_at":"TS","slack_status":"none","slack_attempts":0,"slack_sent_at":null,"slack_msg_id":null,"batch_id":null,"note":null,"created_at":"TS","herdr_session":"default"}\n{"id":"N-41","type":"incident_opened","ref":"test-ref","severity":"x","text":"[障害] test-ref 開始 x: [伏せた秘密]","hash":"H","blocked":1,"blocked_reason":"トークン(API キーなどの形式)","sinks":"file stdout","dedupe_key":"incident_opened:test-ref","file_at":"TS","slack_status":"none","slack_attempts":0,"slack_sent_at":null,"slack_msg_id":null,"batch_id":null,"note":null,"created_at":"TS","herdr_session":"default"}'
eq "events --json: no match → nothing" "$($TB notify events --json --status none-such)" ""
eq "tail -n 2" "$($TB notify tail -n 2 | ncts | sed -E 's/N-[0-9]+/N-x/')" $'TS N-x [障害] test-ref 開始 x: [伏せた秘密] [外へは出していない: 秘密を検出]\nTS N-x [通知の警告] N-41(incident_opened)は秘密らしい値(トークン(API キーなどの形式))を含むため、外へ出していません。file には伏せて残しました'
eq "tail -n 0" "$($TB notify tail -n 0)" ""
ncsql "UPDATE notify_events SET slack_status='none';"
export TB_NOTIFY_RATE_PER_MIN=2
for i in 1 2 3; do ncev "d$i" --title "送る $i"; done
eq "dry-run: over the budget, blocked listed" "$($TB notify dry-run | sed -E 's/N-[0-9]+/N-x/g; s/\[[0-9a-f]{12}\]/[H]/')" $'# dry-run: 何も送りません。経路 slack_via=agent / 宛先 未設定 / 今回の枠 2 通\nwould send N-x [H] [障害] d1 開始: 送る 1\nwould send N-x [H] [障害] d2 開始: 送る 2\nwould send N-x [H] [障害] d3 開始: 送る 3\n# 枠を超える分は 1 通にまとめます(個別に送るのは最大 1 件)\nblocked N-x(外へは送りません) [障害] test-ref 開始 x: [伏せた秘密]\nblocked N-x(外へは送りません) [障害] [伏せた秘密] 開始: ref に秘密\nblocked N-x(外へは送りません) [障害] s20 開始 [伏せた秘密]: [伏せた秘密] [伏せた秘密] [伏せた秘密] [伏せた秘密]\nblocked N-x(外へは送りません) [障害] s13 開始: 前 [伏せた秘密] 後\nblocked N-x(外へは送りません) [障害] s12 開始: 前 [伏せた秘密] 後\nblocked N-x(外へは送りません) [障害] s11 開始: 前 [伏せた秘密] 後\nblocked N-x(外へは送りません) [障害] s10 開始: 前 [伏せた秘密] 後\nblocked N-x(外へは送りません) [障害] s9 開始: 前 [伏せた秘密] 後\nblocked N-x(外へは送りません) [障害] s8 開始: 前 [伏せた秘密] 後\nblocked N-x(外へは送りません) [障害] s7 開始: 前 [伏せた秘密] 後\nblocked N-x(外へは送りません) [障害] s6 開始: 前 [伏せた秘密] 後\nblocked N-x(外へは送りません) [障害] s5 開始: 前 [伏せた秘密] 後\nblocked N-x(外へは送りません) [障害] s4 開始: 前 [伏せた秘密] 後\nblocked N-x(外へは送りません) [障害] s3 開始: 前 [伏せた秘密] 後\nblocked N-x(外へは送りません) [障害] s2 開始: 前 [伏せた秘密] 後\nblocked N-x(外へは送りません) [障害] s1 開始: 前 [伏せた秘密] 後'
eq "dry-run: off" "$(TB_NOTIFY_ENABLED=off $TB notify dry-run | head -2)" $'(enabled=off: 今は何も積まれず、何も送られません。下は、積まれているものを送るとしたときの内容)\n# dry-run: 何も送りません。経路 slack_via=agent / 宛先 未設定 / 今回の枠 2 通'
eq "flush: off" "$(TB_NOTIFY_ENABLED=off $TB notify flush)" $'notify: off(何もしません)'

# ---- slack(agent 経由)と ack
export TB_NOTIFY_SLACK_TARGET=CNC TB_NOTIFY_ACK_TIMEOUT_MIN=0
: >"$FAKE_LOG"; $TB notify flush >/dev/null
eq "flush: 1 single + 1 digest to the notifier" "$(grep '^prompt notifier' "$FAKE_LOG" | sed -E 's/N-[0-9]+/N-x/g; s/hash=[0-9a-f]{12}/hash=H/; s/batch-[0-9]+-[0-9]+/batch/')" $'prompt notifier\t[from: tb / notify / N-x / info] POST channel=CNC hash=H text=<<<[障害] d1 開始: 送る 1>>> (本文をそのまま 1 通だけ投稿し、投稿した本文で tb notify hash を出して tb notify ack N-x <hash> を実行)\nprompt notifier\t[from: tb / notify / N-x / info] POST channel=CNC hash=H text=<<<[まとめ] 頻度の上限のため 3 件をまとめました: N-x [障害] d2 開始: 送る 2 | N-x [障害] d3 開始: 送る 3 | N-x [まとめ] 動作中 agent 0 / 進行中タスク 0 / 未クローズのアラート 0 />>> (本文をそのまま 1 通だけ投稿し、投稿した本文で tb notify hash を出して tb notify ack N-x <hash> を実行)'
eq "flush: statuses after" "$(ncsql "SELECT ref||' '||slack_status||' '||slack_attempts||' '||(batch_id IS NOT NULL) FROM notify_events WHERE ref GLOB 'd[0-9]' OR type='digest' ORDER BY id;" | sed -E 's/batch-[0-9]+-[0-9]+/batch/')" $'d1 awaiting-ack 1 0\nd2 batched 0 1\nd3 batched 0 1\nbatch awaiting-ack 1 0'
eq "flush: sent.log line" "$(tail -1 "$TB_NOTIFY_DIR/sent.log" | ncts | sed -E 's/N-[0-9]+/N-x/; s/\t[0-9a-f]{12}\t/\tH\t/')" $'TS\tN-x\tagent\tH\t[まとめ] 頻度の上限のため 3 件をまとめました: N-44 [障害] d2 開始: 送る 2 | N-45 [障害] d3 開始: 送る 3 | N-46 [まとめ] 動作中 agent 0 / 進行中タスク 0 / 未クローズのアラート 0 /'
d1=$(ncsql "SELECT id FROM notify_events WHERE ref='d1';"); h1=$(ncsql "SELECT hash FROM notify_events WHERE id=$d1;")
eq "ack: from a worker" "$(HERDR_PANE_ID=wW:p1 ncerr $TB notify ack "N-$d1" "$h1")" $'tb: ack は notifier agent(notifier)か人だけが使えます(worker1)\nrc=1'
eq "ack: from an unknown pane" "$(HERDR_PANE_ID=wZ:p9 ncerr $TB notify ack "N-$d1" "$h1")" $'tb: pane wZ:p9 に該当する agent がありません。保存しません\nrc=3'
eq "ack: wrong hash" "$(HERDR_PANE_ID=wN:p1 ncerr $TB notify ack "$d1" 0123456789ab) $(ncsql "SELECT slack_status||' '||note FROM notify_events WHERE id=$d1;")" $'N-43 mismatch\nrc=1 mismatch 投稿された本文の hash が一致しない'
eq "ack: right hash, no --id" "$(ncerr $TB notify ack "N-$d1" "$h1") $(ncsql "SELECT slack_status||' '||COALESCE(slack_msg_id,'-')||' '||COALESCE(note,'-') FROM notify_events WHERE id=$d1;")" $'N-43 acked\nrc=0 acked - -'
eq "ack: not awaiting" "$(ncerr $TB notify ack "N-$(ncsql "SELECT id FROM notify_events WHERE ref='f3';")" "$h1" | sed -E 's/N-[0-9]+/N-x/')" $'tb: N-x は ack を待っていません(none)\nrc=1'
eq "test: one fixed line" "$($TB notify test | sed -E 's/^N-[0-9]+/N-x/')" $'N-x awaiting-ack [テスト] tb notify test: 疎通確認の固定文です。返信や操作は要りません'
eq "test: no target → failed" "$(TB_NOTIFY_SLACK_TARGET= $TB notify test | sed -E 's/^N-[0-9]+/N-x/') $(ncsql "SELECT note FROM notify_events WHERE type='test' ORDER BY id DESC LIMIT 1;")" $'N-x failed [テスト] tb notify test: 疎通確認の固定文です。返信や操作は要りません slack_target が未設定'
: >"$FAKE_LOG"; FAKE_STATUS=missing $TB notify flush >/dev/null
eq "flush: notifier missing → attempts counted, note" "$(ncsql "SELECT slack_status||' '||slack_attempts||' '||COALESCE(note,'-') FROM notify_events WHERE type='test' ORDER BY id LIMIT 1;")" $'awaiting-ack 1 -'

# ---- webhook(疑似 curl)
export NC_DIR=$TMP   # 疑似 curl は別のプロセス(TMP は export されていないことがある)
cat >"$TB_CURL" <<'CURL'
#!/usr/bin/env bash
echo "argv: ${*//\/tmp*tb-notify.*/TMP}" | sed -E 's#(/[^ ]*)?tb-notify\.[^ ]*#TMP#g' >>"$NC_DIR/nc-curl.log"
while [[ $# -gt 0 ]]; do case $1 in --data-binary) cat "${2#@}" >>"$NC_DIR/nc-curl.body"; shift 2;; -K) cat "$2" >>"$NC_DIR/nc-curl.cfg"; shift 2;; *) shift;; esac; done
printf '%s' "${NC_CODE-200}"; exit "${NC_RC:-0}"
CURL
chmod +x "$TB_CURL"; : >"$TMP/nc-curl.log"; : >"$TMP/nc-curl.body"; : >"$TMP/nc-curl.cfg"
chmod 600 "$TMP/nc-wh"; export TB_NOTIFY_SLACK_VIA=webhook TB_NOTIFY_WEBHOOK_FILE=$TMP/nc-wh TB_NOTIFY_RATE_PER_MIN=100
ncsql "UPDATE notify_events SET slack_status='none'; DELETE FROM notify_sends;"
ncev w1 --title $'"引用" と \\ と ‹›'
$TB notify flush >/dev/null
eq "webhook: curl argv, body JSON, config" "$(cat "$TMP/nc-curl.log" "$TMP/nc-curl.body" "$TMP/nc-curl.cfg")" $'argv: -sS --max-time 10 -o /dev/null -w %{http_code} -X POST -H Content-Type: application/json --data-binary @TMP -K TMP\n{"text":"[障害] w1 開始: \\"引用\\" と \\\\ と ‹›"}\nurl = "https://example.invalid/x"'
ncev w2 --title rc; NC_RC=7 NC_CODE=000 $TB notify flush >/dev/null
ncev w3 --title empty; NC_CODE= $TB notify flush >/dev/null
printf 'http://example.invalid/x\n' >"$TMP/nc-wh"; ncev w4 --title http; $TB notify flush >/dev/null
printf ' http://127.0.0.1:9/x \r\n' >"$TMP/nc-wh"; ncev w5 --title local; TB_NOTIFY_ALLOW_HTTP=1 $TB notify flush >/dev/null
eq "webhook: failure notes" "$(ncsql "SELECT ref||' '||slack_status||' '||COALESCE(note,'-') FROM notify_events WHERE ref GLOB 'w[2-5]' ORDER BY id;")" $'w2 failed webhook の URL が https ではない\nw3 sent -\nw4 sent -\nw5 sent -'
unset TB_NOTIFY_SLACK_VIA TB_NOTIFY_WEBHOOK_FILE

# ---- 規則(show / test / run)と flush の期限のイベント
cat >"$TB_NOTIFY_RULES" <<'R'
# コメント
status new=done -> file slack lead
created -> stdout
bogus -> file
created priority>=high owner=worker1 -> webui file
label_added label=a#b -> file
* actor=agent -> conductor  # 後ろのコメント
created foo=1 -> file
R
eq "rules show: errors and rules" "$($TB notify rules show | sed "s|$TMP|TMP|")" $'# notify-rules(出どころ: file:TMP/nc-rules.conf)。書くのは人だけ: tb config set notify-rules < 規則のファイル\n# 誤りのある行(飛ばしています):\n#   4 行目: 不明なイベントの種類 bogus(* か created status title type priority owner due parent description label_added label_removed ref_added ref_removed link unlink comment overdue due_soon pr_opened pr_approved pr_changes pr_merged)\n#   8 行目: 不明な条件 foo(type priority label project owner new old actor)\n#   \n2 行目\tstatus new=done -> file slack conductor\n3 行目\tcreated -> stdout\n5 行目\tcreated priority>=high owner=worker1 -> webui file\n6 行目\tlabel_added label=a#b -> file\n7 行目\t* actor=agent -> conductor'
TB_TODAY=2099-01-01 $TB notify flush >/dev/null
eq "rules run: baseline cursor" "$($TB notify rules run | sed -E 's/E-[0-9]+/E-x/')" $'notify rules: 読み位置 E-x'
t=$(HERDR_PANE_ID= $TB add -p nc -t '規則の対象' --priority highest --owner worker1 --due 2098-12-31)
e=$(ncsql "SELECT MAX(id) FROM task_events WHERE kind='created';")
eq "rules test: hit" "$($TB notify rules test "E-$e" | sed -E 's/E-[0-9]+/E-x/')" $'E-x: 届け先 stdout webui file(当たった行: 3,5)。試すだけで、何も積みません'
TB_TODAY=2099-01-01 $TB notify flush >/dev/null
eq "flush: overdue event and the rule's ticket notice" "$(ncsql "SELECT kind||' '||COALESCE(new,'-')||' '||ref FROM task_events WHERE task_id='$t' AND kind='overdue';") | $(ncsql "SELECT sinks||' | '||text FROM notify_events WHERE type='ticket' ORDER BY id;" | sed -E 's/E-[0-9]+/E-x/')" $'overdue 2098-12-31 day:2099-01-01 | stdout webui file | [チケット] T-nc-002 規則の対象 — 作成(todo) / highest / task / 担当 worker1'
eq "rules test: miss" "$($TB notify rules test "E-$(ncsql "SELECT MAX(id) FROM task_events WHERE kind='overdue';")" | sed -E 's/E-[0-9]+/E-x/')" $'E-x: どの規則にも当たりません(送りません)'
rm -f "$TB_NOTIFY_RULES"
eq "rules show: none" "$($TB notify rules)" $'# notify-rules(出どころ: none)。書くのは人だけ: tb config set notify-rules < 規則のファイル\n(規則なし。今の決め打ちの通知だけが動きます)'

unset NC_DIR TB_NOTIFY_ENABLED TB_NOTIFY_DEDUPE_SEC TB_NOTIFY_RATE_PER_MIN TB_NOTIFY_SLACK_TARGET TB_NOTIFY_ACK_TIMEOUT_MIN TB_NOTIFY_CONF TB_NOTIFY_DIR TB_REPOS_FILE TB_CURL TB_NOTIFY_RULES
export TB_DB=$_nc_old_db
if [[ -n $_nc_old_agents ]]; then export FAKE_AGENTS=$_nc_old_agents; else unset FAKE_AGENTS; fi
[[ -z $_saved_pane ]] || export HERDR_PANE_ID=$_saved_pane
