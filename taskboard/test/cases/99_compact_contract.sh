# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- tb compact-check の使い方・文面・終了コード・記録(bash と Go で同じ。期待値は bash の出力から作った)。DB は自分のものを使う
_cc_saved=$(declare -p HERDR_PANE_ID TB_DB TB_POLICY_A TB_COMPACT_CONF TB_CLAUDE_PROJECTS TB_COMPACT_ENABLED TB_COMPACT_THRESHOLD_TOKENS FAKE_AGENTS FAKE_STATUS FAKE_PROMPT_RC FAKE_PROMPT_MODE TB_PROMPT_VERIFY_SEC TB_VERIFY_POLL_SEC COMPACT_TAIL_BYTES 2>/dev/null || true)
unset HERDR_PANE_ID TB_COMPACT_ENABLED TB_COMPACT_THRESHOLD_TOKENS FAKE_STATUS FAKE_PROMPT_RC FAKE_PROMPT_MODE COMPACT_TAIL_BYTES FAKE_DYNAMIC
export TB_DB=$TMP/cc.db TB_POLICY_A=$TMP/cc-appr.conf TB_COMPACT_CONF=$TMP/cc.conf TB_CLAUDE_PROJECTS=$TMP/cc-projects
export TB_PROMPT_VERIFY_SEC=0 TB_VERIFY_POLL_SEC=0
rm -rf "$TB_CLAUDE_PROJECTS"; mkdir -p "$TB_CLAUDE_PROJECTS/b-proj" "$TB_CLAUDE_PROJECTS/a-proj" "$TB_CLAUDE_PROJECTS/.hidden"
ccag() { printf '{"pane_id":"w%s:p1","name":"%s","agent_status":"%s","agent_session":{"value":"%s"}}' "$1" "$1" "${2:-idle}" "${3-s-$1}"; }
cctr() { printf '%s\n' "${@:2}" >"$TB_CLAUDE_PROJECTS/${CCDIR:-a-proj}/$1.jsonl"; }
cctr s-big '{"message":{"usage":{"input_tokens":1,"cache_creation_input_tokens":1,"cache_read_input_tokens":1}}}' '{"message":{"usage":{"input_tokens":100,"cache_creation_input_tokens":200,"cache_read_input_tokens":200000}}}' '{"type":"user"}'
cctr s-top '{"usage":{"input_tokens":160000}}'
cctr s-nulls '{"message":{"usage":{"input_tokens":null,"cache_read_input_tokens":170000}}}'
cctr s-bad '{"message":"text","usage":{"input_tokens":999999}}'
cctr s-none '{"type":"user"}'
cctr s-small '{"usage":{"input_tokens":10}}'
cctr s-cod '{"usage":{"input_tokens":999999}}'
cctr s-led '{"usage":{"input_tokens":180000}}'
cctr s-done '{"usage":{"input_tokens":190000}}'
CCDIR=b-proj cctr s-big '{"usage":{"input_tokens":5}}'
printf '{"usage":{"input_tokens":5}}\n' >"$TB_CLAUDE_PROJECTS/.hidden/s-small.jsonl"
export FAKE_AGENTS="[$(ccag conductor),$(ccag big),$(ccag top),$(ccag nulls),$(ccag bad),$(ccag none),$(ccag small),$(ccag cod),$(ccag led idle ''),$(ccag busy working),$(ccag odd blocked),$(ccag done done),$(ccag skipme),{\"pane_id\":\"wx:p1\",\"agent_status\":\"idle\"}]"
$TB agents >/dev/null 2>&1
sqlite3 "$TB_DB" "UPDATE agents SET kind='codex' WHERE name='cod'; UPDATE agents SET session_id='s-led' WHERE name='led';"
printf '* agent:compact done = ask\n* agent:compact * = allow\n' >"$TB_POLICY_A"
ccerr() { "$@" 2>&1; echo "rc=$?"; }

eq "compact: bad arg" "$(ccerr $TB compact-check --nope)" $'tb: compact-check: 不明な引数 --nope\n(tb help で使い方)\nrc=2'
eq "compact: no config" "$(rm -f "$TB_COMPACT_CONF"; ccerr $TB compact-check)" $'compact-check: disabled (config/compact.conf の enabled = 1 で有効)\nrc=0'
printf '# c\nenabled = 0\n' >"$TB_COMPACT_CONF"
eq "compact: disabled" "$(ccerr $TB compact-check --apply)" $'compact-check: disabled (config/compact.conf の enabled = 1 で有効)\nrc=0'
eq "compact: env enables" "$(TB_COMPACT_ENABLED=1 ccerr $TB compact-check | head -3)" $'conductor - skip(excluded)\nbig 200300 would-compact(dry-run)\ntop 160000 would-compact(dry-run)'
printf 'enabled = 1 # on\nthreshold_tokens = 150000\nmin_interval_sec = 600\nexclude = skipme conductor\n' >"$TB_COMPACT_CONF"
eq "compact: bad threshold" "$(TB_COMPACT_THRESHOLD_TOKENS=1e5 ccerr $TB compact-check)" $'tb: compact.conf の threshold_tokens / min_interval_sec は 0 以上の整数\nrc=1'
eq "compact: bad interval" "$(printf 'enabled=1\nmin_interval_sec = -1\n' >"$TMP/cc2.conf"; TB_COMPACT_CONF=$TMP/cc2.conf ccerr $TB compact-check)" $'tb: compact.conf の threshold_tokens / min_interval_sec は 0 以上の整数\nrc=1'
eq "compact: list fails" "$(FAKE_LIST_FAIL=1 ccerr $TB compact-check)" $'tb: herdr agent list に失敗しました\nrc=1'
eq "compact: dry-run" "$(ccerr $TB compact-check --apply --dry-run)" $'conductor - skip(excluded)\nbig 200300 would-compact(dry-run)\ntop 160000 would-compact(dry-run)\nnulls 170000 would-compact(dry-run)\nbad - skip(no transcript usage)\nnone - skip(no transcript usage)\nsmall 10 skip(below threshold 150000)\ncod - skip(kind=codex: compact 非対応)\nled 180000 would-compact(dry-run)\nbusy - skip(working)\nodd - skip(blocked)\ndone 190000 skip(approval: agent:compact:done is ask; add \'* agent:compact * = allow\' to policy)\nskipme - skip(excluded)\nrc=0'
eq "compact: from an agent pane skips itself" "$(HERDR_PANE_ID=wbig:p1 ccerr $TB compact-check | grep '^big')" $'big - skip(excluded)'
: >"$FAKE_LOG"
eq "compact: apply" "$(ccerr $TB compact-check --apply)" $'conductor - skip(excluded)\nbig 200300 sent\ntop 160000 sent\nnulls 170000 sent\nbad - skip(no transcript usage)\nnone - skip(no transcript usage)\nsmall 10 skip(below threshold 150000)\ncod - skip(kind=codex: compact 非対応)\nled 180000 sent\nbusy - skip(working)\nodd - skip(blocked)\ndone 190000 skip(approval: agent:compact:done is ask; add \'* agent:compact * = allow\' to policy)\nskipme - skip(excluded)\nrc=0'
eq "compact: sent" "$(grep '^prompt ' "$FAKE_LOG")" $'prompt big\t/compact\nprompt top\t/compact\nprompt nulls\t/compact\nprompt led\t/compact'
eq "compact: apply again is held by min_interval" "$(ccerr $TB compact-check --apply | grep -v ' - ' | sed 's/compacted [0-9]*s ago/compacted Ns ago/')" $'big 200300 skip(compacted Ns ago, min_interval 600s)\ntop 160000 skip(compacted Ns ago, min_interval 600s)\nnulls 170000 skip(compacted Ns ago, min_interval 600s)\nsmall 10 skip(below threshold 150000)\nled 180000 skip(compacted Ns ago, min_interval 600s)\ndone 190000 skip(approval: agent:compact:done is ask; add \'* agent:compact * = allow\' to policy)\nrc=0'
printf 'enabled = 1\nmin_interval_sec = 0\nexclude =\n' >"$TB_COMPACT_CONF"
eq "compact: send fails" "$(FAKE_PROMPT_RC=3 ccerr $TB compact-check --apply | grep -E '^(big|conductor|skipme) ')" $'conductor - skip(excluded)\nbig 200300 skip(busy or send failed)\nskipme - skip(no transcript usage)'
eq "compact: not started" "$(TB_PROMPT_VERIFY_SEC=1 ccerr $TB compact-check --apply | grep -E '^(big|top) ')" $'big 200300 sent (処理の開始を確認できない。pane を見てください)\ntop 160000 sent (処理の開始を確認できない。pane を見てください)'
eq "compact: started" "$(FAKE_PROMPT_MODE=work TB_PROMPT_VERIFY_SEC=1 ccerr $TB compact-check --apply | grep -E '^big '; rm -f "$FAKE_LOG.working")" $'big 200300 sent'
eq "compact: tail fallback" "$(COMPACT_TAIL_BYTES=20 ccerr $TB compact-check | grep -E '^(big|top) ')" $'big 200300 would-compact(dry-run)\ntop 160000 would-compact(dry-run)'
eq "compact: records" "$(sqlite3 "$TB_DB" "SELECT op, target, args, requested_by, status, result FROM agent_ops ORDER BY id;")" $'compact|big|{"tokens":200300}|human|done|compact sent at 200300 tokens\ncompact|top|{"tokens":160000}|human|done|compact sent at 160000 tokens\ncompact|nulls|{"tokens":170000}|human|done|compact sent at 170000 tokens\ncompact|led|{"tokens":180000}|human|done|compact sent at 180000 tokens\ncompact|big|{"tokens":200300}|human|done|compact sent at 200300 tokens (処理の開始を確認できない。pane を見てください)\ncompact|top|{"tokens":160000}|human|done|compact sent at 160000 tokens (処理の開始を確認できない。pane を見てください)\ncompact|nulls|{"tokens":170000}|human|done|compact sent at 170000 tokens (処理の開始を確認できない。pane を見てください)\ncompact|led|{"tokens":180000}|human|done|compact sent at 180000 tokens (処理の開始を確認できない。pane を見てください)\ncompact|big|{"tokens":200300}|human|done|compact sent at 200300 tokens'
eq "compact: messages" "$(sqlite3 "$TB_DB" "SELECT kind, ref, target, body, result FROM messages ORDER BY id;")" $'compact|compact:big|big|/compact|sent\ncompact|compact:top|top|/compact|sent\ncompact|compact:nulls|nulls|/compact|sent\ncompact|compact:led|led|/compact|sent\ncompact|compact:big|big|/compact|failed\ncompact|compact:top|top|/compact|failed\ncompact|compact:nulls|nulls|/compact|failed\ncompact|compact:led|led|/compact|failed\ncompact|compact:big|big|/compact|sent\ncompact|compact:top|top|/compact|sent\ncompact|compact:nulls|nulls|/compact|sent\ncompact|compact:led|led|/compact|sent\ncompact|compact:big|big|/compact|sent\ncompact|compact:top|top|/compact|busy\ncompact|compact:nulls|nulls|/compact|busy\ncompact|compact:led|led|/compact|busy'

unset HERDR_PANE_ID TB_DB TB_POLICY_A TB_COMPACT_CONF TB_CLAUDE_PROJECTS TB_COMPACT_ENABLED TB_COMPACT_THRESHOLD_TOKENS FAKE_AGENTS FAKE_STATUS FAKE_PROMPT_RC FAKE_PROMPT_MODE TB_PROMPT_VERIFY_SEC TB_VERIFY_POLL_SEC COMPACT_TAIL_BYTES
eval "$_cc_saved"
