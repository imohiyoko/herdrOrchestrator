# shellcheck shell=bash
# merge / approve の判定の口(段 0: 判定は読み取りだけ。実行の口は常に無効)
# bin/tb から source される。単体でも source できる(テスト用。依存は bash だけ)。
# 設計と入力の形: docs/gate.md。決定論(時刻・乱数・LLM・ネットワークを使わない)。理由は寄与を 1 件ずつ列挙する。
#
# 設定ファイルの場所は固定。env では差し替えられない(agent が自分の許可ファイルで評価させる迂回を作らない)。
# テストは、この lib を source したシェルの中で GATE_CONF / GATE_PATHS を代入して差し替える(tb 本体は別プロセスなので env は効かない)。
GATE_CONF="${TB_ROOT:-.}/config/gate.conf"
GATE_PATHS="${TB_ROOT:-.}/config/gate-paths.conf"
# レビュー結果の記録(追記専用の定型ファイル。tb review-submit だけが追記する)。場所は固定で、env では差し替えられない
GATE_REVIEWS="${TB_ROOT:-.}/data/gate-reviews.log"
# 外部レビューの取り込み(tb gate ingest-review)の設定。場所は固定で、env では差し替えられない
GATE_INGEST_CONF="${TB_ROOT:-.}/config/gate-ingest.conf"
# 取り込み口の送信者名(固定)。agent が review-submit では名乗れない。外部系統の記録は、この送信者のものだけを数える
GATE_INGEST_SENDER="ingest"
# 全レビューの身元つきの記録(段 1c。追記専用。判定の入力には使わない)。場所は固定で、env では差し替えられない
GATE_EVENTS="${TB_ROOT:-.}/data/gate-review-events.log"
# agent の pane から review-submit できる役割(固定。roles/ の固定の置き場と台帳から決める。env では変えられない)
GATE_REVIEW_ROLES=" reviewer "
# 外部系統(agent が名乗れない系統)。scheduler などの非 agent の経路だけが記録する
GATE_RESERVED_LINEAGES=" coderabbit "

# しきい値の上限(これより緩い設定は読み込んで警告し、上限のままにする)
GATE_CAP_APPROVE=2
GATE_CAP_MERGE=1

# 設定の有無にかかわらず効くパス規則(設定ファイルで外せない。判定・承認・ゲートの設定そのもの)
# .github(末尾なし)はパスそのものの変更用。bin/tb(全コマンドの入口)とテスト基盤・gate のテストの変更も、判定を弱める経路なので常に人に回す
# 基準: 「ガバナンス・権限・コードの取得や実行の入口」。どの階層でも効く(**/ は根直下にも当たる)。大文字小文字は区別しない
GATE_BUILTIN_PATHS=('.github' '.github/**' '**/.github' '**/.github/**' '**/gate*.conf' '**/gate-paths.conf' '**/gate*.sh' '**/approvals.conf' '**/risk-rules.conf'
  '**/bin/tb' '**/run-all.sh' '**/test/run.sh' '**/tests/run.sh' '**/test/cases/*gate*' '**/tests/*gate*'
  # agent への指示・権限・拡張を決めるファイル(振る舞いを変える設定)。どの階層でも
  '**/.claude' '**/.claude/**' '**/CLAUDE.md' '**/CLAUDE.local.md' '**/AGENTS.md' '**/GEMINI.md' '**/copilot-instructions.md'
  '**/.cursor' '**/.cursor/**' '**/.cursorrules' '**/.windsurf' '**/.windsurf/**' '**/.windsurfrules' '**/.clinerules*' '**/.roo' '**/.roo/**'
  '**/.claude.json' '**/.agents' '**/.agents/**' '**/AGENT.md' '**/.aider.conf.*'
  '**/.continue' '**/.continue/**' '**/.codex' '**/.codex/**' '**/.gemini' '**/.gemini/**' '**/mcp.json' '**/.vscode/tasks.json' '**/.vscode/settings.json'
  # レビュー必須の規則とレビューの設定(GitHub は CODEOWNERS をルート・docs/・.github/ のどこからも読む)
  '**/CODEOWNERS' '**/.coderabbit.yaml'
  # 実行の入口: git の hook・filter・submodule、コミット前検査、起動時に実行される設定、CI の設定
  '.githooks' '**/.githooks/**' '.husky' '**/.husky/**' '**/.gitattributes' '**/.gitmodules' '**/.pre-commit-config.yaml'
  '**/.envrc' '**/.mcp.json' '**/.devcontainer/**' '**/.circleci/**' '**/Jenkinsfile'
  # ビルド・実行の入口
  '**/Makefile' '**/GNUmakefile' '**/Dockerfile*' '**/*.dockerfile' '**/docker-compose*.yml' '**/docker-compose*.yaml'
  # 依存定義と lockfile(コードの取得の入口)と、取得先の設定
  '**/package.json' '**/package-lock.json' '**/yarn.lock' '**/pnpm-lock.yaml' '**/bun.lockb' '**/.npmrc' '**/.yarnrc*'
  '**/requirements*.txt' '**/Pipfile' '**/Pipfile.lock' '**/poetry.lock' '**/uv.lock' '**/pyproject.toml' '**/setup.py' '**/setup.cfg'
  '**/pip.conf' '**/pip.ini' '**/.pip/**' '**/.pypirc'
  '**/go.mod' '**/go.sum' '**/go.work' '**/go.work.sum' '**/Cargo.toml' '**/Cargo.lock'
  '**/Gemfile' '**/Gemfile.lock' '**/composer.json' '**/composer.lock' '**/pom.xml' '**/build.gradle*'
  '**/Podfile' '**/Podfile.lock' '**/Package.swift' '**/Package.resolved' '**/mix.exs' '**/mix.lock' '**/pubspec.yaml' '**/pubspec.lock'
  '**/build.sbt' '**/deno.json' '**/deno.jsonc' '**/deno.lock' '**/environment.yml' '**/flake.nix' '**/flake.lock' '**/Brewfile*' '**/.terraform.lock.hcl'
  # 取得先・ラッパー・ツール設定(取得先の差し替えや、取得したものの実行につながる)
  '**/.cargo/config' '**/.cargo/config.*' '**/.bundle/config' '**/.gemrc' '**/bunfig.toml' '**/.yarn/**' '**/pnpm-workspace.yaml'
  '**/gradlew' '**/gradlew.bat' '**/mvnw' '**/mvnw.cmd' '**/.mvn/**' '**/gradle.properties' '**/settings.gradle*'
  # テスト・タスクの実行の入口(テスト実行時や task 実行時に読み込まれる)
  '**/conftest.py' '**/tox.ini' '**/noxfile.py' '**/justfile' '**/.justfile' '**/Taskfile.yml' '**/Taskfile.yaml' '**/Procfile'
  '**/lefthook.yml' '**/lefthook.yaml' '**/.lefthook.yml' '**/.lefthook.yaml' '**/.pre-commit-hooks.yaml'
  # ツールのバージョン・タスク・hook の定義(mise の設定の別名を含む)
  '**/.tool-versions' '**/mise.toml' '**/.mise.toml' '**/mise.*.toml' '**/.mise.*.toml' '**/.mise' '**/.mise/**' '**/mise/config.toml' '**/.config/mise/**'
  '**/.bazelrc' '**/.bazelrc.*' '**/MODULE.bazel' '**/WORKSPACE' '**/WORKSPACE.bazel'
  # github 以外の CI・依存の自動更新の設定
  '**/.gitlab-ci.yml' '**/.gitlab/**' '**/.travis.yml' '**/azure-pipelines*.yml' '**/azure-pipelines*.yaml' '**/.azure-pipelines/**' '**/bitbucket-pipelines.yml'
  '**/cloudbuild.yaml' '**/cloudbuild.yml' '**/appveyor.yml' '**/.appveyor.yml' '**/.drone.yml' '**/buildspec*.yml' '**/buildspec*.yaml'
  '**/renovate.json' '**/renovate.json5' '**/.renovaterc' '**/.renovaterc.*' '**/.dependabot/**')

_gate_trim() { local s=$1; s="${s#"${s%%[![:space:]]*}"}"; printf '%s' "${s%"${s##*[![:space:]]}"}"; }

# ---------- 設定 ----------
# 成功で 0。失敗は GATE_ERR に理由を入れて 1(評価不能 = 21)
GATE_ERR=""
_gate_load_conf() {
  local LC_ALL=C
  GATE_ERR=""; GATE_WARNS=()
  GATE_MODE=""; GATE_REPOS=(); GATE_TH_APPROVE=""; GATE_TH_MERGE=""
  local f=$GATE_CONF
  [[ -e $f ]] || { GATE_ERR="設定ファイルがありません(config/gate.conf)"; return 1; }
  [[ -f $f && -r $f ]] || { GATE_ERR="設定ファイルを読めません"; return 1; }
  local sec="" line n=0 k v item items
  while IFS= read -r line || [[ -n $line ]]; do
    n=$((n+1)); line=$(_gate_trim "${line%%#*}")
    [[ -z $line ]] && continue
    if [[ $line =~ ^\[([a-z-]+)\]$ ]]; then
      sec=${BASH_REMATCH[1]}
      case $sec in mode|repos|pr-kinds|threshold|limits) ;; *) GATE_ERR="L$n: 未知の節 [$sec]"; return 1;; esac
      continue
    fi
    [[ -n $sec ]] || { GATE_ERR="L$n: 節([mode] など)の前に行があります"; return 1; }
    [[ $line == *=* ]] || { GATE_ERR="L$n: 'キー = 値' の形ではありません"; return 1; }
    k=$(_gate_trim "${line%%=*}"); v=$(_gate_trim "${line#*=}")
    case $sec.$k in
      mode.state)
        [[ $v == disabled || $v == shadow || $v == enabled ]] || { GATE_ERR="L$n: state は disabled | shadow | enabled です: $v"; return 1; }
        GATE_MODE=$v;;
      repos.allow)
        GATE_REPOS=()
        if [[ -n $v ]]; then
          IFS=',' read -r -a items <<<"$v"
          for item in "${items[@]}"; do
            item=$(_gate_trim "$item")
            [[ $item =~ ^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$ ]] || { GATE_ERR="L$n: allow は <owner>/<repo> のコンマ区切りです: $item"; return 1; }
            GATE_REPOS+=("$item")
          done
        fi;;
      pr-kinds.approve|pr-kinds.merge)
        IFS=',' read -r -a items <<<"$v"
        for item in "${items[@]:-}"; do
          item=$(_gate_trim "$item")
          [[ $item == docs-only || $item == tests-only || $item == code || $item == mixed ]] || { GATE_ERR="L$n: PR の種類は docs-only | tests-only | code | mixed です: $item"; return 1; }
        done;;
      threshold.approve|threshold.merge)
        [[ $v =~ ^[0-9]+$ && ${#v} -le 2 ]] || { GATE_ERR="L$n: しきい値は 0 以上 99 以下の整数です: $v"; return 1; }
        if [[ $k == approve ]]; then GATE_TH_APPROVE=$((10#$v)); else GATE_TH_MERGE=$((10#$v)); fi;;
      limits.merge-per-day|limits.merge-min-interval-min|limits.approve-per-day)
        [[ $v =~ ^[0-9]+$ && ${#v} -le 4 ]] || { GATE_ERR="L$n: 上限は整数です: $k = $v"; return 1; };;
      *) GATE_ERR="L$n: 未知のキー [$sec] $k"; return 1;;
    esac
  done <"$f"
  [[ -n $GATE_MODE ]] || { GATE_ERR="[mode] state がありません"; return 1; }
  [[ -n $GATE_TH_APPROVE ]] || { GATE_ERR="[threshold] approve がありません"; return 1; }
  [[ -n $GATE_TH_MERGE ]] || { GATE_ERR="[threshold] merge がありません"; return 1; }
  if ((GATE_TH_APPROVE > GATE_CAP_APPROVE)); then
    GATE_WARNS+=("緩め設定は無効: [threshold] approve = $GATE_TH_APPROVE(上限 $GATE_CAP_APPROVE のまま)"); GATE_TH_APPROVE=$GATE_CAP_APPROVE
  fi
  if ((GATE_TH_MERGE > GATE_CAP_MERGE)); then
    GATE_WARNS+=("緩め設定は無効: [threshold] merge = $GATE_TH_MERGE(上限 $GATE_CAP_MERGE のまま)"); GATE_TH_MERGE=$GATE_CAP_MERGE
  fi
  return 0
}

# パス規則(gate-paths.conf)。ファイルが無い・壊れている場合は評価不能。組み込みの規則は常に加わる
GATE_PATH_PATS=()
_gate_load_paths() {
  local LC_ALL=C
  GATE_PATH_PATS=("${GATE_BUILTIN_PATHS[@]}")
  local f=$GATE_PATHS
  [[ -e $f ]] || { GATE_ERR="パス規則ファイルがありません(config/gate-paths.conf)"; return 1; }
  [[ -f $f && -r $f ]] || { GATE_ERR="パス規則ファイルを読めません"; return 1; }
  local line n=0
  while IFS= read -r line || [[ -n $line ]]; do
    n=$((n+1)); line=$(_gate_trim "${line%%#*}")
    [[ -z $line ]] && continue
    [[ $line =~ ^[A-Za-z0-9._*?/@-]+$ && $line != /* ]] || { GATE_ERR="gate-paths L$n: パターンに使えない文字、または絶対パスです: $line"; return 1; }
    GATE_PATH_PATS+=("$line")
  done <"$f"
  return 0
}

# パスが規則に当たるか。当たったパターンを GATE_HIT_PAT に入れる。大文字小文字は区別しない。
# glob の * は / も越える(広く当たる側。人に回る側へ倒す)。**/ で始まるパターンは先頭の階層なしにも当たる
_gate_path_hit() {
  local LC_ALL=C p=$1 pat g had=0
  GATE_HIT_PAT=""
  shopt -q nocasematch && had=1
  shopt -s nocasematch
  for pat in "${GATE_PATH_PATS[@]}"; do
    g=${pat//\*\*/\*}
    # shellcheck disable=SC2053  # glob として比べる
    if [[ $p == $g ]]; then GATE_HIT_PAT=$pat; break; fi
    if [[ $pat == '**/'* ]]; then
      g=${pat#'**/'}; g=${g//\*\*/\*}
      # shellcheck disable=SC2053
      if [[ $p == $g ]]; then GATE_HIT_PAT=$pat; break; fi
    fi
  done
  ((had)) || shopt -u nocasematch
  [[ -n $GATE_HIT_PAT ]]
}

# ---------- 入力(記録済みの事実の定型行) ----------
# HEAD <sha> / PR <owner>/<repo>#<n>(REVIEW は入力では受けず、記録 GATE_REVIEWS から読む)
# TEST exit=<n> sha=<sha> / TEST none / PUBLIC-CHECK clean|dirty|unavailable / FILE <A|M|D|T> <path> / FILE R <旧> <新>
# SIZE <変更行数> <全行数> / AUTHOR <id> / EXECUTOR <id> / AUTHOR-REJECT-RATE low|mid|high|unknown
# 入力の正規形に頼らない: 空・.・.. の階層(先頭の ./、連続スラッシュ、末尾スラッシュ、a/../b を含む)、先頭の / は不正(評価不能)。
# 大文字小文字は規則の側で区別しない。シンボリックリンクは文字列からは分からないので、記録する側が FILE T で渡す前提(docs/gate.md)
_gate_path_ok() {
  local seg; local IFS=/
  [[ $1 =~ ^[A-Za-z0-9._@/+-]+$ && $1 != /* && $1 != */ ]] || return 1
  for seg in $1; do [[ -n $seg && $seg != . && $seg != .. ]] || return 1; done
  return 0
}

# _gate_parse <行>...  → G_* と GATE_INERRS(入力の不足・不正の一覧)
_gate_parse() {
  local LC_ALL=C line key a b c d rest kv
  G_HEAD=""; G_PR=""; G_TEST=""; G_TEST_EXIT=""; G_TEST_SHA=""; G_PUBLIC=""; G_SIZE_CH=""; G_SIZE_TOT=""
  G_AUTHOR=""; G_EXECUTOR=""; G_REJECT=""
  G_FILES=(); GATE_INERRS=()
  local seen=" "
  for line in "$@"; do
    line=$(_gate_trim "$line")
    [[ -z $line || $line == \#* ]] && continue
    read -r key rest <<<"$line"
    case $key in
      HEAD|PR|TEST|PUBLIC-CHECK|SIZE|AUTHOR|EXECUTOR|AUTHOR-REJECT-RATE)
        if [[ $seen == *" $key "* ]]; then GATE_INERRS+=("$key が重複しています"); continue; fi
        seen+="$key ";;
    esac
    case $key in
      HEAD) [[ $rest =~ ^[0-9a-f]{7,40}$ ]] && G_HEAD=$rest || GATE_INERRS+=("HEAD の形が不正です");;
      PR) [[ $rest =~ ^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+#[0-9]+$ ]] && G_PR=$rest || GATE_INERRS+=("PR の形が不正です(<owner>/<repo>#<n>)");;
      REVIEW) GATE_INERRS+=("REVIEW は入力では受け付けません(記録 tb review-submit から読みます)");;
      TEST)
        if [[ $rest == none ]]; then G_TEST=none
        else
          local extra
          read -r a b extra <<<"$rest"
          if [[ -n ${extra:-} || ! ${a:-} =~ ^exit=[0-9]{1,3}$ || ! ${b:-} =~ ^sha=[0-9a-f]{7,40}$ ]]; then GATE_INERRS+=("TEST の形が不正です")
          else G_TEST=run; G_TEST_EXIT=$((10#${a#exit=})); G_TEST_SHA=${b#sha=}; fi
        fi;;
      PUBLIC-CHECK) [[ $rest == clean || $rest == dirty || $rest == unavailable ]] && G_PUBLIC=$rest || GATE_INERRS+=("PUBLIC-CHECK は clean | dirty | unavailable です");;
      FILE)
        local extra
        read -r a b c extra <<<"$rest"
        if [[ -n ${extra:-} ]]; then GATE_INERRS+=("FILE の欄が多すぎます")
        elif [[ $a == R ]]; then
          if [[ -n ${b:-} && -n ${c:-} ]] && _gate_path_ok "$b" && _gate_path_ok "$c"; then G_FILES+=("R $b $c"); else GATE_INERRS+=("FILE R は旧パスと新パスの両方が要り、パスは相対で、空・.・.. の階層を含みません"); fi
        elif [[ $a =~ ^[AMDT]$ ]]; then
          if [[ -n ${b:-} && -z ${c:-} ]] && _gate_path_ok "$b"; then G_FILES+=("$a $b"); else GATE_INERRS+=("FILE $a のパスが不正です(相対パスで、空・.・.. の階層を含まない 1 つ)"); fi
        else GATE_INERRS+=("FILE の種別は A | M | D | R | T です"); fi;;
      SIZE)
        read -r a b <<<"$rest"
        if [[ ${a:-} =~ ^[0-9]{1,9}$ && ${b:-} =~ ^[0-9]{1,9}$ && $((10#${b:-0})) -ge 1 ]]; then G_SIZE_CH=$((10#$a)); G_SIZE_TOT=$((10#$b)); else GATE_INERRS+=("SIZE は <変更行数> <全行数(1 以上)> です"); fi;;
      AUTHOR) [[ $rest =~ ^[A-Za-z0-9._-]{1,64}$ ]] && G_AUTHOR=$rest || GATE_INERRS+=("AUTHOR の形が不正です");;
      EXECUTOR) [[ $rest =~ ^[A-Za-z0-9._-]{1,64}$ ]] && G_EXECUTOR=$rest || GATE_INERRS+=("EXECUTOR の形が不正です");;
      AUTHOR-REJECT-RATE) [[ $rest == low || $rest == mid || $rest == high || $rest == unknown ]] && G_REJECT=$rest || GATE_INERRS+=("AUTHOR-REJECT-RATE は low | mid | high | unknown です");;
      *) GATE_INERRS+=("未知の行: $(printf %s "${key:0:32}" | tr -cd 'A-Za-z0-9._#/=-')");;
    esac
  done
  # 欠けている入力(取れなかった = 評価不能)
  [[ -n $G_HEAD ]] || GATE_INERRS+=("HEAD がありません")
  [[ -n $G_PR ]] || GATE_INERRS+=("PR がありません")
  [[ -n $G_TEST ]] || GATE_INERRS+=("TEST がありません(未実行は 'TEST none')")
  [[ -n $G_PUBLIC ]] || GATE_INERRS+=("PUBLIC-CHECK がありません")
  [[ -n $G_SIZE_TOT ]] || GATE_INERRS+=("SIZE がありません")
  [[ -n $G_AUTHOR ]] || GATE_INERRS+=("AUTHOR がありません")
  [[ -n $G_EXECUTOR ]] || GATE_INERRS+=("EXECUTOR がありません")
  [[ -n $G_REJECT ]] || GATE_INERRS+=("AUTHOR-REJECT-RATE がありません")
  ((${#G_FILES[@]})) || GATE_INERRS+=("FILE がありません")
  return 0
}

# ---------- レビュー結果の記録 ----------
# 1 行: ts=<UTC> task=<id> by=<送信者> pr=<o/r#n|-> REVIEW <系統> <sha> blocker=<n> should=<n> verdict=pass|fail evidence=<path|->
GATE_REC_RE='^ts=([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z) task=([A-Za-z0-9._-]{1,64}) by=([A-Za-z0-9._-]{1,64}) pr=(-|[A-Za-z0-9._-]+/[A-Za-z0-9._-]+#[0-9]+) REVIEW ([a-z][a-z0-9-]{0,31}) ([0-9a-f]{7,40}) blocker=([0-9]{1,4}) should=([0-9]{1,4}) verdict=(pass|fail) evidence=(-|[A-Za-z0-9._/@+~-]{1,200})$'

# _gate_load_reviews <chain> <head> <pr>: 全行を厳密に読む。形式違反の行が 1 行でもあれば失敗(GATE_ERR)。
# R_* に、この chain の記録を入れる(R_OK=1: head の sha と一致し pr も矛盾しない = 有効 / 0 = 無効。理由は R_NOTE)
_gate_load_reviews() {
  local LC_ALL=C chain=$1 head=$2 pr=$3 line n=0
  R_TS=(); R_BY=(); R_LIN=(); R_SHA=(); R_BLK=(); R_SHD=(); R_VER=(); R_OK=(); R_NOTE=(); R_CNT=()
  local f=$GATE_REVIEWS
  [[ -e $f ]] || { GATE_ERR="レビューの記録がありません(data/gate-reviews.log)"; return 1; }
  [[ -f $f && ! -L $f && -r $f ]] || { GATE_ERR="レビューの記録を読めません(通常のファイルではない)"; return 1; }
  while IFS= read -r line || [[ -n $line ]]; do
    n=$((n+1))
    [[ -z $line ]] && continue
    [[ $line =~ $GATE_REC_RE ]] || { GATE_ERR="レビューの記録の形式違反(L$n)"; return 1; }
    [[ ${BASH_REMATCH[2]} == "$chain" ]] || continue
    R_TS+=("${BASH_REMATCH[1]}"); R_BY+=("${BASH_REMATCH[3]}"); R_LIN+=("${BASH_REMATCH[5]}"); R_SHA+=("${BASH_REMATCH[6]}")
    R_BLK+=("$((10#${BASH_REMATCH[7]}))"); R_SHD+=("$((10#${BASH_REMATCH[8]}))"); R_VER+=("${BASH_REMATCH[9]}")
    local cnt=1 cnote=""
    if [[ ${BASH_REMATCH[3]} == human ]]; then cnt=0; cnote="独立系統には数えない: 送信者 human"
    elif [[ $GATE_RESERVED_LINEAGES == *" ${BASH_REMATCH[5]} "* && ${BASH_REMATCH[3]} != "$GATE_INGEST_SENDER" ]]; then
      cnt=0; cnote="独立系統には数えない: 外部系統は送信者 $GATE_INGEST_SENDER の記録だけが数えられる(自称)"
    elif [[ $GATE_RESERVED_LINEAGES != *" ${BASH_REMATCH[5]} "* && ${BASH_REMATCH[3]} == "$GATE_INGEST_SENDER" ]]; then
      cnt=0; cnote="独立系統には数えない: 送信者 $GATE_INGEST_SENDER は外部系統の記録だけに使われる"
    fi
    R_CNT+=("$cnt")
    if [[ ${BASH_REMATCH[6]} != "$head" ]]; then R_OK+=(0); R_NOTE+=("無効: sha が head と一致しない")
    elif [[ ${BASH_REMATCH[4]} != - && -n $pr && ${BASH_REMATCH[4]} != "$pr" ]]; then R_OK+=(0); R_NOTE+=("無効: 記録の PR(${BASH_REMATCH[4]})が対象と違う")
    else R_OK+=(1); R_NOTE+=("有効${cnote:+($cnote)}"); fi
  done <"$f"
  return 0
}

# ---------- 評価 ----------
# gate_eval <merge|approve> <chain-id> <pr 確認用|空> <行>...
# 常に 0 で返り、結果は GATE_VALUE(allow|ask) GATE_EXIT(0|20|21) GATE_SCORE GATE_THRESHOLD GATE_HARD GATE_LINES GATE_WARNS に入る
gate_eval() {
  local LC_ALL=C action=$1 chain=$2 prcheck=$3; shift 3
  GATE_VALUE=ask; GATE_EXIT=21; GATE_SCORE=0; GATE_THRESHOLD=""; GATE_HARD=(); GATE_LINES=(); GATE_WARNS=()
  GATE_ACTION=$action; GATE_CHAIN=$chain; G_PR=""; G_HEAD=""
  local errs=() e
  if ! _gate_load_conf; then errs+=("$GATE_ERR"); GATE_WARNS=(); fi
  if ! _gate_load_paths; then errs+=("$GATE_ERR"); fi
  _gate_parse "$@"
  for e in "${GATE_INERRS[@]:-}"; do [[ -n $e ]] && errs+=("入力: $e"); done
  if [[ -n $G_HEAD ]]; then
    _gate_load_reviews "$chain" "$G_HEAD" "$G_PR" || errs+=("$GATE_ERR")
  fi
  if [[ -n $prcheck && -n $G_PR && $prcheck != "$G_PR" ]]; then errs+=("入力: --pr($prcheck)が記録の PR($G_PR)と食い違います"); fi
  if ((${#errs[@]})); then
    for e in "${errs[@]}"; do GATE_LINES+=("E-01 評価不能(fail-closed): $e"); done
    return 0
  fi
  case $action in approve) GATE_THRESHOLD=$GATE_TH_APPROVE;; merge) GATE_THRESHOLD=$GATE_TH_MERGE;; esac
  local i p pts n pct lv cat ext base nc=0 has_docs=0 has_tests=0 has_code=0 has_imp=0 repo=${G_PR%%#*} allowed=0 r

  # --- hard(点数に関係なく ask)
  for r in "${GATE_REPOS[@]:-}"; do [[ -n $r && $r == "$repo" ]] && allowed=1; done
  ((allowed)) || GATE_HARD+=("H-05 許可リスト外のリポジトリ(またはリストが空)")
  local f st op np hits=""
  for f in "${G_FILES[@]}"; do
    read -r st op np <<<"$f"
    [[ $st == T ]] && GATE_HARD+=("H-06 ファイルの種別・モードの変更: $op")
    for p in "$op" ${np:+"$np"}; do
      if _gate_path_hit "$p"; then GATE_HARD+=("H-01 ガバナンスのパス規則に当たる変更: $p(規則 $GATE_HIT_PAT)"); fi
    done
  done
  [[ $G_PUBLIC == clean ]] || GATE_HARD+=("H-02 公開前検査が clean でない($G_PUBLIC)")
  # レビュー(記録から読む): 記録の sha が head と違うものは無効(数えない)。有効な記録の系統ごとに、
  # blocker・fail が 1 件でもあれば hard。独立 2 系統(系統が違い、送信者も違う)の pass がそろわなければ hard
  local lins=" " senders=" " nlin=0 bad=0 should=0 nvalid=0 failed=" " j
  for ((i = 0; i < ${#R_LIN[@]}; i++)); do
    GATE_LINES+=("REC ${R_LIN[i]} ${R_SHA[i]:0:7} blocker=${R_BLK[i]} should=${R_SHD[i]} verdict=${R_VER[i]} by=${R_BY[i]} at=${R_TS[i]} → ${R_NOTE[i]}")
    ((R_OK[i])) || continue
    nvalid=$((nvalid+1))
    if ((R_BLK[i] > 0)) || [[ ${R_VER[i]} != pass ]]; then
      bad=1; failed+="${R_LIN[i]} "
      GATE_HARD+=("H-03 レビュー(${R_LIN[i]}, ${R_BY[i]})に blocker または fail がある")
    fi
  done
  for ((i = 0; i < ${#R_LIN[@]}; i++)); do
    ((R_OK[i])) || continue
    [[ $failed != *" ${R_LIN[i]} "* ]] || continue         # 同じ sha で 1 件でも fail の系統は数えない
    ((R_CNT[i])) || continue                               # 送信者 human(誰でも名乗れる)と、自称の外部系統(送信者が ingest でない coderabbit など)は、独立な系統の根拠に数えない(fail は上で効く)
    ((R_SHD[i] > 0)) && should=1
    [[ $lins != *" ${R_LIN[i]} "* && $senders != *" ${R_BY[i]} "* ]] || continue   # 系統も送信者も、すでに数えたものと別であること
    lins+="${R_LIN[i]} "; senders+="${R_BY[i]} "; nlin=$((nlin+1))
  done
  if ((nvalid == 0)); then GATE_HARD+=("H-03 head に対する有効なレビューの記録が無い")
  elif ((nlin < 2)); then GATE_HARD+=("H-03 独立した 2 系統の pass がそろっていない(数えられた系統: $nlin。送信者 human と自称の外部系統は数えない)"); fi
  if [[ $G_TEST == run ]]; then
    if ((G_TEST_EXIT != 0)); then GATE_HARD+=("H-03 テストが失敗(exit=$G_TEST_EXIT)")
    elif [[ $G_TEST_SHA != "$G_HEAD" ]]; then GATE_HARD+=("H-03 テストの sha が head と一致しない"); fi
  fi
  [[ $(printf %s "$G_AUTHOR" | tr A-Z a-z) != "$(printf %s "$G_EXECUTOR" | tr A-Z a-z)" ]] || GATE_HARD+=("H-04 作者と実行の主体が同一($G_AUTHOR)")

  # --- 点数
  pct=$((G_SIZE_CH * 100 / G_SIZE_TOT))
  if ((pct < 10)); then pts=0; lv=小; elif ((pct < 30)); then pts=1; lv=中; elif ((pct < 60)); then pts=2; lv=大; else pts=3; lv=特大; fi
  GATE_SCORE=$((GATE_SCORE + pts)); GATE_LINES+=("S-01 +$pts 変更の大きさ: $lv")
  shopt -q nocasematch && nc=1   # 点数の分類も hard と同じく大文字小文字を区別しない(Lib/ と lib/ は同じ扱い)
  shopt -s nocasematch
  for f in "${G_FILES[@]}"; do
    read -r st op np <<<"$f"
    for p in "$op" ${np:+"$np"}; do
      case $p in
        lib/*|*/lib/*|config/*|*/config/*|scheduler/*|*/scheduler/*|.github/*|bin/*|*/bin/*) has_imp=1; has_code=1;;
        test/*|tests/*|*/test/*|*/tests/*) has_tests=1;;
        docs/*|*/docs/*|*.md)   # docs として数えるのは拡張子の許可リストだけ。それ以外(拡張子なし、実行できるもの、設定など)は一般のコード
          base=${p##*/}; if [[ ${base#.} == *.* ]]; then ext=$(printf %s "${base##*.}" | tr A-Z a-z); else ext=""; fi   # 先頭の . だけの名前(.md)は拡張子なし
          case $ext in md|txt|png|svg|jpg|jpeg|gif|rst) has_docs=1;; *) has_code=1;; esac;;
        *) has_code=1;;
      esac
    done
  done
  ((nc)) || shopt -u nocasematch
  if ((has_imp)); then pts=2; cat="lib・config・scheduler 配下(重要度 高)"
  elif ((has_code)); then pts=1; cat="一般のコード"
  else pts=0; cat="docs・tests のみ"; fi
  GATE_SCORE=$((GATE_SCORE + pts)); GATE_LINES+=("P-01 +$pts 触るパス: $cat")
  n=$((has_docs + has_tests + has_code))
  if ((n > 1)); then pts=2; cat=mixed
  elif ((has_code)); then pts=1; cat=code
  elif ((has_tests)); then pts=0; cat=tests-only
  else pts=0; cat=docs-only; fi
  GATE_SCORE=$((GATE_SCORE + pts)); GATE_LINES+=("K-01 +$pts PR の種類: $cat")
  if [[ $G_TEST == none ]]; then pts=2; GATE_SCORE=$((GATE_SCORE + pts)); GATE_LINES+=("T-01 +$pts テスト: 未実行")
  else GATE_LINES+=("T-01 +0 テスト: exit=$G_TEST_EXIT sha 一致"); fi
  if ((bad || nlin < 2)); then GATE_LINES+=("R-01 +0 レビュー: hard で扱う(H-03)")
  elif ((should)); then pts=1; GATE_SCORE=$((GATE_SCORE + pts)); GATE_LINES+=("R-01 +$pts レビュー: should が残っている")
  else GATE_LINES+=("R-01 +0 レビュー: $nlin 系統とも blocker=0 should=0 sha 一致"); fi
  case $G_REJECT in
    low) pts=0;; mid) pts=1;; high) pts=2;; unknown) pts=1;;
  esac
  GATE_SCORE=$((GATE_SCORE + pts)); GATE_LINES+=("A-01 +$pts 作者の差し戻し率: $G_REJECT")

  if ((${#GATE_HARD[@]})); then GATE_VALUE=ask; GATE_EXIT=20
  elif ((GATE_SCORE <= GATE_THRESHOLD)); then GATE_VALUE=allow; GATE_EXIT=0
  else GATE_VALUE=ask; GATE_EXIT=20; fi
  return 0
}

gate_format() {
  local l h w
  printf 'gate: %s\n' "$GATE_VALUE"
  printf 'action: %s\n' "$GATE_ACTION"
  printf 'chain: %s   pr: %s   head: %s\n' "$GATE_CHAIN" "${G_PR:--}" "${G_HEAD:--}"
  printf 'mode: %s   # 段 0 は参考値だけ。実行は常に無効\n' "${GATE_MODE:--}"
  if [[ $GATE_EXIT == 21 ]]; then
    printf 'score: - / threshold(%s) -\n' "$GATE_ACTION"; printf 'hard: -\n'
  else
    printf 'score: %s / threshold(%s) %s\n' "$GATE_SCORE" "$GATE_ACTION" "$GATE_THRESHOLD"
    if ((${#GATE_HARD[@]})); then for h in "${GATE_HARD[@]}"; do printf 'hard: %s\n' "$h"; done; else printf 'hard: none\n'; fi
  fi
  for l in "${GATE_LINES[@]}"; do printf 'reason: %s\n' "$l"; done
  for w in "${GATE_WARNS[@]:-}"; do [[ -n $w ]] && printf 'warn: %s\n' "$w"; done
  return 0
}

# ---------- コマンド ----------
cmd_gate() {
  local sub=${1:-}; shift || true
  case $sub in
    evaluate) _gate_cmd_evaluate "$@";;
    ingest-review) _gate_cmd_ingest "$@";;
    ingest-events) _gate_cmd_ingest_events "$@";;
    reviewers) _gate_cmd_reviewers "$@";;
    execute) _gate_cmd_execute "$@";;
    *) usage "使い方: tb gate evaluate|execute <chain-id> --action merge|approve / tb gate ingest-review <task-id> --pr <o>/<r>#<n> --sha <sha> / tb gate ingest-events <task-id> --pr <o>/<r>#<n> / tb gate reviewers <o>/<r>#<n> [--task <id>]";;
  esac
}

# 引数: <chain-id> --action merge|approve。chain-id と action を GATE_A_* に入れる。誤りは終了コード 2
_gate_args() {
  GATE_A_CHAIN=""; GATE_A_ACTION=""; GATE_A_PR=""; GATE_A_STDIN=0; GATE_A_FACTS=()
  local allow_facts=$1; shift
  while (($#)); do
    case $1 in
      --action) [[ $# -ge 2 ]] || usage "--action に値がありません"; GATE_A_ACTION=$2; shift 2;;
      --pr) [[ $allow_facts == 1 && $# -ge 2 ]] || usage "--pr は evaluate だけで、値が要ります"; GATE_A_PR=$2; shift 2;;
      --fact) [[ $allow_facts == 1 && $# -ge 2 ]] || usage "--fact は evaluate だけで、値が要ります"; GATE_A_FACTS+=("$2"); shift 2;;
      --facts-stdin) [[ $allow_facts == 1 ]] || usage "--facts-stdin は evaluate だけです"; GATE_A_STDIN=1; shift;;
      -*) usage "不明な引数: $1";;
      *) [[ -z $GATE_A_CHAIN ]] || usage "chain-id は 1 つだけです"; GATE_A_CHAIN=$1; shift;;
    esac
  done
  [[ $GATE_A_CHAIN =~ ^[A-Za-z0-9._-]{1,64}$ ]] || usage "chain-id が不正です(英数字 . _ - の 64 文字まで)"
  [[ $GATE_A_ACTION == merge || $GATE_A_ACTION == approve ]] || usage "--action は merge か approve です"
}

_gate_cmd_evaluate() {
  _gate_args 1 "$@"
  local lines=() line
  if ((GATE_A_STDIN)); then
    while IFS= read -r line || [[ -n $line ]]; do lines+=("$line"); done
  fi
  lines+=("${GATE_A_FACTS[@]:-}")
  gate_eval "$GATE_A_ACTION" "$GATE_A_CHAIN" "$GATE_A_PR" "${lines[@]}"
  gate_format
  return "$GATE_EXIT"
}

# 実行の口(段 0: 常に無効)。gate の設定や入力は読まず、何も記録せず、何も実行しない。設定・入力・環境によらず終了コード 30(tb 共通の起動処理は走るので、DB を開けないときは 30 でなく 1)
_gate_cmd_execute() {
  _gate_args 0 "$@"
  echo "gate: execute は無効です(段 0。何も実行せず、何も記録しません)" >&2
  return 30
}

# ---------- 記録の追記(review-submit と ingest-review の共通) ----------
# _gate_append_record <task> <by> <pr> <lin> <sha> <blk> <shd> <ver> <ev>。固定の置き場に 1 行だけ追記する。symlink は拒否。既存の行は書き換えない
_gate_append_record() {
  local f=$GATE_REVIEWS line
  mkdir -p "$(dirname "$f")" || die "記録の置き場を作れません"
  if [[ -L $f || ( -e $f && ! -f $f ) ]]; then die "記録の置き場が通常のファイルではありません"; fi
  line="ts=$(now) task=$1 by=$2 pr=$3 REVIEW $4 $5 blocker=$6 should=$7 verdict=$8 evidence=$9"
  [[ $line =~ $GATE_REC_RE ]] || die "記録の形を作れませんでした(内部エラー)"
  ( umask 077; printf '%s\n' "$line" >>"$f" ) || die "記録に失敗しました"
}

# ---------- review-submit(レビュー結果の記録の口) ----------
# tb review-submit <task-id> --lineage L --sha S --blocker N --should N --verdict pass|fail [--pr o/r#n] [--evidence PATH]
# 送信者は pane から tb が決める(自己申告の引数は無い)。pane が無い = 人の端末(または非 agent の決定論の経路)で、by=human。
# 終了コード: 0 記録した / 2 使い方 / 3 送信者を特定できない / 4 作者(タスクの owner)と同一、または作者を確かめられない / 5 役割・系統が許されない / 1 その他
# 記録先は固定(GATE_REVIEWS)。追記だけで、既存の行は書き換えない。DB のスキーマは変えない。
cmd_review-submit() {
  [[ $# -ge 1 ]] || usage "review-submit <task-id> --lineage L --sha S --blocker N --should N --verdict pass|fail [--pr o/r#n] [--evidence PATH]"
  local LC_ALL=C tid=$1 lin="" sha="" blk="" shd="" ver="" pr="-" ev="-"; shift
  valid_id "$tid"
  while [[ $# -gt 0 ]]; do
    case $1 in
      --lineage) need_arg "$@"; lin=$2; shift 2;;
      --sha) need_arg "$@"; sha=$2; shift 2;;
      --blocker) need_arg "$@"; blk=$2; shift 2;;
      --should) need_arg "$@"; shd=$2; shift 2;;
      --verdict) need_arg "$@"; ver=$2; shift 2;;
      --pr) need_arg "$@"; pr=$2; shift 2;;
      --evidence) need_arg "$@"; ev=$2; shift 2;;
      *) usage "review-submit: 不明な引数 $1";;
    esac
  done
  [[ $lin =~ ^[a-z][a-z0-9-]{0,31}$ ]] || usage "--lineage は小文字英字で始まる英小文字・数字・- の 32 文字まで(例: self, coderabbit)"
  [[ $sha =~ ^[0-9a-f]{7,40}$ ]] || usage "--sha は 7〜40 桁の小文字 16 進"
  [[ $blk =~ ^[0-9]{1,4}$ ]] || usage "--blocker は 0 以上の整数"
  [[ $shd =~ ^[0-9]{1,4}$ ]] || usage "--should は 0 以上の整数"
  [[ $ver == pass || $ver == fail ]] || usage "--verdict は pass か fail"
  [[ $pr == - || $pr =~ ^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+#[0-9]+$ ]] || usage "--pr は <owner>/<repo>#<n>"
  [[ $ev == - || $ev =~ ^[A-Za-z0-9._/@+~-]{1,200}$ ]] || usage "--evidence は空白のない 200 文字までのパス"
  blk=$((10#$blk)); shd=$((10#$shd))
  # 整合: pass なのに blocker がある記録は作らない(評価でも hard になる)
  [[ $ver == fail || $blk -eq 0 ]] || usage "verdict=pass には blocker=0 が要ります"

  # 送信者(pane から決める。自己申告にしない)
  local by=human kind=human pane=${HERDR_PANE_ID:-}
  if [[ -n $pane ]]; then
    by=$(pane_owner_name "$pane" 2>/dev/null) || { echo "tb: herdr agent list に失敗。送信者を特定できないため記録しません" >&2; exit 3; }
    [[ $by =~ ^[A-Za-z0-9._-]{1,64}$ ]] || { echo "tb: pane $pane の agent の名前を特定できないため記録しません" >&2; exit 3; }
    kind=agent
  fi
  # 送信者名 ingest は取り込み口の固定の名前。同じ名前の agent は記録できない
  if [[ $by == "$GATE_INGEST_SENDER" ]]; then echo "tb: 送信者名 $GATE_INGEST_SENDER は取り込み口の予約名です。記録しません" >&2; exit 5; fi
  # タスクと作者(タスクの owner)
  local exists owner
  exists=$(sql "SELECT count(*) FROM tasks WHERE id=$(q "$tid");")
  [[ $exists == 1 ]] || die "タスクが見つかりません: $tid"
  owner=$(sql "SELECT COALESCE(owner_agent,'') FROM tasks WHERE id=$(q "$tid");")
  if [[ $kind == agent ]]; then
    # 最小の職務分掌(設計書 11.3): agent はレビュー担当の役割だけ。作者と同一は拒否。作者が分からないときも拒否
    if [[ -z $owner ]]; then echo "tb: タスクの owner がいないため、作者と別かを確かめられません。記録しません" >&2; exit 4; fi
    if [[ $(printf %s "$owner" | tr A-Z a-z) == "$(printf %s "$by" | tr A-Z a-z)" ]]; then
      echo "tb: 作者(タスクの owner: $owner)は自分の成果物のレビュー結果を記録できません" >&2; exit 4
    fi
    local role rf
    rf=$(GATE_ROLES_DIR="${TB_ROOT}/../roles"; ROLES_DIR=$GATE_ROLES_DIR; role_file_for "$by") || rf=""
    role=$(basename "${rf:-}" .md)
    if [[ -z $role || $GATE_REVIEW_ROLES != *" $role "* ]]; then
      echo "tb: この役割($(printf %s "${role:-なし}"))は review-submit できません(レビュー担当の役割だけ)" >&2; exit 5
    fi
    if [[ $GATE_RESERVED_LINEAGES == *" $lin "* ]]; then
      echo "tb: 系統 $lin は外部の取り込み用で、agent は名乗れません" >&2; exit 5
    fi
  fi

  _gate_append_record "$tid" "$by" "$pr" "$lin" "$sha" "$blk" "$shd" "$ver" "$ev"
  echo "recorded: $tid REVIEW $lin ${sha:0:7} blocker=$blk should=$shd verdict=$ver by=$by"
}

# ---------- ingest-review(外部レビューの取り込み。決定論。agent ではなくこの口が、別の送信者 ingest として記録する) ----------
# tb gate ingest-review <task-id> --pr <o>/<r>#<n> --sha <head の sha>
# gh の読み取り(GET の api だけ)で、外部レビュー(CodeRabbit)の状態を取り、REVIEW coderabbit の記録を送信者 ingest で追記する。LLM を使わない。
# 数えるのは、レビューが head の sha に対して存在すること、スキップの印、未解決の指摘の件数と重大度の表記だけ。
# 本文の中の指示(Prompt for AI Agents、自動修正のチェックボックスなど)は読まず、実行しない。
# 終了コード: 0 記録した / 2 使い方 / 5 agent の pane からは実行できない / 1 設定・gh を使えない / 10 head に対するレビューが無い(記録しない)
#             11 レビューがスキップされている(上限など。pass とは記録しない) / 12 取得・解析に失敗(記録しない)
# 設定: config/gate-ingest.conf(固定の場所。env では差し替えられない)。gh は専用ラッパーの絶対パス。未指定なら止まる(fail-closed)
GATE_SEV_KEYS=(critical major "potential issue" minor trivial nitpick "refactor suggestion")
GATE_SEV_VALS=(blocker blocker should should should should should)
GATE_ING_GH=""; GATE_ING_LOGIN=""; GATE_ING_ERR=""
# 本文から重大度の表記を探す jq の定義(固定。本文は解釈せず、最初の実質行だけを見る)。
# 実物の CodeRabbit の指摘は、1 行目が HTML コメント、2 行目が空、重大度は 3 行目(picture タグ入り)。
# そのため HTML コメントとタグを除いて、空行を飛ばした最初の行(最初の実質行)から表記を探す。表の対応先がなければ "unknown"
GATE_JQ_DEFS='
    def firstline: ((. // "") | gsub("<!--.*?-->"; ""; "s") | gsub("<[^>]*>"; "") | split("\n") | map(gsub("^\\s+|\\s+$"; "")) | map(select(length > 0)) | (.[0] // "") | ascii_downcase);
    def sev: firstline as $l
      | [$table | to_entries[] | . as $e | select($l | contains($e.key)) | $e.value] | if length == 0 then "unknown" elif index("blocker") then "blocker" else "should" end;
    def addressed: test("(^|\n)\\s*✅ Addressed in commit");
'

_gate_load_ingest_conf() {   # $1=login を必須にするか(既定 1。段 1c の全レビュー取り込みは 0)
  local LC_ALL=C need_login=${1:-1} f=$GATE_INGEST_CONF sec="" line n=0 k v i found
  GATE_ING_ERR=""; GATE_ING_GH=""; GATE_ING_LOGIN=""; GATE_WARNS=()
  GATE_SEV_KEYS=(critical major "potential issue" minor trivial nitpick "refactor suggestion")
  GATE_SEV_VALS=(blocker blocker should should should should should)
  [[ -e $f ]] || { GATE_ING_ERR="設定ファイルがありません(config/gate-ingest.conf)。gh の専用ラッパーの絶対パスを指定してください"; return 1; }
  [[ -f $f && -r $f ]] || { GATE_ING_ERR="設定ファイルを読めません"; return 1; }
  while IFS= read -r line || [[ -n $line ]]; do
    n=$((n+1)); line=$(_gate_trim "${line%%#*}")
    [[ -z $line ]] && continue
    if [[ $line =~ ^\[([a-z]+)\]$ ]]; then
      sec=${BASH_REMATCH[1]}
      [[ $sec == gh || $sec == source || $sec == severity ]] || { GATE_ING_ERR="L$n: 未知の節 [$sec]"; return 1; }
      continue
    fi
    [[ -n $sec && $line == *=* ]] || { GATE_ING_ERR="L$n: '[節]' の後の 'キー = 値' の形ではありません"; return 1; }
    k=$(_gate_trim "${line%%=*}"); v=$(_gate_trim "${line#*=}")
    case $sec.$k in
      gh.path) GATE_ING_GH=$v;;
      source.login) [[ $v =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,38}(\[bot\])?$ ]] || { GATE_ING_ERR="L$n: login の形が不正です"; return 1; }; GATE_ING_LOGIN=$v;;
      severity.*)
        [[ $k =~ ^[a-z][a-z0-9\ -]{0,31}$ ]] || { GATE_ING_ERR="L$n: 重大度の表記は小文字英数字・空白・- の 32 文字までです: $k"; return 1; }
        [[ $v == blocker || $v == should ]] || { GATE_ING_ERR="L$n: 重大度の対応先は blocker | should です: $v"; return 1; }
        found=0
        for ((i = 0; i < ${#GATE_SEV_KEYS[@]}; i++)); do
          [[ ${GATE_SEV_KEYS[i]} == "$k" ]] || continue
          found=1
          if [[ ${GATE_SEV_VALS[i]} == blocker && $v == should ]]; then GATE_WARNS+=("緩め設定は無効: [severity] $k = should(blocker のまま)")
          else GATE_SEV_VALS[i]=$v; fi
        done
        ((found)) || { GATE_SEV_KEYS+=("$k"); GATE_SEV_VALS+=("$v"); };;
      *) GATE_ING_ERR="L$n: 未知のキー [$sec] $k"; return 1;;
    esac
  done <"$f"
  [[ $need_login == 0 || -n $GATE_ING_LOGIN ]] || { GATE_ING_ERR="[source] login がありません(例: coderabbitai[bot])"; return 1; }
  if [[ -z $GATE_ING_GH ]]; then GATE_ING_ERR="[gh] path が指定されていないため止めます(fail-closed)。専用の gh ラッパーの絶対パスを指定してください。PATH 上の gh には落としません"; return 1; fi
  if [[ $GATE_ING_GH != /* || ! -f $GATE_ING_GH || ! -x $GATE_ING_GH ]]; then GATE_ING_ERR="指定された gh を使えないため止めます(fail-closed): $GATE_ING_GH"; return 1; fi
  return 0
}

# _gate_ingest_fetch <gh> <path> <出力ファイル>: GET の api だけを固定の引数で呼ぶ。ページを連結して 1 つの配列にする。失敗は 1
_gate_ingest_fetch() {
  local out
  out=$("$1" api --paginate "$2" 2>/dev/null) || return 1
  printf '%s' "$out" | jq -s 'if length > 0 and all(.[]; type == "array") then add else error("not an array") end' >"$3" 2>/dev/null || return 1
}

_gate_cmd_ingest() {
  [[ $# -ge 1 ]] || usage "gate ingest-review <task-id> --pr <o>/<r>#<n> --sha <head の sha>"
  local LC_ALL=C tid=$1 pr="" sha=""; shift
  valid_id "$tid"
  while [[ $# -gt 0 ]]; do
    case $1 in
      --pr) need_arg "$@"; pr=$2; shift 2;;
      --sha) need_arg "$@"; sha=$2; shift 2;;
      *) usage "gate ingest-review: 不明な引数 $1";;
    esac
  done
  [[ $pr =~ ^([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+)#([0-9]+)$ ]] || usage "--pr は <owner>/<repo>#<n>"
  local owner=${BASH_REMATCH[1]} repo=${BASH_REMATCH[2]} num=${BASH_REMATCH[3]}
  [[ $sha =~ ^[0-9a-f]{7,40}$ ]] || usage "--sha は 7〜40 桁の小文字 16 進"
  # agent の pane からは実行できない(送信者 ingest を名乗らせない)。既知の限界: 環境変数を外せば破れる(docs/gate.md)
  if [[ -n ${HERDR_PANE_ID:-} ]]; then echo "tb: gate ingest-review は agent の pane からは実行できません" >&2; exit 5; fi
  local exists; exists=$(sql "SELECT count(*) FROM tasks WHERE id=$(q "$tid");")
  [[ $exists == 1 ]] || die "タスクが見つかりません: $tid"
  _gate_load_ingest_conf || { echo "tb: gate ingest-review: $GATE_ING_ERR" >&2; exit 1; }
  local w; for w in "${GATE_WARNS[@]:-}"; do [[ -n $w ]] && echo "warn: $w" >&2; done
  local tmp; tmp=$(mktemp -d) || die "一時ディレクトリを作れません"
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" RETURN
  local base="repos/$owner/$repo"
  _gate_ingest_fetch "$GATE_ING_GH" "$base/pulls/$num/reviews" "$tmp/reviews.json" \
    && _gate_ingest_fetch "$GATE_ING_GH" "$base/pulls/$num/comments" "$tmp/comments.json" \
    && _gate_ingest_fetch "$GATE_ING_GH" "$base/issues/$num/comments" "$tmp/issues.json" \
    || { echo "tb: gate ingest-review: 取得または解析に失敗したため記録しません(記録なし = 条件を満たさない)" >&2; exit 12; }
  local table i; table="{}"
  for ((i = 0; i < ${#GATE_SEV_KEYS[@]}; i++)); do table=$(jq -c --arg k "${GATE_SEV_KEYS[i]}" --arg v "${GATE_SEV_VALS[i]}" '. + {($k): $v}' <<<"$table"); done
  # 判定は jq の固定のプログラム。本文は、1 行目の重大度の表記と、決まった印の有無を見るだけ
  local res
  res=$(jq -nc --arg login "$GATE_ING_LOGIN" --arg sha "$sha" --argjson table "$table" \
      --slurpfile rv "$tmp/reviews.json" --slurpfile cm "$tmp/comments.json" --slurpfile is "$tmp/issues.json" "$GATE_JQ_DEFS"'
    def lines($n): ((. // "") | split("\n")[:$n] | join("\n"));
    def skipmark: lines(12) | test("rate limit|review skipped|reviews paused|usage limit|quota"; "i");
    ($rv[0]) as $R | ($cm[0]) as $C | ($is[0]) as $I
    | [$R[] | select(.user.login == $login and ((.commit_id // "") | test("^[0-9a-f]{40}$")) and ((.commit_id) | startswith($sha))
        and (.state | IN("COMMENTED", "APPROVED", "CHANGES_REQUESTED")))] | sort_by(.submitted_at // "") as $heads
    | if ($heads | length) == 0 then {status: "none"}
      else ($heads | last) as $h
      | if ($h.body | skipmark) then {status: "skipped"}
        elif ([$I[] | select(.user.login == $login and ((.updated_at // "") > ($h.submitted_at // "")) and (.body | skipmark))] | length) > 0 then {status: "skipped"}
        else
          [$C[] | select(.user.login == $login and (.position != null or .subject_type == "file") and ((.body // "") | addressed | not)) | (.body | sev)] as $sevs
          | {status: "ok", blocker: ([$sevs[] | select(. == "blocker")] | length), unknown: ([$sevs[] | select(. == "unknown")] | length),
             should: ([$sevs[] | select(. == "should" or . == "unknown")] | length)}
        end
      end' 2>/dev/null) || { echo "tb: gate ingest-review: 解析に失敗したため記録しません" >&2; exit 12; }
  local st blk shd ver
  st=$(jq -r '.status // ""' <<<"$res")
  case $st in
    none) echo "tb: gate ingest-review: head($sha)に対する外部レビューがありません。記録しません(記録なし = 条件を満たさない)" >&2; exit 10;;
    skipped) echo "tb: gate ingest-review: 外部レビューはスキップされています(上限など)。pass とは記録しません" >&2; exit 11;;
    ok) ;;
    *) echo "tb: gate ingest-review: 解析結果が不正なため記録しません" >&2; exit 12;;
  esac
  local unk
  blk=$(jq -r '.blocker' <<<"$res"); shd=$(jq -r '.should' <<<"$res"); unk=$(jq -r '.unknown' <<<"$res")
  [[ $blk =~ ^[0-9]{1,4}$ && $shd =~ ^[0-9]{1,4}$ && $unk =~ ^[0-9]{1,4}$ ]] || { echo "tb: gate ingest-review: 件数が範囲外のため記録しません" >&2; exit 12; }
  # 重大度の表記が見つからなかった指摘は should として数える(blocker には数えない)。見落としに気づけるよう、件数を警告に出す
  ((unk == 0)) || echo "warn: 重大度の表記が見つからない指摘が ${unk} 件あります(should として数えました。blocker には数えていません)" >&2
  ver=pass; ((blk == 0)) || ver=fail
  _gate_append_record "$tid" "$GATE_INGEST_SENDER" "$pr" coderabbit "$sha" "$blk" "$shd" "$ver" -
  echo "recorded: $tid REVIEW coderabbit ${sha:0:7} blocker=$blk should=$shd verdict=$ver by=$GATE_INGEST_SENDER unknown-label=$unk"
}

# ---------- ingest-events / reviewers(段 1c: 全レビューの身元つきの記録と表示。判定の入力は変えない) ----------
# tb gate ingest-events <task-id> --pr <o>/<r>#<n>   PR に付いた全レビュー(reviews / inline / issue のコメント)を、身元つきで GATE_EVENTS に追記する。
# tb gate reviewers <o>/<r>#<n> [--task <id>]         記録をレビュアー(数値 id)ごとにまとめて表示する(読み取りだけ。gh は呼ばない)。
# 本文は保存しない(本文の sha256 だけ)。本文中の指示は読まず、実行しない。判定(evaluate / execute)はこの記録を読まない。
# 1 行: ts= task= repo= pr= kind=review|inline|issue event=<id> actor=<login> actor_id=<数値> actor_type=user|bot|org assoc=<小文字> author=0|1
#       sha=<40 桁|-> state=<状態|-> label=blocker|should|none addr=0|1 body_sha256=<64 桁> upd=<UTC|->
#       review=<属するレビューの id|-> orig_sha=<inline の元のコミット|-> outdated=0|1 head=<取り込み時点の PR の head の sha> by=ingest
#   inline の sha(commit_id)は、コメントが付いた時のコミットとは限らない(再アンカーで付け替わる)ので、元のコミット(orig_sha)、
#   位置が消えたか(outdated)、属するレビュー(review)、取り込み時点の head(head)を併せて残す。判定には使わない(段 1c)
GATE_EV_RE='^ts=([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z) task=([A-Za-z0-9._-]{1,64}) repo=([A-Za-z0-9._-]+/[A-Za-z0-9._-]+) pr=([0-9]+) kind=(review|inline|issue) event=([0-9]{1,15}) actor=([A-Za-z0-9][A-Za-z0-9-]{0,38}(\[bot\])?) actor_id=([0-9]{1,12}) actor_type=(user|bot|org) assoc=([a-z_]{1,32}) author=([01]) sha=([0-9a-f]{40}|-) state=([a-z_]{1,32}|-) label=(blocker|should|none) addr=([01]) body_sha256=([0-9a-f]{64}) upd=([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z|-) review=([0-9]{1,15}|-) orig_sha=([0-9a-f]{40}|-) outdated=([01]) head=([0-9a-f]{40}) by=ingest$'

# 全体を 1 回で取る(object を返す GET)。失敗・JSON でない・object でないものは 1
_gate_ingest_fetch_obj() {
  local out
  out=$("$1" api "$2" 2>/dev/null) || return 1
  printf '%s' "$out" | jq -e 'type == "object"' >/dev/null 2>&1 || return 1
  printf '%s' "$out" >"$3"
}

_gate_cmd_ingest_events() {
  [[ $# -ge 1 ]] || usage "gate ingest-events <task-id> --pr <o>/<r>#<n>"
  local LC_ALL=C tid=$1 pr=""; shift
  valid_id "$tid"
  while [[ $# -gt 0 ]]; do
    case $1 in
      --pr) need_arg "$@"; pr=$2; shift 2;;
      *) usage "gate ingest-events: 不明な引数 $1";;
    esac
  done
  [[ $pr =~ ^([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+)#([0-9]+)$ ]] || usage "--pr は <owner>/<repo>#<n>"
  local owner=${BASH_REMATCH[1]} repo=${BASH_REMATCH[2]} num=${BASH_REMATCH[3]}
  # agent の pane からは実行できない(送信者 ingest を名乗らせない)。既知の限界: 環境変数を外せば破れる(docs/gate.md)
  if [[ -n ${HERDR_PANE_ID:-} ]]; then echo "tb: gate ingest-events は agent の pane からは実行できません" >&2; exit 5; fi
  local exists; exists=$(sql "SELECT count(*) FROM tasks WHERE id=$(q "$tid");")
  [[ $exists == 1 ]] || die "タスクが見つかりません: $tid"
  _gate_load_ingest_conf 0 || { echo "tb: gate ingest-events: $GATE_ING_ERR" >&2; exit 1; }
  local w; for w in "${GATE_WARNS[@]:-}"; do [[ -n $w ]] && echo "warn: $w" >&2; done
  # 既存の記録: 通常のファイルで、全行が形式どおりであること(壊れていれば止まる = fail-closed)
  local f=$GATE_EVENTS
  if [[ -L $f || ( -e $f && ! -f $f ) ]]; then echo "tb: gate ingest-events: 記録の置き場が通常のファイルではありません" >&2; exit 1; fi
  local tmp; tmp=$(mktemp -d) || die "一時ディレクトリを作れません"
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" EXIT
  if [[ -f $f ]]; then
    local ln=0 l; : >"$tmp/keys"
    while IFS= read -r l || [[ -n $l ]]; do
      ln=$((ln+1)); [[ -z $l ]] && continue
      [[ $l =~ $GATE_EV_RE ]] || { echo "tb: gate ingest-events: 既存の記録の形式違反(L$ln)のため止めます" >&2; exit 1; }
      # 同じ発言の同じ内容(task、repo、pr、種類、id、本文のハッシュ、更新時刻、状態、sha、outdated、login、actor_type、assoc)は足さない。head は取り込みの時点の値なので、キーに入れない
      printf '%s\n' "${BASH_REMATCH[2]}|${BASH_REMATCH[3]}|${BASH_REMATCH[4]}|${BASH_REMATCH[5]}|${BASH_REMATCH[6]}|${BASH_REMATCH[17]}|${BASH_REMATCH[18]}|${BASH_REMATCH[14]}|${BASH_REMATCH[13]}|${BASH_REMATCH[21]}|${BASH_REMATCH[7]}|${BASH_REMATCH[10]}|${BASH_REMATCH[11]}" >>"$tmp/keys"
    done <"$f"
  else : >"$tmp/keys"; fi
  local base="repos/$owner/$repo"
  _gate_ingest_fetch_obj "$GATE_ING_GH" "$base/pulls/$num" "$tmp/pr.json" \
    && _gate_ingest_fetch "$GATE_ING_GH" "$base/pulls/$num/reviews" "$tmp/reviews.json" \
    && _gate_ingest_fetch "$GATE_ING_GH" "$base/pulls/$num/comments" "$tmp/comments.json" \
    && _gate_ingest_fetch "$GATE_ING_GH" "$base/issues/$num/comments" "$tmp/issues.json" \
    || { echo "tb: gate ingest-events: 取得または解析に失敗したため記録しません" >&2; exit 12; }
  # PR 本体の必須項目(作者の数値 id と head の sha)は、発言の有無に関係なく検証する。欠けていれば何も記録しない
  jq -e '(.user.id | type == "number") and (.head.sha | type == "string" and test("^[0-9a-f]{40}$"))' "$tmp/pr.json" >/dev/null 2>&1 \
    || { echo "tb: gate ingest-events: PR 本体の作者 id または head の sha が不正なため、何も記録しません" >&2; exit 12; }
  local table i; table="{}"
  for ((i = 0; i < ${#GATE_SEV_KEYS[@]}; i++)); do table=$(jq -c --arg k "${GATE_SEV_KEYS[i]}" --arg v "${GATE_SEV_VALS[i]}" '. + {($k): $v}' <<<"$table"); done
  # 判定ではなく取り出しだけ。jq の固定のプログラムが、機械的に数えられる項目を TSV にする。本文は 1 行目の重大度の表記と、決まった印の有無、base64 だけに使う(解釈しない)
  jq -rn --argjson table "$table" --slurpfile pr "$tmp/pr.json" --slurpfile rv "$tmp/reviews.json" --slurpfile cm "$tmp/comments.json" --slurpfile is "$tmp/issues.json" "$GATE_JQ_DEFS"'
    def sev3: sev | if . == "unknown" then "should" else . end;   # 表記が見つからない指摘は should(段 1b と同じ)
    def addr01: (. // "") | addressed | if . then "1" else "0" end;
    def who: [(.user.login // ""), ((.user.id // "") | tostring), (.user.type // ""), ((.author_association // "") | ascii_downcase)];
    ($pr[0].user.id // "") as $aid | ($pr[0].head.sha // "") as $head
    | ( ($rv[0][] | ["review", (.id | tostring)] + who + [((.user.id // "") == $aid | tostring), (.commit_id // "-"), ((.state // "-") | ascii_downcase), "none", "0", (.submitted_at // "-"), (.id | tostring), "-", "0", $head, ((.body // "") | @base64)]),
        ($cm[0][] | ["inline", (.id | tostring)] + who + [((.user.id // "") == $aid | tostring), (.commit_id // "-"), "-", (.body | sev3), (.body | addr01), (.updated_at // "-"), ((.pull_request_review_id // "-") | tostring), (.original_commit_id // "-"), (if .position == null and .subject_type != "file" then "1" else "0" end), $head, ((.body // "") | @base64)]),
        ($is[0][] | ["issue", (.id | tostring)] + who + [((.user.id // "") == $aid | tostring), "-", "-", "none", "0", (.updated_at // "-"), "-", "-", "0", $head, ((.body // "") | @base64)]) )
    | map("x" + tostring) | @tsv' >"$tmp/events.tsv" 2>/dev/null || { echo "tb: gate ingest-events: 解析に失敗したため記録しません" >&2; exit 12; }
  # 1 件でも形式が不正なら、何も記録しない(全部か無か)
  local kind ev login lid ltype assoc isauth sha state label addr upd b64 sum t line ts nnew=0 nskip=0 key
  ts=$(now); : >"$tmp/new"
  while IFS=$'\t' read -r kind ev login lid ltype assoc isauth sha state label addr upd rvid osha outd head b64; do
    kind=${kind#x}; ev=${ev#x}; login=${login#x}; lid=${lid#x}; ltype=${ltype#x}; assoc=${assoc#x}; isauth=${isauth#x}; sha=${sha#x}; state=${state#x}; label=${label#x}; addr=${addr#x}; upd=${upd#x}; rvid=${rvid#x}; osha=${osha#x}; outd=${outd#x}; head=${head#x}; b64=${b64#x}   # 空の欄が詰まらないよう、jq 側で x を付けている
    case $ltype in User) t=user;; Bot) t=bot;; Organization) t=org;; *) t="";; esac
    [[ -n $assoc ]] || assoc=unknown
    [[ $isauth == true ]] && isauth=1 || isauth=0
    # 本文は、ハッシュにするためだけにデコードする。変数に入れて展開・評価はしない
    sum=$(printf '%s' "$b64" | base64 -d 2>/dev/null | { if command -v sha256sum >/dev/null; then sha256sum; else shasum -a 256; fi; } | cut -c1-64)
    line="ts=$ts task=$tid repo=$owner/$repo pr=$num kind=$kind event=$ev actor=$login actor_id=$lid actor_type=$t assoc=$assoc author=$isauth sha=$sha state=$state label=$label addr=$addr body_sha256=$sum upd=$upd review=$rvid orig_sha=$osha outdated=$outd head=$head by=$GATE_INGEST_SENDER"
    [[ -n $t && $line =~ $GATE_EV_RE ]] || { echo "tb: gate ingest-events: 形式が不正な発言があるため、何も記録しません(種類 $kind、id ${ev:0:20})" >&2; exit 12; }
    key="$tid|$owner/$repo|$num|$kind|$ev|$sum|$upd|$state|$sha|$outd|$login|$t|$assoc"
    if grep -qxF -- "$key" "$tmp/keys"; then nskip=$((nskip+1)); continue; fi
    printf '%s\n' "$key" >>"$tmp/keys"; printf '%s\n' "$line" >>"$tmp/new"; nnew=$((nnew+1))
  done <"$tmp/events.tsv"
  if ((nnew > 0)); then
    mkdir -p "$(dirname "$f")" || die "記録の置き場を作れません"
    ( umask 077; cat "$tmp/new" >>"$f" ) || die "記録に失敗しました"
  fi
  echo "recorded: $tid $owner/$repo#$num events: new=$nnew unchanged=$nskip"
}

# 表示だけ。読み取り専用。gh は呼ばない。形式違反の行があれば止まる
_gate_cmd_reviewers() {
  [[ $# -ge 1 ]] || usage "gate reviewers <o>/<r>#<n> [--task <id>]"
  local LC_ALL=C pr=$1 task=""; shift
  while [[ $# -gt 0 ]]; do
    case $1 in
      --task) need_arg "$@"; task=$2; shift 2;;
      *) usage "gate reviewers: 不明な引数 $1";;
    esac
  done
  [[ $pr =~ ^([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+)#([0-9]+)$ ]] || usage "引数は <owner>/<repo>#<n>"
  local repo=${BASH_REMATCH[1]}/${BASH_REMATCH[2]} num=${BASH_REMATCH[3]}
  [[ -z $task || $task =~ ^[A-Za-z0-9._-]{1,64}$ ]] || usage "--task の形が不正です"
  local f=$GATE_EVENTS
  echo "reviewers: $repo#$num   # 表示だけ。判定には使われません(段 1c)"
  echo "note: label-blocker / label-should は、投稿者が書いた本文の最初の実質行の語から機械的に数えた値で、検証された重大度ではありません(誰でも書ける)。判定には使われません"
  if [[ ! -e $f ]]; then echo "events: 0 (記録がありません)"; return 0; fi
  [[ -f $f && ! -L $f && -r $f ]] || { echo "tb: gate reviewers: 記録を読めません(通常のファイルではない)" >&2; exit 1; }
  local tmp; tmp=$(mktemp -d) || die "一時ディレクトリを作れません"
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" EXIT
  local ln=0 l
  # 同じ発言(種類と id)は最新の行だけを使う(編集は新しい行になる)。ファイルの順 = 時刻の順
  : >"$tmp/latest"
  while IFS= read -r l || [[ -n $l ]]; do
    ln=$((ln+1)); [[ -z $l ]] && continue
    [[ $l =~ $GATE_EV_RE ]] || { echo "tb: gate reviewers: 記録の形式違反(L$ln)" >&2; exit 1; }
    [[ ${BASH_REMATCH[3]} == "$repo" && ${BASH_REMATCH[4]} == "$num" ]] || continue
    [[ -z $task || ${BASH_REMATCH[2]} == "$task" ]] || continue
    # 発言の主キー | 内容の要約(後の行が前の行を置き換える)
    printf '%s\t%s\n' "${BASH_REMATCH[5]}:${BASH_REMATCH[6]}" "${BASH_REMATCH[9]} ${BASH_REMATCH[7]} ${BASH_REMATCH[10]} ${BASH_REMATCH[11]} ${BASH_REMATCH[12]} ${BASH_REMATCH[5]} ${BASH_REMATCH[15]} ${BASH_REMATCH[18]}" >>"$tmp/latest"
  done <"$f"
  # 発言ごとの最新 → レビュアー(数値 id)ごとの集計。login は id に紐づく表示用で、同じ id で複数あれば login-changed を出す
  # awk の for-in の順序は実装(gawk/mawk/BSD)で違うので、順序に頼らない: 最初に現れた順の番号を付け、番号で並べ直してから集計する。
  # 出力は先頭に数値 id を付けて数値順に並べ、あとで id の列を落とす
  awk -F'\t' '{ if (!($1 in last)) { cnt++; ord[$1] = cnt } last[$1] = $2 }
      END { for (k in last) print ord[k] "\t" last[k] }' "$tmp/latest" \
    | sort -n -k1,1 | cut -f2- \
    | awk '{
        id=$1; login=$2; type=$3; assoc=$4; auth=$5; kind=$6; label=$7; upd=$8
        if (!(id in lg)) { lg[id]=login } else if (index(" " lg[id] " ", " " login " ") == 0) { lg[id]=lg[id] " " login }
        ty[id]=type; as[id]=assoc
        if (auth == "1") { au[id]=1 }
        n[id SUBSEP kind]++
        if (label == "blocker") { bl[id]++ }
        if (label == "should") { sh[id]++ }
        if (upd > la[id]) { la[id]=upd }
        ids[id]=1
      }
      END { for (id in ids) {
        nl = split(lg[id], a, " ")
        first = a[1]; isau = au[id] + 0
        nr = n[id SUBSEP "review"] + 0; ni = n[id SUBSEP "inline"] + 0; ns = n[id SUBSEP "issue"] + 0
        nb = bl[id] + 0; nh = sh[id] + 0
        lastv = la[id]; if (lastv == "") { lastv = "-" }
        chg = ""; if (nl > 1) { chg = " login-changed" }
        print id "\treviewer: " first " id=" id " type=" ty[id] " assoc=" as[id] " author=" isau " reviews=" nr " inline=" ni " issue=" ns " label-blocker=" nb " label-should=" nh " last=" lastv chg
      } }' | sort -n -k1,1 | cut -f2- >"$tmp/out"
  echo "events: $(cut -f1 "$tmp/latest" | sort -u | wc -l | tr -d ' ')   reviewers: $(wc -l <"$tmp/out" | tr -d ' ')"
  cat "$tmp/out"
  return 0
}
