# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- tb pr(queue / watch)の使い方・誤りの文面・終了コード(bash と Go で同じ。期待値は bash の出力から作った)。
# ガード・公開前検査・run・scan の動きは 93_prqueue.sh、watch は 94_notify.sh
_pc_saved=$(env | grep -E '^(TB_DB|HERDR_PANE_ID|FAKE_AGENTS|TB_REPOS_FILE|PR_QUEUE_[A-Z_]+)=' || true)
unset HERDR_PANE_ID PR_QUEUE_CONF PR_QUEUE_GH PR_QUEUE_REPO_DIR PR_QUEUE_BODY_ROOT PR_QUEUE_GIT
export TB_DB=$TMP/prcontract.db TB_REPOS_FILE=$TMP/pc-repos.md
pcerr() { "$@" 2>&1; echo "rc=$?"; }
printf '%s\n' '- org/repo   # 注釈' >"$TB_REPOS_FILE"

# ---- tb pr
eq "pr: no subcommand → usage on stdout, rc 2" "$(pcerr $TB pr)" $'tb pr queue add|list|show|approve|reject|retry|run|scan ...   PR 作成キュー(引数は tb help)\ntb pr watch   許可リストの PR を gh の読み取りだけで見て、新規・approve・変更依頼・マージを積む\n(古い名前 tb pr-queue / tb pr-watch も同じに使える)\nrc=2'
eq "pr help" "$(pcerr $TB pr help)" $'tb pr queue add|list|show|approve|reject|retry|run|scan ...   PR 作成キュー(引数は tb help)\ntb pr watch   許可リストの PR を gh の読み取りだけで見て、新規・approve・変更依頼・マージを積む\n(古い名前 tb pr-queue / tb pr-watch も同じに使える)\nrc=0'
eq "pr: unknown subcommand" "$(pcerr $TB pr bogus)" $'tb: pr: 不明なサブコマンド bogus(queue / watch)\n(tb help で使い方)\nrc=2'
eq "pr watch: no arguments allowed (old name too)" "$(pcerr $TB pr watch x; pcerr $TB pr-watch x)" $'tb: pr-watch(引数なし)\n(tb help で使い方)\nrc=2\ntb: pr-watch(引数なし)\n(tb help で使い方)\nrc=2'
eq "pr watch: notify off → quiet, rc 0" "$(pcerr $TB pr watch)" $'rc=0'

# ---- tb pr queue(古い名前 tb pr-queue も同じ)
eq "pr queue: no subcommand" "$(pcerr $TB pr queue; pcerr $TB pr-queue)" $'tb: pr-queue add|list|show|approve|reject|retry|run|scan\n(tb help で使い方)\nrc=2\ntb: pr-queue add|list|show|approve|reject|retry|run|scan\n(tb help で使い方)\nrc=2'
eq "pr queue: unknown subcommand" "$(pcerr $TB pr queue bogus)" $'tb: pr-queue add|list|show|approve|reject|retry|run|scan\n(tb help で使い方)\nrc=2'
eq "pr queue add: unknown arg / value needed / bad depends-on" "$(pcerr $TB pr queue add --bogus; pcerr $TB pr queue add --branch; pcerr $TB pr queue add --depends-on x)" $'tb: pr-queue add: 不明な引数 --bogus\n(tb help で使い方)\nrc=2\ntb: --branch に値が必要です\n(tb help で使い方)\nrc=2\ntb: pr-queue: ID は PQ-<数字>: x\n(tb help で使い方)\nrc=2'
eq "pr queue add: required args" "$(pcerr $TB pr queue add --branch agent/x)" $'tb: pr-queue add --branch <接頭辞/…> --body-file <body_root の中のファイル> --title <題> [--repo <org/repo>] [--task T-…] [--base <ブランチ> | --depends-on PQ-<n>]\n(tb help で使い方)\nrc=2'
eq "pr queue add: --base with --depends-on" "$(pcerr $TB pr queue add --branch agent/x --body-file f --title t --base main --depends-on 1)" $'tb: pr-queue add: --base と --depends-on は一緒に使えません\n(tb help で使い方)\nrc=2'
eq "pr queue add: bad branch" "$(pcerr $TB pr queue add --branch feat/x --body-file f --title t)" $'tb: pr-queue: ブランチは agent/ のどれかで始め、英数字と . _ - / だけにします: feat/x\nrc=1'
eq "pr queue add: bad title" "$(pcerr $TB pr queue add --branch agent/x --body-file f --title ' ')" $'tb: pr-queue: 題は 1 行・200 字まで\nrc=1'
eq "pr queue add: bad task id" "$(pcerr $TB pr queue add --branch agent/x --body-file f --title t --task x)" $'tb: タスク ID は T-<project>-NNN 形式: x\n(tb help で使い方)\nrc=2'
eq "pr queue add: repo not on the list" "$(pcerr $TB pr queue add --branch agent/x --body-file f --title t --repo a/b)" $'tb: pr-queue: リポジトリが許可リストにありません: a/b\nrc=1'
eq "pr queue add: no clone" "$(pcerr $TB pr queue add --branch agent/x --body-file f --title t)" $'tb: pr-queue: PR_QUEUE_REPO_DIR が空(差分のガードは必須。clone のパスを指定する)\nrc=1'
eq "pr queue list: bad args" "$(pcerr $TB pr queue list --bogus; pcerr $TB pr queue list --status; pcerr $TB pr queue list --status done)" $'tb: pr-queue list [--status S] [--json]\n(tb help で使い方)\nrc=2\ntb: --status に値が必要です\n(tb help で使い方)\nrc=2\ntb: pr-queue: status は pending|approved|rejected|created|failed\n(tb help で使い方)\nrc=2'
eq "pr queue list: empty" "$(pcerr $TB pr queue list; pcerr $TB pr queue list --json)" $'rc=0\nrc=0'
eq "pr queue show: args" "$(pcerr $TB pr queue show; pcerr $TB pr queue show x; pcerr $TB pr queue show PQ-9)" $'tb: pr-queue show <PQ-n>\n(tb help で使い方)\nrc=2\ntb: pr-queue: ID は PQ-<数字>: x\n(tb help で使い方)\nrc=2\ntb: 見つかりません: PQ-9\nrc=1'
eq "pr queue approve: args" "$(pcerr $TB pr queue approve; pcerr $TB pr queue approve PQ-9)" $'tb: pr-queue approve <PQ-n>\n(tb help で使い方)\nrc=2\ntb: 見つかりません: PQ-9\nrc=1'
eq "pr queue approve: from an agent pane" "$(FAKE_AGENTS='[{"pane_id":"wW:p1","name":"worker1"}]' HERDR_PANE_ID=wW:p1 pcerr $TB pr queue approve 1)" $'tb: tb pr-queue approve は人が自分の端末で実行します(agent \'worker1\' の pane からは実行できません)\nrc=1'
eq "pr queue reject: args" "$(pcerr $TB pr queue reject; pcerr $TB pr queue reject x; pcerr $TB pr queue reject 1 -m; pcerr $TB pr queue reject 1 --bogus; pcerr $TB pr queue reject 1)" $'tb: pr-queue reject <PQ-n> [-m 理由]\n(tb help で使い方)\nrc=2\ntb: pr-queue: ID は PQ-<数字>: x\n(tb help で使い方)\nrc=2\ntb: -m に値が必要です\n(tb help で使い方)\nrc=2\ntb: pr-queue reject: 不明な引数 --bogus\n(tb help で使い方)\nrc=2\ntb: 見つかりません: PQ-1\nrc=1'
eq "pr queue reject: long reason" "$(pcerr $TB pr queue reject 1 -m "$(printf 'あ%.0s' {1..301})")" $'tb: pr-queue reject: 理由は 300 字まで\n(tb help で使い方)\nrc=2'
eq "pr queue retry: args" "$(pcerr $TB pr queue retry; pcerr $TB pr queue retry 1 2; pcerr $TB pr queue retry 7)" $'tb: pr-queue retry <PQ-n>\n(tb help で使い方)\nrc=2\ntb: pr-queue retry <PQ-n>\n(tb help で使い方)\nrc=2\ntb: 見つかりません: PQ-7\nrc=1'
eq "pr queue run: bad arg" "$(pcerr $TB pr queue run --now)" $'tb: pr-queue run [--dry-run]\n(tb help で使い方)\nrc=2'
eq "pr queue run: no gh → stops, rc 1" "$(pcerr $TB pr queue run)" $'pr-queue: gh が指定されていないため止めます(fail-closed)。PR_QUEUE_GH か config/pr-queue.conf の gh に、専用の gh ラッパーの絶対パスを指定してください。PATH 上の gh には落としません\nrc=1'
eq "pr queue scan: bad arg / --repo needs a value" "$(pcerr $TB pr queue scan --now; pcerr $TB pr queue scan --repo)" $'tb: pr-queue scan [--dry-run] [--repo <org/repo>]\n(tb help で使い方)\nrc=2\ntb: --repo に値が必要です\n(tb help で使い方)\nrc=2'
eq "pr queue scan: repo not on the list → quiet rc 0" "$(pcerr $TB pr queue scan --repo a/b)" $'pr-queue scan: リポジトリが許可リストに無い\nrc=0'
eq "pr queue scan: no clone → stops, rc 1" "$(pcerr $TB pr queue scan)" $'pr-queue scan: PR_QUEUE_REPO_DIR が空(差分のガードは必須。clone のパスを指定する)。止めます(fail-closed)\nrc=1'
eq "pr: the Go code issues no merge / review / ready / close either" "$(cat "$HERE/../../internal/pr/"*.go | grep -cE 'pr (merge|review|ready|close)|--approve|"(merge|ready|close)"')" "0"

while IFS= read -r _l; do [[ -n $_l ]] && export "$_l"; done <<<"$_pc_saved"
unset _pc_saved _l
