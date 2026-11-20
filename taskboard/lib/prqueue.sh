# shellcheck shell=bash
# PR 作成キュー(tb pr-queue)。bin/tb から source される。単体では実行しない。
#   add     agent が依頼(branch・本文ファイル・題)を置く                      → pending
#   approve 人が承認(本文が置いたときから変わっていないことも確かめる)          → approved
#   run     scheduler の COMMAND ジョブ。approved だけをガードに通し gh pr create --draft  → created / rejected / failed
# できないこと(実装しない): マージ・approve・ready・close・コメント。gh は create(--draft 固定)と pr list(重複確認)だけ。
# gh は PR_QUEUE_GH(環境変数)か config/pr-queue.conf の gh で指定する(絶対パス。既定は無い)。未指定・不正のままでは run / scan しない(fail-closed):
# PATH 上の gh には落とさない(ghtkn を通らない gh で黙って進まないため)。書き込み用の認証は gh のラッパー(人が用意する。docs/pr-queue-gh-wrapper.md)に持たせる。
# ラッパーが終了コード 75(= ghtkn が token を返せない。auth-locked)を返したときは attempts を使わず、通知を 1 回だけ出して止まる(人が unlock したあと次の run が拾う)。
# 差分のガードは必須: PR_QUEUE_REPO_DIR(ローカルの clone)が空・不正なら承認も run もしない。failed は人が tb pr-queue retry で approved に戻す。
# 環境ごとの決まりは config/pr-queue.conf(人だけが編集): branch_prefix(既定 agent/)・body_root(本文の置き場)・
# body_first_line(本文 1 行目が合うべき正規表現。空なら見ない。# は書けない)・auto_approve・max_create_per_run。
# 値は読まない・出さない: 秘密らしい値が見つかっても、出すのは種類の名前だけ。
# base(PR の向き先。#133): 既定は main 固定(base_mode = fixed)。base_mode = default_branch なら origin の既定ブランチ(git ls-remote --symref origin HEAD)を使う。
#   add --base で明示もできる(既定の base か、conf の base_allow の形に合うブランチだけ。origin に実在すること)。
#   決めた base と決め方は add のときに行へ書いて固定する(人は show で見て承認する)。approve と run でかけ直し、
#   既定ブランチが変わった・許可から外れた・origin に無いときは作らず理由を残す(取得の失敗だけは行に触らず次の回)。
# 積み重ね(#134 の第一段): add --depends-on PQ-<n> で、下の行の head(ブランチ)を base にする(base_source = stack)。
#   上の行は、下の行が PR になる(created)まで待つ(行は触らない)。下が却下・失敗なら上も却下。作る前に、下のブランチが origin にあり、
#   下の PR を作ったときに検査した SHA(head_sha)のままであることを確かめる。違えば作らず理由を残す(積み直し=force push は人がする。自動ではしない)。
#   マージの順(下から)と、base が既定ブランチでない PR では CI が走らないことがある点は、show に出すだけ(見張りはしない)。
PQ_BASE_FIXED=main
PQ_BASE=$PQ_BASE_FIXED   # いま処理している行の base(pq_base_check が行ごとに入れる)
PQ_MAX_BODY=102400
PQ_MAX_ATTEMPTS=3
PQ_AUTH_RC=75   # gh ラッパーが「認証で止まった(auth-locked)」を伝える終了コード(ghtkn の 111 をラッパーが読み替える)
PQ_MAX_CREATE=3   # 1 回の run で作る PR の上限(config/pr-queue.conf の max_create_per_run で変える)
PQ_REASON=""
PQ_SYNC_SHA=""

pq_body_root() {
  local d=${PR_QUEUE_BODY_ROOT:-$(pq_conf body_root "$TB_ROOT/data/pr-bodies")}
  (cd "$d" 2>/dev/null && pwd -P) || return 1
}

# 許可リスト(config/repos.md。TB_REPOS_FILE)の "- <org>/<repo>   # 注釈" から <org>/<repo> を 1 行ずつ。注釈(# 以降)は落とす。
# (notify_repos は注釈つきの行を読めないので使わない。実物の repos.md は注釈つき)
pq_repos() {
  local line f=${NOTIFY_REPOS_FILE:-}
  [[ -f $f ]] || return 0
  while IFS= read -r line || [[ -n $line ]]; do
    line=$(trim "$line"); [[ $line == -* ]] || continue
    line=$(trim "${line#-}"); line=$(trim "${line%%#*}")
    [[ $line =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]*/[A-Za-z0-9_][A-Za-z0-9_.-]*$ ]] && printf '%s\n' "$line"
  done <"$f"
  return 0
}

# 使う gh を決める。PR_QUEUE_GH(環境変数) > conf の gh。絶対パスの実行ファイルだけ(PATH 上の gh には落とさない)。
# 通れば 0 で PQ_GH に入れる。通らなければ警告を stderr に出して 1
PQ_GH=""
pq_gh() {
  local g=${PR_QUEUE_GH:-$(pq_conf gh "")} where=
  PQ_GH=""
  if [[ -z $g ]]; then
    echo "pr-queue: gh が指定されていないため止めます(fail-closed)。PR_QUEUE_GH か config/pr-queue.conf の gh に、専用の gh ラッパーの絶対パスを指定してください。PATH 上の gh には落としません" >&2
    return 1
  fi
  if [[ $g != /* || ! -f $g || ! -x $g ]]; then
    echo "pr-queue: 指定された gh を使えないため止めます(fail-closed)。絶対パスの実行ファイルを指定してください: $g" >&2
    return 1
  fi
  PQ_GH=$g
}

pq_ref() {  # "PQ-12" か "12" → 数字
  local r=${1#PQ-}; [[ $r =~ ^[0-9]+$ ]] || usage "pr-queue: ID は PQ-<数字>: $1"; printf '%s' "$r"
}

pq_row() { session_row pr_queue id "$1"; }

# ブランチの接頭辞(英数字と . _ - の後に / )。config/pr-queue.conf の branch_prefixes(空白かカンマ区切り。例: agent/ feat/ fix/)。
# 無ければ branch_prefix(1 つ。従来の書き方)、それも無ければ agent/ だけ。不正な要素は捨てる(狭める側に倒す)。全部不正なら agent/。広げるのは人が conf に書く
pq_prefixes() {
  local raw p out="" seen=" "
  raw=$(pq_conf branch_prefixes ""); [[ -n $raw ]] || raw=$(pq_conf branch_prefix agent/)
  for p in ${raw//,/ }; do
    [[ $p =~ ^[A-Za-z0-9._-]{1,40}/$ ]] || continue
    [[ $seen == *" $p "* ]] && continue
    seen+="$p "; out+="$p"$'\n'
  done
  [[ -n $out ]] || out=$'agent/\n'
  printf '%s' "$out"
}
pq_prefix() { pq_prefixes | head -n 1; }   # 先頭。scan が走査するのはこれだけ(広げた接頭辞のブランチは add で登録する)
pq_prefix_list() { pq_prefixes | paste -sd' ' -; }

pq_valid_branch() {  # どれかの接頭辞で始まり、安全な文字だけ
  local b=$1 p ok=1
  while IFS= read -r p; do
    [[ -n $p && $b == "$p"* && ${b#"$p"} =~ ^[A-Za-z0-9._/-]{1,120}$ ]] && { ok=0; break; }
  done < <(pq_prefixes)
  ((ok == 0)) || return 1
  [[ $b != *..* && $b != */ && $b != *.lock && $b != *//* ]] || return 1
}

pq_sha() { sha256 <"$1"; }

# ---------- base(PR の向き先)。#133 ----------
# ブランチ名として安全な形か(接頭辞は問わない)
pq_valid_ref() {
  local b=$1
  [[ $b =~ ^[A-Za-z0-9._/-]{1,200}$ && $b != -* && $b != /* && $b != */ && $b != *..* && $b != *//* && $b != *.lock ]]
}
pq_base_mode() { [[ $(pq_conf base_mode fixed) == default_branch ]] && echo default_branch || echo fixed; }   # 知らない値は fixed(今までどおり)に倒す

# origin の既定ブランチ(HEAD が指すブランチ)を出す。clone の origin に git ls-remote --symref で聞く(gh の権限を広げない)。取れなければ 1
pq_default_branch() {
  local git=${PR_QUEUE_GIT:-git} d=${PR_QUEUE_REPO_DIR:-} out b
  out=$("$git" -C "$d" ls-remote --symref origin HEAD 2>/dev/null) || return 1
  b=$(awk '$1=="ref:" && $3=="HEAD" { sub(/^refs\/heads\//, "", $2); print $2; exit }' <<<"$out")
  [[ -n $b ]] && pq_valid_ref "$b" || return 1
  printf '%s' "$b"
}

# いまの設定での既定の base を PQ_DEF_BASE に入れる。0 / 取れない 2(PQ_REASON)
PQ_DEF_BASE=""
pq_default_base() {
  PQ_DEF_BASE=$PQ_BASE_FIXED
  [[ $(pq_base_mode) == default_branch ]] || return 0
  PQ_DEF_BASE=$(pq_default_branch) || { PQ_DEF_BASE=""; PQ_REASON="origin の既定ブランチを確かめられない"; return 2; }
}

# 明示の base が許可されるか。$1=base $2=既定の base。既定と同じか、conf の base_allow(空白かカンマ区切りのグロブ。例: release/* develop)のどれかに合えば 0
pq_base_allowed() {
  local b=$1 p pats=()
  [[ $b == "$2" ]] && return 0
  read -ra pats <<<"$(pq_conf base_allow "" | tr ',' ' ')"
  for p in ${pats[@]+"${pats[@]}"}; do
    [[ $p =~ ^[A-Za-z0-9._/*-]{1,200}$ ]] || continue   # 不正な要素は捨てる(狭める側に倒す)
    # shellcheck disable=SC2053  # $p はグロブとして合わせる
    [[ $b == $p ]] && return 0
  done
  return 1
}

# origin のブランチの有無。0 ある / 1 無い / 2 確かめられない
pq_origin_state() {
  local git=${PR_QUEUE_GIT:-git} d=${PR_QUEUE_REPO_DIR:-} out
  out=$("$git" -C "$d" ls-remote --heads origin "refs/heads/$1" 2>/dev/null) || return 2
  awk -v t="refs/heads/$1" '$2==t{f=1} END{exit !f}' <<<"$out" || return 1
}

# 新しい行の base を決める(add / scan)。$1=明示の base(空なら設定どおり) $2=head のブランチ。
# PQ_NEW_BASE / PQ_NEW_SRC に入れて 0。使えなければ PQ_REASON に理由を入れて 1
PQ_NEW_BASE=""; PQ_NEW_SRC=""
pq_resolve_base() {
  local want=$1 head=$2 rc
  PQ_NEW_BASE=""; PQ_NEW_SRC=""; PQ_REASON=""
  pq_default_base || return 1
  if [[ -z $want ]]; then
    PQ_NEW_BASE=$PQ_DEF_BASE
    [[ $(pq_base_mode) == default_branch ]] && PQ_NEW_SRC=default || PQ_NEW_SRC=fixed
    [[ $PQ_NEW_BASE != "$head" ]] || { PQ_REASON="base($PQ_NEW_BASE)が head と同じ"; return 1; }
    return 0
  fi
  pq_valid_ref "$want" || { PQ_REASON="base はブランチ名として安全な形(英数字と . _ - /)だけ: $want"; return 1; }
  [[ $want != "$head" ]] || { PQ_REASON="base が head と同じ: $want"; return 1; }
  pq_base_allowed "$want" "$PQ_DEF_BASE" || { PQ_REASON="base($want)は許可されていない(既定の base の $PQ_DEF_BASE か、config/pr-queue.conf の base_allow の形だけ)"; return 1; }
  pq_origin_state "$want" && rc=0 || rc=$?
  ((rc == 0)) || { PQ_REASON="base($want)が origin に見つからない(か、確認できない)"; return 1; }
  PQ_NEW_BASE=$want; PQ_NEW_SRC=explicit
}

# 積み重ねの base を決める(add --depends-on)。$1=下の行の id $2=head のブランチ $3=リポジトリ。PQ_NEW_BASE / PQ_NEW_SRC に入れて 0。使えなければ PQ_REASON で 1
pq_resolve_stack() {
  local low st rc
  PQ_NEW_BASE=""; PQ_NEW_SRC=""; PQ_REASON=""
  low=$(pq_row "$1"); [[ -n $low ]] || { PQ_REASON="下の行が見つからない: PQ-$1"; return 1; }
  [[ $(jq -r .repo <<<"$low") == "$3" ]] || { PQ_REASON="下の PQ-$1 は別のリポジトリ"; return 1; }
  st=$(jq -r .status <<<"$low")
  [[ $st =~ ^(pending|approved|created)$ ]] || { PQ_REASON="下の PQ-$1 は $st(積めるのは pending / approved / created の上だけ)"; return 1; }
  [[ $st != created || -n $(jq -r '.head_sha // empty' <<<"$low") ]] || { PQ_REASON="下の PQ-$1 は作成時のコミットが記録されていない(この機能より前に作った PR)"; return 1; }
  PQ_NEW_BASE=$(jq -r .branch <<<"$low")
  [[ $PQ_NEW_BASE != "$2" ]] || { PQ_REASON="下の PQ-$1 と同じブランチ"; return 1; }
  pq_origin_state "$PQ_NEW_BASE" && rc=0 || rc=$?
  ((rc == 0)) || { PQ_REASON="下のブランチ($PQ_NEW_BASE)が origin に見つからない(か、確認できない)"; return 1; }
  PQ_NEW_SRC=stack
}

# 行の base をかけ直して PQ_BASE に入れる(approve と run が、origin と同期する前に呼ぶ)。$1=行(JSON) $2=approve なら、下の行を待っているだけのときは通す
# 0 使える / 1 使えない(PQ_REASON。run では却下)/ 2 確かめられない・下の行を待っている(PQ_REASON。行は触らず次の回)
pq_base_check() {
  local row=$1 base src rc def low lst lsha cur
  base=$(jq -r '.base // empty' <<<"$row"); src=$(jq -r '.base_source // empty' <<<"$row")
  PQ_BASE=$PQ_BASE_FIXED; PQ_REASON=""
  if [[ -z $base && -z $src ]]; then return 0; fi   # base を持たない古い行は今までどおり main
  pq_valid_ref "$base" || { PQ_REASON="base が安全な形ではない"; return 1; }
  case $src in
    fixed)
      [[ $base == "$PQ_BASE_FIXED" ]] || { PQ_REASON="base が固定の $PQ_BASE_FIXED ではない(行が書き換えられている)"; return 1; }
      return 0;;   # 今までどおり: origin への問い合わせは足さない
    default)
      def=$(pq_default_branch) || { PQ_REASON="origin の既定ブランチを確かめられない"; return 2; }
      [[ $def == "$base" ]] || { PQ_REASON="origin の既定ブランチが、登録したときの $base から変わった(登録し直す)"; return 1; };;
    explicit)
      pq_default_base || return 2
      pq_base_allowed "$base" "$PQ_DEF_BASE" || { PQ_REASON="base($base)が許可されていない(既定の base か base_allow の形だけ)"; return 1; };;
    stack)
      low=$(jq -r '.depends_on // empty' <<<"$row")
      [[ $low =~ ^[0-9]+$ ]] || { PQ_REASON="積み重ねの下の行が記録されていない"; return 1; }
      low=$(pq_row "$low"); [[ -n $low ]] || { PQ_REASON="下の行(PQ-$(jq -r .depends_on <<<"$row"))が見つからない"; return 1; }
      [[ $(jq -r .branch <<<"$low") == "$base" && $(jq -r .repo <<<"$low") == "$(jq -r .repo <<<"$row")" ]] \
        || { PQ_REASON="base が下の行のブランチと合わない(行が書き換えられている)"; return 1; }
      lst=$(jq -r .status <<<"$low"); lsha=$(jq -r '.head_sha // empty' <<<"$low")
      case $lst in
        rejected|failed) PQ_REASON="下の PQ-$(jq -r .id <<<"$low") が $lst(上の PR も作らない)"; return 1;;
        created) [[ -n $lsha ]] || { PQ_REASON="下の PQ-$(jq -r .id <<<"$low") の作成時のコミットが記録されていない"; return 1; };;
        *) if [[ ${2:-} != approve ]]; then PQ_REASON="下の PQ-$(jq -r .id <<<"$low") がまだ PR になっていない(${lst}。待つ)"; return 2; fi;;
      esac
      if [[ -n $lsha ]]; then
        cur=$(pq_origin_sha "$base") && rc=0 || { pq_origin_state "$base" && rc=2 || rc=$?; }
        ((rc == 2)) && { PQ_REASON="下のブランチ($base)を origin で確かめられない"; return 2; }
        ((rc == 0)) || { PQ_REASON="下のブランチ($base)が origin に無い(マージで消えた等。base を決め直して登録し直す)"; return 1; }
        [[ $cur == "$lsha" ]] || { PQ_REASON="下のブランチ($base)が、下の PR を作ったときのコミットから変わった(積み直しは人がする)"; return 1; }
      fi;;
    *) PQ_REASON="base の決め方が不明: $src"; return 1;;
  esac
  pq_origin_state "$base" && rc=0 || rc=$?
  ((rc == 2)) && { PQ_REASON="base($base)が origin にあるか確かめられない"; return 2; }
  ((rc == 0)) || { PQ_REASON="base($base)が origin に無い"; return 1; }
  PQ_BASE=$base
}

# 見つかった種類の名前だけを PQ_REASON に入れる(値は入れない)。$1=本文。見つかれば 0
pq_patterns_only() {
  local line name pat kinds="" h
  while IFS= read -r line; do
    [[ -n $line ]] || continue
    name=${line%%|*}; pat=${line#*|}
    h=$(printf '%s' "$1" | grep -oEi -- "$pat" 2>/dev/null || true)
    [[ -z $h ]] || kinds+="${kinds:+, }$name"
  done <<<"$NOTIFY_SECRET_PATTERNS"
  PQ_REASON=$kinds
  [[ -n $kinds ]]
}

# 本文・題・差分のガード。通れば 0。落ちれば PQ_REASON に理由(値は含めない)を入れて 1
# $1=行(JSON)。base は PQ_BASE(pq_base_check が行ごとに入れる。呼ばなければ main)
pq_guard() {
  local row=$1 repo branch title file sha now_sha first rest strip root dir abs re
  repo=$(jq -r .repo <<<"$row"); branch=$(jq -r .branch <<<"$row"); title=$(jq -r .title <<<"$row")
  file=$(jq -r .body_file <<<"$row"); sha=$(jq -r .body_sha256 <<<"$row")
  PQ_REASON=""
  pq_valid_branch "$branch" || { PQ_REASON="ブランチ名が許可された接頭辞($(pq_prefix_list))で始まる安全な形ではない"; return 1; }
  pq_repos | grep -qxF -- "$repo" || { PQ_REASON="リポジトリが許可リストに無い"; return 1; }
  root=$(pq_body_root) || { PQ_REASON="本文のルートが見つからない"; return 1; }
  [[ ! -L $file && -f $file ]] || { PQ_REASON="本文ファイルが無い(か、シンボリックリンク)"; return 1; }
  dir=$(cd "$(dirname "$file")" 2>/dev/null && pwd -P) || { PQ_REASON="本文ファイルの場所が不正"; return 1; }
  abs=$dir/$(basename "$file")
  [[ $abs == "$root"/* ]] || { PQ_REASON="本文ファイルが許可された場所(body_root)の外"; return 1; }
  now_sha=$(pq_sha "$abs")
  [[ $now_sha == "$sha" ]] || { PQ_REASON="本文が、置かれたときから変わっている(承認のやり直しが要る)"; return 1; }
  first=$(head -n 1 "$abs" | tr -d '\r'); re=$(pq_conf body_first_line "")
  if [[ -n $re ]]; then
    [[ $first =~ $re ]] || { PQ_REASON="本文 1 行目が body_first_line の形ではない"; return 1; }
    # 1 行目(形を検査済み)は除き、残りと題を秘密の検査にかける
    rest=$(tail -n +2 "$abs")
  else
    rest=$(cat "$abs")
  fi
  strip="$title"$'\n'"$rest"
  if notify_secret_scan "$strip"; then PQ_REASON="本文か題に秘密らしい値がある(種類: $NOTIFY_HIT_KINDS)"; NOTIFY_HITS=""; return 1; fi
  pq_diff_guard "$branch" || return 1
  return 0
}

# 検査する内容を origin の今の内容に固定する。ls-remote で origin のブランチの SHA を取り、ブランチと main を fetch して追跡参照(refs/remotes/origin/*)に取り込み、
# 追跡参照の SHA が一致しなければ止める。検査(差分のガード・公開前検査)は追跡参照だけを見る(ローカルブランチは使わない)。$1=ブランチ $2=1 なら dry(fetch しない)
# 戻り値: 0 一致 / 1 origin にブランチが無い(PQ_REASON) / 2 確認できない(取得の失敗・不一致。PQ_REASON。行は触らず次の回に回す)。PR_QUEUE_GIT で git を差し替える
pq_sync_origin() {
  local b=$1 dry=${2:-0} git=${PR_QUEUE_GIT:-git} d=${PR_QUEUE_REPO_DIR:-} out osha tsha
  out=$("$git" -C "$d" ls-remote --heads origin "refs/heads/$b" 2>/dev/null) || { PQ_REASON="origin の SHA を取得できない"; return 2; }
  osha=$(awk -v t="refs/heads/$b" '$2==t{print $1; exit}' <<<"$out")
  [[ -n $osha ]] || { PQ_REASON="ブランチが origin に無い"; return 1; }
  if ((!dry)); then
    "$git" -C "$d" fetch -q origin "+refs/heads/$b:refs/remotes/origin/$b" "+refs/heads/$PQ_BASE:refs/remotes/origin/$PQ_BASE" >/dev/null 2>&1 \
      || { PQ_REASON="origin から取り込めない(fetch に失敗)"; return 2; }
  fi
  tsha=$(git -C "$d" rev-parse -q --verify "refs/remotes/origin/$b^{commit}" 2>/dev/null) || { PQ_REASON="追跡参照が無い"; return 2; }
  [[ $tsha == "$osha" ]] || { PQ_REASON="追跡参照が origin の SHA と一致しない(取り込み後に origin が変わった)"; return 2; }
  PQ_SYNC_SHA=$osha
  return 0
}

# origin のブランチの今の SHA を出す(PR を作る直前の再確認用)。取れなければ 1
pq_origin_sha() {
  local git=${PR_QUEUE_GIT:-git} d=${PR_QUEUE_REPO_DIR:-} out sha
  out=$("$git" -C "$d" ls-remote --heads origin "refs/heads/$1" 2>/dev/null) || return 1
  sha=$(awk -v t="refs/heads/$1" '$2==t{print $1; exit}' <<<"$out"); [[ -n $sha ]] || return 1
  printf '%s' "$sha"
}

# PR_QUEUE_REPO_DIR が使えるか。使えなければ理由を PQ_REASON に入れて 1(差分を見られないので、承認も run もしない)
pq_repo_dir_ok() {
  local d=${PR_QUEUE_REPO_DIR:-}
  [[ -n $d ]] || { PQ_REASON="PR_QUEUE_REPO_DIR が空(差分のガードは必須。clone のパスを指定する)"; return 1; }
  [[ $d == /* ]] || { PQ_REASON="PR_QUEUE_REPO_DIR が絶対パスではない"; return 1; }
  [[ -d $d ]] || { PQ_REASON="PR_QUEUE_REPO_DIR が見つからない"; return 1; }
  git -C "$d" rev-parse -q --git-dir >/dev/null 2>&1 || { PQ_REASON="PR_QUEUE_REPO_DIR が git のリポジトリではない"; return 1; }
}

# 差分を見る(必須)。PR_QUEUE_REPO_DIR(ローカルの clone)が空・存在しない・git の作業ツリーでないときは、見られないので通さない(fail-closed)。clone を変更しない(読み取りだけ)
pq_diff_guard() {
  local b=$1 d=${PR_QUEUE_REPO_DIR:-} ref files added nums
  pq_repo_dir_ok || return 1
  ref=refs/remotes/origin/$b   # 検査は追跡参照だけ(ローカルブランチは使わない)
  git -C "$d" rev-parse -q --verify "$ref^{commit}" >/dev/null 2>&1 || { PQ_REASON="差分を取得できない(ブランチの追跡参照が clone に無い)"; return 1; }
  git -C "$d" rev-parse -q --verify "refs/remotes/origin/$PQ_BASE^{commit}" >/dev/null 2>&1 \
    || { PQ_REASON="差分を取得できない(origin/$PQ_BASE が clone に無い)"; return 1; }
  files=$(set -o pipefail; git -C "$d" -c core.quotePath=false diff -z --name-only "origin/$PQ_BASE...$ref" 2>/dev/null | tr '\0' '\n') || { PQ_REASON="差分を取得できない"; return 1; }
  if grep -qE '^\.github/(workflows|actions)/' <<<"$files"; then PQ_REASON="CI 設定(.github/workflows)を含む。人が作る"; return 1; fi
  nums=$(git -C "$d" diff --numstat "origin/$PQ_BASE...$ref" 2>/dev/null) || { PQ_REASON="差分を取得できない"; return 1; }
  if awk -F'\t' '$1=="-" && $2=="-"{f=1} END{exit !f}' <<<"$nums"; then
    PQ_REASON="バイナリの変更を含む(内容を検査できない)。人が作る"; return 1
  fi
  added=$(git -C "$d" -c core.quotePath=false diff -U0 "origin/$PQ_BASE...$ref" 2>/dev/null) || { PQ_REASON="差分を取得できない"; return 1; }
  added=$(grep -E '^\+[^+]' <<<"$added" || true)
  if grep -qE 'AdministratorAccess' <<<"$added"; then PQ_REASON="AdministratorAccess を足す差分を含む。人が作る"; return 1; fi
  if pq_patterns_only "$added"; then PQ_REASON="差分に秘密らしい値がある(種類: $PQ_REASON)"; return 1; fi
  return 0
}

# ---------- 公開前検査(deny-patterns)。run が PR を作る前に、題・本文・コミットメッセージ・ブランチ名・差分(変更したファイルのパスと足した行)に掛ける ----------
# 規則ファイル(PR_QUEUE_DENY_PATTERNS か conf の deny_patterns。絶対パス。必須)の書式: 1 行 = "<名前> <フラグ> <パターン(ERE)>"。# で始まる行と空行は無視。
#   フラグ i = 大文字小文字を無視 / s = 区別する。(~/developer/herdr/publish/deny-patterns.txt と同じ書式。社内名を含むのでこのリポジトリには置かない)
# 例外ファイル(PR_QUEUE_DENY_EXCEPTIONS か conf の deny_exceptions。任意): 1 行 = "<規則名> <ERE>"。その規則に合った「文字列(ヒットした部分)」が ERE に合えば無視
#   (規則のフラグと同じ大小文字の扱い。厳密にしたいときは ^ と $ を書く)。例: email ^[A-Za-z0-9._-]*noreply@example\.com$
# 未設定・読めない・書式が不正・規則が 0 件のときは、run は何もせず止まる(fail-closed)。ヒットした PR は作らず却下し(attempts は使わない)、規則名と位置だけを通知する(値は出さない)
PQ_DENY_NAMES=(); PQ_DENY_FLAGS=(); PQ_DENY_PATS=(); PQ_DENY_EXN=(); PQ_DENY_EXP=()
pq_abs_file() {  # $1=ラベル $2=パス。絶対パスの読めるファイルか。違えば理由を stderr に出して 1
  [[ $2 == /* && -f $2 && -r $2 ]] || { echo "pr-queue: $1 が絶対パスの読めるファイルではないため止めます(fail-closed): $2" >&2; return 1; }
}
# 公開前検査の設定は、環境変数では決めない(差し替えの口を作らない。ADR 0001)。<tb>/config/pr-queue.conf の固定のキーだけを読む(PR_QUEUE_CONF でも変えられない)
pq_deny_conf() {  # $1=キー
  local f=$TB_ROOT/config/pr-queue.conf v=""
  [[ -f $f ]] && v=$(grep -E "^[[:space:]]*$1[[:space:]]*=" "$f" | tail -n 1 | sed -E 's/^[^=]*=[[:space:]]*//; s/[[:space:]]*(#.*)?$//') || v=""
  printf '%s' "$v"
}
pq_deny_load() {
  local f x line name flag pat n=0 bad=0 re
  PQ_DENY_NAMES=(); PQ_DENY_FLAGS=(); PQ_DENY_PATS=(); PQ_DENY_EXN=(); PQ_DENY_EXP=()
  if [[ -n ${PR_QUEUE_DENY_PATTERNS:-}${PR_QUEUE_DENY_EXCEPTIONS:-} ]]; then
    echo "pr-queue: 警告: PR_QUEUE_DENY_PATTERNS / PR_QUEUE_DENY_EXCEPTIONS は無視します(環境変数では決めません)。config/pr-queue.conf の deny_patterns / deny_exceptions を使います" >&2
  fi
  f=$(pq_deny_conf deny_patterns)
  if [[ -z $f ]]; then
    echo "pr-queue: 公開前検査の規則ファイルが指定されていないため止めます(fail-closed)。config/pr-queue.conf の deny_patterns に絶対パスを指定してください" >&2
    return 1
  fi
  pq_abs_file "公開前検査の規則ファイル(deny_patterns)" "$f" || return 1
  while IFS= read -r line || [[ -n $line ]]; do
    line=${line%$'\r'}
    [[ -n ${line//[[:space:]]/} && ! $line =~ ^[[:space:]]*# ]] || continue
    read -r name flag pat <<<"$line"; n=$((n+1))
    if [[ ! $name =~ ^[A-Za-z0-9._-]+$ || ! $flag =~ ^[is]$ || -z $pat ]]; then echo "pr-queue: 規則ファイルの $n 番目の規則の書式が不正です(<名前> <i|s> <パターン>)" >&2; bad=1; continue; fi
    re=0; printf '' | grep -E -- "$pat" >/dev/null 2>&1 || re=$?
    if ((re > 1)); then echo "pr-queue: 規則ファイルの規則 $name の正規表現が不正です" >&2; bad=1; continue; fi
    PQ_DENY_NAMES+=("$name"); PQ_DENY_FLAGS+=("$flag"); PQ_DENY_PATS+=("$pat")
  done <"$f"
  if ((bad)) || ((${#PQ_DENY_NAMES[@]} == 0)); then
    echo "pr-queue: 公開前検査の規則ファイルが不正か、規則が 0 件のため止めます(fail-closed)" >&2; return 1
  fi
  x=$(pq_deny_conf deny_exceptions)
  if [[ -n $x ]]; then
    pq_abs_file "公開前検査の例外ファイル(deny_exceptions)" "$x" || return 1
    n=0
    while IFS= read -r line || [[ -n $line ]]; do
      line=${line%$'\r'}
      [[ -n ${line//[[:space:]]/} && ! $line =~ ^[[:space:]]*# ]] || continue
      read -r name pat <<<"$line"; n=$((n+1))
      re=0; printf '' | grep -E -- "$pat" >/dev/null 2>&1 || re=$?
      if [[ ! $name =~ ^[A-Za-z0-9._-]+$ || -z $pat ]] || ((re > 1)); then echo "pr-queue: 例外ファイルの $n 番目の書式が不正です(<規則名> <ERE>)" >&2; return 1; fi
      if printf '\n' | grep -qE -- "$pat" 2>/dev/null; then echo "pr-queue: 例外ファイルの $n 番目が空文字列に合います(何にでも合う例外は許しません)" >&2; return 1; fi
      PQ_DENY_EXN+=("$name"); PQ_DENY_EXP+=("$pat")
    done <"$x"
  fi
  return 0
}

# 1 行 = 1 単位。位置(pos)と内容(txt)を同じ行番号で並べる。$1=位置の接頭辞(行番号を後ろに付ける) 標準入力=テキスト
pq_deny_unit() { awk -v l="$1" -v pf="$PQ_DENY_POS" -v tf="$PQ_DENY_TXT" '{ print l ":" NR >> pf; print $0 >> tf }'; }

# $1=行(JSON)。clone の読み取りだけ。ヒットなし 0 / ヒットあり 1(PQ_REASON に「規則名@位置」だけ。値は入れない)/ 検査できない 2(PQ_REASON に理由)
pq_deny_check() {
  local row=$1 d=${PR_QUEUE_REPO_DIR:-} branch title file ref td c f i name flag pat gf out n frag j ex hits="" nh=0 key pos
  branch=$(jq -r .branch <<<"$row"); title=$(jq -r .title <<<"$row"); file=$(jq -r .body_file <<<"$row")
  ref=refs/remotes/origin/$branch   # 追跡参照だけ(ローカルブランチは使わない。run が直前に origin と同期している)
  git -C "$d" rev-parse -q --verify "$ref^{commit}" >/dev/null 2>&1 || { PQ_REASON="公開前検査ができない(ブランチの追跡参照が clone に無い)"; return 2; }
  td=$(mktemp -d) || { PQ_REASON="公開前検査ができない(作業領域)"; return 2; }
  PQ_DENY_POS=$td/pos; PQ_DENY_TXT=$td/txt; : >"$PQ_DENY_POS"; : >"$PQ_DENY_TXT"
  printf '%s\n' "title" >>"$PQ_DENY_POS"; printf '%s\n' "$title" >>"$PQ_DENY_TXT"
  printf '%s\n' "branch" >>"$PQ_DENY_POS"; printf '%s\n' "$branch" >>"$PQ_DENY_TXT"
  pq_deny_unit body <"$file"
  if ! out=$("${PR_QUEUE_GIT:-git}" -C "$d" rev-list "origin/$PQ_BASE..$ref" 2>/dev/null); then rm -rf "$td"; PQ_REASON="公開前検査ができない(コミットを取得できない)"; return 2; fi
  while IFS= read -r c; do
    [[ -n $c ]] || continue
    git -C "$d" log -1 --format=%B "$c" 2>/dev/null | pq_deny_unit "commit ${c:0:7}"
    printf 'author %s\n' "${c:0:7}" >>"$PQ_DENY_POS"; git -C "$d" log -1 --format='%an <%ae>' "$c" >>"$PQ_DENY_TXT" 2>/dev/null
    printf 'committer %s\n' "${c:0:7}" >>"$PQ_DENY_POS"; git -C "$d" log -1 --format='%cn <%ce>' "$c" >>"$PQ_DENY_TXT" 2>/dev/null
  done <<<"$out"
  # パス名は引用符なし・NUL 区切りで取る(非 ASCII のパスも規則に掛かる)。位置はパスではなく番号(値を出さない)
  if ! git -C "$d" -c core.quotePath=false diff -z --name-only "origin/$PQ_BASE...$ref" >"$td/names" 2>/dev/null; then rm -rf "$td"; PQ_REASON="公開前検査ができない(差分を取得できない)"; return 2; fi
  i=0
  while IFS= read -r -d '' f; do
    i=$((i+1)); printf 'path#%s\n' "$i" >>"$PQ_DENY_POS"; printf '%s\n' "$f" >>"$PQ_DENY_TXT"
  done <"$td/names"
  git -C "$d" -c core.quotePath=false diff -U0 --no-color "origin/$PQ_BASE...$ref" 2>/dev/null | awk -v pf="$PQ_DENY_POS" -v tf="$PQ_DENY_TXT" '
    /^diff --git / { hdr=1; fn++; next }   # ファイルの番号(path#<n> と同じ順)。パス名は位置に出さない
    hdr && /^(\+\+\+|---) / { next }
    /^@@/ { hdr=0; match($0, /\+[0-9]+/); n=substr($0,RSTART+1,RLENGTH-1)+0; next }
    !hdr && /^\+/ { print "diff#" fn ":" n >> pf; print substr($0,2) >> tf; n++ }'
  for i in "${!PQ_DENY_NAMES[@]}"; do
    name=${PQ_DENY_NAMES[$i]}; flag=${PQ_DENY_FLAGS[$i]}; pat=${PQ_DENY_PATS[$i]}
    gf=-noE; [[ $flag == i ]] && gf=-noEi
    out=$(grep $gf -- "$pat" "$PQ_DENY_TXT" 2>/dev/null || true)
    [[ -n $out ]] || continue
    while IFS= read -r line; do
      [[ -n $line ]] || continue
      n=${line%%:*}; frag=${line#*:}; ex=0
      for j in "${!PQ_DENY_EXN[@]}"; do
        [[ ${PQ_DENY_EXN[$j]} == "$name" ]] || continue
        if printf '%s' "$frag" | grep -qE${flag/s/} -- "${PQ_DENY_EXP[$j]}" 2>/dev/null; then ex=1; break; fi
      done
      ((ex == 0)) || continue
      pos=$(sed -n "${n}p" "$PQ_DENY_POS"); key="$name@$pos"
      [[ $hits == *"$key"* ]] && continue
      nh=$((nh+1)); ((nh > 5)) || hits+="${hits:+, }$key"
    done <<<"$out"
  done
  rm -rf "$td"
  ((nh == 0)) && return 0
  PQ_REASON="公開前検査にかかった($nh 件): $hits$( ((nh > 5)) && echo ' ほか')"
  return 1
}

cmd_pr-queue() {
  local sub=${1:-}; shift || true
  case $sub in
    add) pq_add "$@";;
    list) pq_list "$@";;
    show) pq_show "$@";;
    approve) pq_approve "$@";;
    reject) pq_reject "$@";;
    run) pq_run "$@";;
    scan) pq_scan "$@";;
    retry) pq_retry "$@";;
    *) usage "pr-queue add|list|show|approve|reject|retry|run|scan";;
  esac
}

# ブランチが origin に実在するか(PR_QUEUE_REPO_DIR の clone で git ls-remote。確認できなければ 1 = fail-closed)。PR_QUEUE_GIT で git を差し替える
pq_origin_has() {
  local b=$1 git=${PR_QUEUE_GIT:-git} d=${PR_QUEUE_REPO_DIR:-} out
  out=$("$git" -C "$d" ls-remote --heads origin "refs/heads/$b" 2>/dev/null) || return 1
  awk -v t="refs/heads/$b" '$2==t{f=1} END{exit !f}' <<<"$out"
}

pq_add() {
  local branch="" file="" title="" repo="" task="" base="" dep="" by id root dir abs n repos
  while [[ $# -gt 0 ]]; do
    case $1 in
      --branch) need_arg "$@"; branch=$2; shift 2;;
      --body-file) need_arg "$@"; file=$2; shift 2;;
      --title) need_arg "$@"; title=$2; shift 2;;
      --repo) need_arg "$@"; repo=$2; shift 2;;
      --task) need_arg "$@"; task=$2; shift 2;;
      --base) need_arg "$@"; base=$2; shift 2;;
      --depends-on) need_arg "$@"; dep=$(pq_ref "$2"); shift 2;;
      *) usage "pr-queue add: 不明な引数 $1";;
    esac
  done
  [[ -n $branch && -n $file && -n $title ]] || usage "pr-queue add --branch <接頭辞/…> --body-file <body_root の中のファイル> --title <題> [--repo <org/repo>] [--task T-…] [--base <ブランチ> | --depends-on PQ-<n>]"
  [[ -z $base || -z $dep ]] || usage "pr-queue add: --base と --depends-on は一緒に使えません"
  pq_valid_branch "$branch" || die "pr-queue: ブランチは $(pq_prefix_list) のどれかで始め、英数字と . _ - / だけにします: $branch"
  [[ $title != *$'\n'* && $title != *$'\r'* ]] && ! blank "$title" && ((${#title} <= 200)) || die "pr-queue: 題は 1 行・200 字まで"
  [[ -z $task ]] || valid_id "$task"
  repos=$(pq_repos)
  [[ -n $repos ]] || die "pr-queue: 許可リスト(config/repos.md)が空です。人に聞いてください"
  if [[ -z $repo ]]; then
    n=$(grep -c . <<<"$repos"); ((n == 1)) || die "pr-queue: 許可リストが複数あります。--repo を付けてください"
    repo=$repos
  fi
  grep -qxF -- "$repo" <<<"$repos" || die "pr-queue: リポジトリが許可リストにありません: $repo"
  pq_repo_dir_ok || die "pr-queue: $PQ_REASON"
  pq_origin_has "$branch" || die "pr-queue: ブランチが origin に見つからない(か、確認できない)。先に push してください: $branch"
  if [[ -n $dep ]]; then pq_resolve_stack "$dep" "$branch" "$repo" || die "pr-queue: $PQ_REASON"
  else pq_resolve_base "$base" "$branch" || die "pr-queue: $PQ_REASON"; dep=""; fi
  root=$(pq_body_root) || die "pr-queue: 本文のルート(body_root)が見つかりません"
  [[ ! -L $file && -f $file ]] || die "pr-queue: 本文ファイルが無い(かシンボリックリンク): $file"
  dir=$(cd "$(dirname "$file")" && pwd -P); abs=$dir/$(basename "$file")
  [[ $abs == "$root"/* ]] || die "pr-queue: 本文ファイルは body_root の中だけ: $file"
  (($(wc -c <"$abs") <= PQ_MAX_BODY)) || die "pr-queue: 本文が大きすぎます(100KB まで)"
  if [[ -n ${HERDR_PANE_ID:-} ]]; then by=$(pane_owner_name "$HERDR_PANE_ID" 2>/dev/null || true); by=${by:-unknown}; else by=human; fi
  id=$(sql "INSERT INTO main.pr_queue (herdr_session, repo,branch,base,base_source,depends_on,title,body_file,body_sha256,task_id,requested_by,status,created_at)
    VALUES ($SQS, $(q "$repo"),$(q "$branch"),$(q "$PQ_NEW_BASE"),$(q "$PQ_NEW_SRC"),${dep:-NULL},$(q "$title"),$(q "$abs"),$(q "$(pq_sha "$abs")"),NULLIF($(q "$task"),''),$(q "$by"),'pending',$(q "$(now)")); SELECT last_insert_rowid();")
  echo "PQ-$id pending"
}

pq_list() {
  local status="" json=0
  while [[ $# -gt 0 ]]; do
    case $1 in
      --status) need_arg "$@"; status=$2; shift 2;;
      --json) json=1; shift;;
      *) usage "pr-queue list [--status S] [--json]";;
    esac
  done
  [[ -z $status || $status =~ ^(pending|approved|rejected|created|failed)$ ]] || usage "pr-queue: status は pending|approved|rejected|created|failed"
  local where=""; [[ -z $status ]] || where="WHERE status=$(q "$status")"
  if ((json)); then sql -json "SELECT id,repo,branch,base,base_source,depends_on,title,task_id,requested_by,status,last_error,attempts,pr_url,created_at FROM pr_queue $where ORDER BY id;" | jq -c '.[]?'
  else sql -separator $'\t' "SELECT 'PQ-'||id, status, branch, title, COALESCE(base,'main') FROM pr_queue $where ORDER BY id;"; fi
}

pq_show() {
  [[ $# -eq 1 ]] || usage "pr-queue show <PQ-n>"
  local id row; id=$(pq_ref "$1"); row=$(pq_row "$id"); [[ -n $row ]] || die "見つかりません: PQ-$id"
  jq -r '"PQ-\(.id) \(.status)\nrepo: \(.repo)\nbranch: \(.branch)  → base: \(.base // "main")(\({fixed:"固定",default:"origin の既定ブランチ",explicit:"指定",stack:"PQ-\(.depends_on) の上に積む"}[.base_source // "fixed"] // .base_source)。draft)\ntitle: \(.title)\nbody: \(.body_file)\ntask: \(.task_id // "-")  依頼: \(.requested_by)\n試行: \(.attempts)  PR: \(.pr_url // "-")\n理由/エラー: \(.last_error // "-")"' <<<"$row"
  local dep ups
  dep=$(jq -r '.depends_on // empty' <<<"$row")
  [[ -z $dep ]] || echo "積み重ね: 下は PQ-$dep($(sql "SELECT status FROM pr_queue WHERE id=$dep;"))。マージは下から。base が既定ブランチでない PR では CI が走らないことがある"
  ups=$(sql "SELECT group_concat('PQ-'||id||'('||status||')', ' ') FROM pr_queue WHERE depends_on=$id;")
  [[ -z $ups ]] || echo "上に積まれている: $ups(この PR を先にマージする)"
}

pq_approve() {
  [[ $# -eq 1 ]] || usage "pr-queue approve <PQ-n>"
  require_human "tb pr-queue approve"
  local id row st; id=$(pq_ref "$1"); row=$(pq_row "$id"); [[ -n $row ]] || die "見つかりません: PQ-$id"
  st=$(jq -r .status <<<"$row"); [[ $st == pending ]] || die "PQ-$id は $st です(承認できるのは pending だけ)"
  pq_repo_dir_ok || die "PQ-$id は承認しません: $PQ_REASON"
  pq_base_check "$row" approve || die "PQ-$id は承認しません: $PQ_REASON"
  pq_sync_origin "$(jq -r .branch <<<"$row")" 0 || die "PQ-$id は承認しません: $PQ_REASON"
  if ! pq_guard "$row"; then die "PQ-$id はガードに落ちるため承認しません: $PQ_REASON"; fi
  sql "UPDATE main.pr_queue SET status='approved', decided_at=$(q "$(now)"), last_error=NULL WHERE herdr_session=$SQS AND id=$id AND status='pending';"
  echo "PQ-$id approved(base: ${PQ_BASE}。次の tb pr-queue run で draft PR を作ります)"
}

pq_reject() {
  [[ $# -ge 1 ]] || usage "pr-queue reject <PQ-n> [-m 理由]"
  local id note="" st row; id=$(pq_ref "$1"); shift
  while [[ $# -gt 0 ]]; do case $1 in -m|--message) need_arg "$@"; note=$2; shift 2;; *) usage "pr-queue reject: 不明な引数 $1";; esac; done
  ((${#note} <= 300)) || usage "pr-queue reject: 理由は 300 字まで"
  require_human "tb pr-queue reject"
  row=$(pq_row "$id"); [[ -n $row ]] || die "見つかりません: PQ-$id"
  st=$(jq -r .status <<<"$row"); [[ $st == pending || $st == approved ]] || die "PQ-$id は $st です(却下できるのは pending / approved だけ)"
  sql "UPDATE main.pr_queue SET status='rejected', decided_at=$(q "$(now)"), last_error=$(q "人が却下${note:+: $note}") WHERE herdr_session=$SQS AND id=$id AND status IN ('pending','approved');"
  echo "PQ-$id rejected"
}

# failed を人が approved に戻す(attempts を 0 に)。ガードは run のたびにかけ直すので、ここでは通さない
pq_retry() {
  [[ $# -eq 1 ]] || usage "pr-queue retry <PQ-n>"
  require_human "tb pr-queue retry"
  local id row st; id=$(pq_ref "$1"); row=$(pq_row "$id"); [[ -n $row ]] || die "見つかりません: PQ-$id"
  st=$(jq -r .status <<<"$row"); [[ $st == failed ]] || die "PQ-$id は $st です(戻せるのは failed だけ)"
  sql "UPDATE main.pr_queue SET status='approved', attempts=0, last_error=NULL, executed_at=NULL WHERE herdr_session=$SQS AND id=$id AND status='failed';"
  echo "PQ-$id approved(failed から戻しました。次の tb pr-queue run が、ガードを通して draft PR を作ります)"
}

# 認証で止まった(auth-locked)ときの記録と通知。approved の行すべてに印を付け、印の付いた行がまだ無かったとき(= 状態が変わったとき)だけ 1 回通知する。attempts は使わない
pq_auth_locked() {  # $1=dry
  echo "pr-queue: 認証で止まっています(auth-locked)。残りの行は処理せず、次の回に自動で再開します" >&2
  [[ $1 == 1 ]] && return 0
  local was; was=$(sql "SELECT COUNT(*) FROM pr_queue WHERE status='approved' AND last_error='auth-locked';")
  sql "UPDATE main.pr_queue SET last_error='auth-locked' WHERE herdr_session=$SQS AND status='approved';"
  ((was == 0)) && notify_emit warning "pr-queue-auth-locked" "" "pr-queue: 公開待ちがあるが、ghtkn が token を返せず止まっています。人の対話端末で ghtkn agent unlock --enable-refresh(ロック中)か ghtkn auth <app>(期限切れ)。再開は次の run が自動で拾います"
  return 0
}

# scheduler の COMMAND から。approved だけを処理する。--dry-run は gh pr create も DB 更新もしない
pq_run() {
  local dry=0 gh id row repo branch title file out rc n url made=0 maxc lrc locked=0 checked cur
  case ${1:-} in "") ;; --dry-run) dry=1;; *) usage "pr-queue run [--dry-run]";; esac
  pq_gh || return 1; gh=$PQ_GH
  # 環境の不備(clone が無い)で承認済みの行を却下しない。行には触らず、警告して止まる(直せば次の回に進む)
  pq_repo_dir_ok || { echo "pr-queue: ${PQ_REASON}。止めます(fail-closed。承認済みの行はそのまま残します)" >&2; return 1; }
  pq_deny_load || return 1
  maxc=$(pq_conf max_create_per_run "$PQ_MAX_CREATE"); [[ $maxc =~ ^[0-9]+$ ]] || maxc=$PQ_MAX_CREATE
  while IFS= read -r id; do
    [[ -n $id ]] || continue
    if ((made >= maxc)); then echo "pr-queue: 1 回の上限($maxc 件)に達した。残りは次の回"; break; fi
    row=$(pq_row "$id"); [[ -n $row ]] || continue
    repo=$(jq -r .repo <<<"$row"); branch=$(jq -r .branch <<<"$row"); title=$(jq -r .title <<<"$row"); file=$(jq -r .body_file <<<"$row")
    # base をかけ直す(既定ブランチが変わった・許可から外れた・origin に無いなら作らない)。確かめられなければ行は触らず次の回
    pq_base_check "$row" && rc=0 || rc=$?
    if ((rc == 2)); then echo "PQ-$id skipped: $PQ_REASON(作らない。次の回に回す)"; continue; fi
    if ((rc != 0)); then
      echo "PQ-$id rejected: $PQ_REASON"
      ((dry)) || sql "UPDATE main.pr_queue SET status='rejected', last_error=$(q "base: $PQ_REASON"), executed_at=$(q "$(now)") WHERE herdr_session=$SQS AND id=$id AND status='approved';"
      continue
    fi
    # 検査の直前に origin と同期する(承認後に別の clone から push し直された内容を、検査しないまま PR にしない)
    pq_sync_origin "$branch" "$dry" && rc=0 || rc=$?
    checked=$PQ_SYNC_SHA
    if ((rc == 2)); then echo "PQ-$id skipped: $PQ_REASON(検査できないので作らない。次の回に回す)"; continue; fi
    if ((rc == 1)) || ! pq_guard "$row"; then
      echo "PQ-$id rejected: $PQ_REASON"
      ((dry)) || sql "UPDATE main.pr_queue SET status='rejected', last_error=$(q "ガード: $PQ_REASON"), executed_at=$(q "$(now)") WHERE herdr_session=$SQS AND id=$id AND status='approved';"
      continue
    fi
    # 公開前検査(題・本文・コミットメッセージ・ブランチ名・差分)。ヒットしたら作らず却下(attempts は使わない)。規則名と位置だけを通知する
    pq_deny_check "$row" && rc=0 || rc=$?
    if ((rc == 2)); then echo "PQ-$id skipped: $PQ_REASON(公開前検査ができないので作らない。次の回に回す)"; continue; fi
    if ((rc != 0)); then
      echo "PQ-$id rejected: $PQ_REASON"
      if ((!dry)); then
        sql "UPDATE main.pr_queue SET status='rejected', last_error=$(q "公開前検査: $PQ_REASON"), executed_at=$(q "$(now)") WHERE herdr_session=$SQS AND id=$id AND status='approved';"
        notify_emit warning "pq-deny-$id" "" "PQ-$id は公開前検査で止めました(PR は作っていません): $PQ_REASON"
      fi
      continue
    fi
    # 重複確認(読み取り)。取れないときは作らない(次の回に回す)
    out=$("$gh" pr list --repo "$repo" --head "$branch" --state open --json number 2>/dev/null) && lrc=0 || lrc=$?
    if ((lrc == PQ_AUTH_RC)); then pq_auth_locked "$dry"; locked=1; break; fi
    if ((lrc != 0)) || ! jq -e 'type=="array"' <<<"$out" >/dev/null 2>&1; then
      echo "PQ-$id skipped: 重複確認の gh pr list に失敗(作らない)"
      ((dry)) || sql "UPDATE main.pr_queue SET attempts=attempts+1, last_error='重複確認に失敗' WHERE herdr_session=$SQS AND id=$id;"
      pq_cap_attempts "$id" "$dry"; continue
    fi
    n=$(jq 'length' <<<"$out")
    if ((n > 0)); then
      echo "PQ-$id rejected: 同じ head の open PR が既にある"
      ((dry)) || sql "UPDATE main.pr_queue SET status='rejected', last_error='同じ head の open PR が既にある', executed_at=$(q "$(now)") WHERE herdr_session=$SQS AND id=$id AND status='approved';"
      continue
    fi
    if ((dry)); then echo "PQ-$id would-create: $repo $branch → $PQ_BASE (draft)"; made=$((made+1)); continue; fi
    # 作る直前にもう一度 origin の SHA を見る。検査した SHA と違えば(重複確認のあいだに push し直された等)作らず、行は触らず次回に回す
    if ! cur=$(pq_origin_sha "$branch") || [[ $cur != "$checked" ]]; then
      echo "PQ-$id skipped: 検査したあとに origin のブランチが変わった(か、確認できない)。作らない(次の回に回す)"; continue
    fi
    out=$("$gh" pr create --repo "$repo" --base "$PQ_BASE" --head "$branch" --title "$title" --body-file "$file" --draft 2>/dev/null) && rc=0 || rc=$?
    if ((rc == 0)); then
      url=$(grep -Eo 'https://[^[:space:]]+/pull/[0-9]+' <<<"$out" | head -n 1 || true)
      sql "UPDATE main.pr_queue SET status='created', pr_url=NULLIF($(q "$url"),''), head_sha=$(q "$checked"), last_error=NULL, executed_at=$(q "$(now)") WHERE herdr_session=$SQS AND id=$id AND status='approved';"
      echo "PQ-$id created${url:+: $url}"; made=$((made+1))
    elif ((rc == PQ_AUTH_RC)); then pq_auth_locked 0; locked=1; break
    else
      sql "UPDATE main.pr_queue SET attempts=attempts+1, last_error=$(q "gh pr create が失敗(exit $rc)") WHERE herdr_session=$SQS AND id=$id;"
      echo "PQ-$id error: gh pr create が失敗(exit $rc)"
      pq_cap_attempts "$id" 0
    fi
  done < <(sql "SELECT id FROM pr_queue WHERE status='approved' ORDER BY id;")
  # 印を消すのは、この run が認証で止まらずに最後まで進んだときだけ(list が通って create が 75 のときに毎回消えて、毎回通知されるのを防ぐ)
  ((locked || dry)) || sql "UPDATE main.pr_queue SET last_error=NULL WHERE herdr_session=$SQS AND last_error='auth-locked';"
  return 0
}

pq_cap_attempts() {  # $1=id $2=dry。上限を超えたら failed にして、人に見える形にする
  [[ $2 == 1 ]] && return 0
  sql "UPDATE main.pr_queue SET status='failed', executed_at=$(q "$(now)") WHERE herdr_session=$SQS AND id=$1 AND status='approved' AND attempts>=$PQ_MAX_ATTEMPTS;"
}

# config/pr-queue.conf(人だけが編集。無ければ既定)の "<キー>=<値>" を読む。$1=キー $2=既定
pq_conf() {
  local f=${PR_QUEUE_CONF:-$TB_ROOT/config/pr-queue.conf} v
  [[ -f $f ]] && v=$(grep -E "^[[:space:]]*$1[[:space:]]*=" "$f" | tail -n 1 | sed -E 's/^[^=]*=[[:space:]]*//; s/[[:space:]]*(#.*)?$//') || v=""
  printf '%s' "${v:-$2}"
}

# ブランチ走査(scheduler の COMMAND で run の前に呼ぶ)。origin の <branch_prefix>* のうち条件を満たすものをキューに積む。
# 積むのは (a) 同じ head の PR(open/closed/merged)が無い (b) <body_root>/pr-body-<名>.md がある (c) ガード(1 行目の形など)を通り、{{…}} が残っていない
# (d) 秘密らしい値が無い (e) 最新コミットの題が空でない。auto_approve=1(conf。既定 0)のときだけ approved で積む。それ以外は pending
# 読み取りだけ: git は fetch と ls-remote、gh は pr list だけ。PR_QUEUE_REPO_DIR(clone)が要る。PR_QUEUE_GIT で git を差し替える
pq_scan() {
  local dry=0 repo="" gh git=${PR_QUEUE_GIT:-git} d=${PR_QUEUE_REPO_DIR:-} repos n auto st root
  local b slug file title out cnt row sha by id
  while [[ $# -gt 0 ]]; do
    case $1 in --dry-run) dry=1; shift;; --repo) need_arg "$@"; repo=$2; shift 2;; *) usage "pr-queue scan [--dry-run] [--repo <org/repo>]";; esac
  done
  repos=$(pq_repos); [[ -n $repos ]] || { echo "pr-queue scan: 許可リスト(config/repos.md)が空" >&2; return 0; }
  if [[ -z $repo ]]; then n=$(grep -c . <<<"$repos"); ((n == 1)) || { echo "pr-queue scan: 許可リストが複数。--repo を付ける" >&2; return 0; }; repo=$repos; fi
  grep -qxF -- "$repo" <<<"$repos" || { echo "pr-queue scan: リポジトリが許可リストに無い" >&2; return 0; }
  pq_repo_dir_ok || { echo "pr-queue scan: ${PQ_REASON}。止めます(fail-closed)" >&2; return 1; }
  pq_gh || return 1; gh=$PQ_GH
  root=$(pq_body_root) || { echo "pr-queue scan: 本文のルートが無い" >&2; return 0; }
  auto=$(pq_conf auto_approve 0); [[ $auto == 1 ]] && st=approved || st=pending
  local heads pfx; pfx=$(pq_prefix)
  heads=$("$git" -C "$d" ls-remote --heads origin "refs/heads/${pfx}*" 2>/dev/null | awk '{print $2}' | sed 's#^refs/heads/##') \
    || { echo "pr-queue scan: origin の一覧を取れない" >&2; return 0; }
  [[ -n $heads ]] || { echo "pr-queue scan: 対象ブランチなし"; return 0; }
  ((dry)) || "$git" -C "$d" fetch -q origin "+refs/heads/${pfx}*:refs/remotes/origin/${pfx}*" 2>/dev/null || true
  # base_mode = default_branch なら、差分のガードが比べる既定ブランチも取り込む(取れなければ各行のガードが理由つきで飛ばす)
  if ((!dry)) && [[ $(pq_base_mode) == default_branch ]] && pq_default_base 2>/dev/null; then
    "$git" -C "$d" fetch -q origin "+refs/heads/$PQ_DEF_BASE:refs/remotes/origin/$PQ_DEF_BASE" 2>/dev/null || true
  fi
  while IFS= read -r b; do
    [[ -n $b ]] || continue
    pq_valid_branch "$b" || { echo "skip $b: ブランチ名が安全な形ではない"; continue; }
    cnt=$(sql "SELECT COUNT(*) FROM pr_queue WHERE repo=$(q "$repo") AND branch=$(q "$b");")
    if (($(sql "SELECT COUNT(*) FROM pr_queue WHERE repo=$(q "$repo") AND branch=$(q "$b") AND status<>'rejected';") > 0)); then echo "skip $b: 既にキューにある"; continue; fi
    if ((cnt >= PQ_MAX_ATTEMPTS)); then echo "skip $b: 同じブランチの試行が上限($PQ_MAX_ATTEMPTS)"; continue; fi
    slug=${b//\//-}; file=$root/pr-body-$slug.md
    [[ -f $file && ! -L $file ]] || { echo "skip $b: 本文(pr-body-$slug.md)が無い"; continue; }
    if grep -qE '\{\{[^}]*\}\}' "$file"; then echo "skip $b: 本文にプレースホルダ {{…}} が残っている"; continue; fi
    title=$("$git" -C "$d" log -1 --format=%s "refs/remotes/origin/$b" 2>/dev/null | head -n 1 | tr -d '\r') || title=""
    blank "$title" && { echo "skip $b: 最新コミットの題が空(か取得できない)"; continue; }
    ((${#title} <= 200)) || title=${title:0:200}
    sha=$(pq_sha "$file")
    pq_resolve_base "" "$b" || { echo "skip $b: $PQ_REASON"; continue; }
    row=$(jq -nc --arg r "$repo" --arg b "$b" --arg t "$title" --arg f "$file" --arg s "$sha" --arg ba "$PQ_NEW_BASE" --arg bs "$PQ_NEW_SRC" '{repo:$r,branch:$b,title:$t,body_file:$f,body_sha256:$s,base:$ba,base_source:$bs}')
    pq_base_check "$row" >/dev/null || { echo "skip $b: $PQ_REASON"; continue; }
    # 本文・題・(clone があれば)差分のガードは、承認前でも同じものを通す。落ちたものは積まない
    if ! pq_guard "$row"; then echo "skip $b: $PQ_REASON"; continue; fi
    # 重複確認: open / closed / merged のどれでも、同じ head の PR があれば積まない。取れなければ積まない
    if ! out=$("$gh" pr list --repo "$repo" --head "$b" --state all --json number 2>/dev/null) || ! jq -e 'type=="array"' <<<"$out" >/dev/null 2>&1; then
      echo "skip $b: 重複確認の gh pr list に失敗"; continue
    fi
    if (($(jq length <<<"$out") > 0)); then echo "skip $b: 同じ head の PR が既にある"; continue; fi
    if ((dry)); then echo "would-queue $b ($st): $title"; continue; fi
    by=scan
    id=$(sql "INSERT INTO main.pr_queue (herdr_session, repo,branch,base,base_source,title,body_file,body_sha256,requested_by,status,created_at,decided_at)
      VALUES ($SQS, $(q "$repo"),$(q "$b"),$(q "$PQ_NEW_BASE"),$(q "$PQ_NEW_SRC"),$(q "$title"),$(q "$file"),$(q "$sha"),'scan',$(q "$st"),$(q "$(now)"),$([[ $st == approved ]] && q "$(now)" || echo NULL)); SELECT last_insert_rowid();")
    echo "PQ-$id $st: $b"
  done <<<"$heads"
  return 0
}
