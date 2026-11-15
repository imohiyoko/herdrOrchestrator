# shellcheck shell=bash
# run.sh から番号順に source される。権限カタログ(config/approvals-catalog.conf)と、コード・既定の approvals.conf のずれを防ぐ。
# 決定論(静的な grep と glob 照合だけ。tb は実行しない)
CAT=$HERE/../config/approvals-catalog.conf
TBROOT=$HERE/..
REPO=$HERE/../..

# 空白を落とす(bash だけで)
_ct() { local s=$1; s="${s#"${s%%[![:space:]]*}"}"; printf '%s' "${s%"${s##*[![:space:]]}"}"; }

# --- カタログを読む: 各行は 7 項目(パターン | 種別 | 副作用 | リスク | allow 候補 | 状態 | 説明)
CAT_PATS=(); cat_bad=0; cat_n=0; cat_active=0; cat_reserved=0; cat_seen=" "
CAT_RE_PAT='^[A-Za-z0-9._:*?-]{1,128}$'
while IFS= read -r line || [[ -n $line ]]; do
  t=$(_ct "$line"); [[ -z $t || $t == \#* ]] && continue
  cat_n=$((cat_n+1))
  IFS='|' read -r f_pat f_kind f_side f_risk f_allow f_state f_desc <<<"$line"
  f_pat=$(_ct "$f_pat"); f_kind=$(_ct "$f_kind"); f_side=$(_ct "$f_side"); f_risk=$(_ct "$f_risk")
  f_allow=$(_ct "$f_allow"); f_state=$(_ct "$f_state"); f_desc=$(_ct "$f_desc")
  why=""
  [[ $f_pat =~ $CAT_RE_PAT ]] || why="パターンの文字集合"
  case $f_kind in send|agent|alert|proxy|role|gate|setup) ;; *) why="種別";; esac
  [[ ${f_pat%%:*} == "$f_kind" ]] || why="種別がパターンの先頭と違う"
  case $f_side in none|local-write|external-send|external-write) ;; *) why="副作用";; esac
  case $f_risk in low|mid|high|human-only) ;; *) why="リスク階層";; esac
  case $f_allow in yes|no|conditional) ;; *) why="allow 候補";; esac
  case $f_state in active) cat_active=$((cat_active+1));; reserved) cat_reserved=$((cat_reserved+1));; *) why="状態";; esac
  [[ -n $f_desc ]] || why="説明が空"
  [[ $f_risk != human-only || $f_allow == no ]] || why="human-only なのに allow 候補が no でない"
  [[ $cat_seen != *" $f_pat "* ]] || why="パターンが重複"
  cat_seen+="$f_pat "
  [[ -z $why ]] || { cat_bad=$((cat_bad+1)); ng "catalog: $why: $f_pat"; }
  CAT_PATS+=("$f_pat")
done <"$CAT"
eq "catalog: 全行の形式(必須項目・値の列挙・文字集合)" "$cat_bad" "0"
eq "catalog: 行が読めている(active と reserved の両方がある)" "$((cat_n >= 15 && cat_active >= 10 && cat_reserved >= 1))" "1"

# 候補パターンがカタログのどれかと重なるか(glob を双方向に当てる。動的な部分は * として受ける)
_cat_covers() {  # $1=候補(glob)
  local p
  for p in "${CAT_PATS[@]}"; do
    # shellcheck disable=SC2053  # glob として比べる
    if [[ $p == $1 || $1 == $p ]]; then return 0; fi
  done
  return 1
}

# --- (a) コードが承認判定に渡す操作名が、カタログのどれかに載っている
_ops=$(
  { grep -hoE '(approval_of|set_approval (allow|ask)) +"[^"]+"' "$TBROOT"/lib/*.sh "$TBROOT"/bin/tb | sed -E 's/^[^"]*"//; s/"$//'
    grep -hoE 'tb approval [A-Za-z0-9:<>*._-]+' "$REPO"/roles/*.md "$REPO"/setup/CLAUDE.md | sed -E 's/^tb approval //; s/<[^>]*>/*/g'
  } | sed -E 's/\$\{[^}]*\}|\$[A-Za-z0-9_]+/*/g' | grep -E '^[a-z][A-Za-z0-9._:*?-]*$' | sort -u)   # 「tb approval <操作>」のような説明文の記号は除く
n_ops=$(printf '%s\n' "$_ops" | grep -c .)
eq "catalog: コードから承認判定の操作名を集められている(grep が壊れていない)" "$((n_ops >= 8))" "1"
for must in 'alert:analyze' 'agent:compact:*' 'agent:spawn:*' 'role:change' 'setup:*'; do
  eq "catalog: 抽出に $must が含まれる" "$(printf '%s\n' "$_ops" | grep -cxF -- "$must")" "1"
done
miss=""
while IFS= read -r op; do
  [[ -n $op ]] || continue
  _cat_covers "$op" || miss+="$op "
done <<<"$_ops"
eq "catalog: コード中の操作名がすべてカタログのパターンに一致する" "${miss:-none}" "none"

# --- (c) 既定の approvals.conf の規則がカタログに載っている(未知の規則)
_conf_unknown() {  # stdin=conf の本文 → 未知の規則を空白区切りで出す
  local ln k v out=""
  while IFS= read -r ln || [[ -n $ln ]]; do
    ln=$(_ct "${ln%%#*}"); [[ $ln == *=* ]] || continue
    k=$(_ct "${ln%%=*}"); v=$(_ct "${ln#*=}")
    [[ $v == ask || $v == allow ]] || continue
    [[ $k == '*' ]] && continue   # 末尾の既定
    _cat_covers "$k" || out+="$k "
  done
  printf '%s' "$out"
}
ex=$(git -C "$REPO" show HEAD:taskboard/config/approvals.conf 2>/dev/null) || ex=$(cat "$TBROOT/config/approvals.conf")
eq "catalog: 既定の approvals.conf(コミット済み)の規則がすべてカタログに載っている" "$(printf '%s\n' "$ex" | _conf_unknown)" ""
# 手元の approvals.conf(人が書き換えたもの)は、未知の規則を警告するだけ(テストは落とさない)
w=$(_conf_unknown <"$TBROOT/config/approvals.conf")
[[ -z $w ]] && ok "catalog: 手元の approvals.conf に未知の規則は無い" || echo "warn catalog: 手元の approvals.conf にカタログに無い規則: $w"
eq "catalog: 未知の規則の検出が働く(自己テスト)" "$(printf 'zzz:unknown = allow\nsend:task:dd-* = allow\n* = ask\n' | _conf_unknown)" "zzz:unknown "
