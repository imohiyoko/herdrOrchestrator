# shellcheck shell=bash
# 会話チーム(#95)と、会話の振り分け表(#96)。bin/tb から source される。単体では実行しない。
# 設計と書式: conversation/README.md
#
# 2 つの層に分ける(間で受け渡すのは区分の名前・参照・一般化した説明だけ。文面は渡さない):
#   振り分けの層(tb conv-route)  構造化した依頼 → {種別, 相手の区分, 経路の区分, 時機, 重大度}。人が書く決定論の表(設定 conv-routes)
#   下書きの層(tb conv-draft)    振り分けの結果 + 一般化した状況 + 参照 → 下書きの指示(共通の土台 + 種別ごとのチーム + 採用済みのパターン)
# 声の役(tb conv-voice)は下書きと自分の指示だけを受け取り、1〜3 点を返す(ほかの役の出力は渡さない)。
#
# 文面(依頼・下書き・最終文)は DB にも記録にも書かない。ここのコマンドはファイルと標準入力を読んで標準出力に出すだけで、
# DB に書くのは人が振り分け表を変える tb conv-routes-set(設定の行と履歴)だけ。
#
# 種別は conversation/kinds/<種別>/ のファイル(prompt.md・patterns.conf)を足すだけで増やせる(コードの変更は要らない)。
# TB_CONV_TEAMS=off でチームの層を止める(どの種別も共通の土台だけで下書きする)。

CONV_DIR=${TB_CONV_DIR:-$TB_ROOT/../conversation}
CONV_NAME_RE='^[a-z][a-z0-9-]{0,39}$'
CONV_EVIDENCE="human-edit human-written other-source agent-generated"
CONV_VOICE_MAX=3
CONV_NOTE_MAX=300

conv_valid_name() { [[ $1 =~ $CONV_NAME_RE ]]; }
conv_need_name() { conv_valid_name "$2" || usage "$1 は英小文字で始まる 40 文字までの英小文字・数字・-: $2"; }
conv_teams_on() { [[ ${TB_CONV_TEAMS:-on} != off ]]; }
conv_has_team() { conv_valid_name "$1" && [[ -f $CONV_DIR/kinds/$1/prompt.md && ! -L $CONV_DIR/kinds/$1/prompt.md ]]; }

# 文字数(コードポイント。ロケールに左右されないよう jq で数える。末尾の空白・改行は数えない)
conv_chars() { jq -Rs 'sub("\\s+$"; "") | length' <"$1"; }
conv_strlen() { jq -Rrs 'length' < <(printf '%s' "$1"); }   # 文字列の文字数(${#s} はロケールが C だとバイト数になる)
conv_need_file() { [[ -f $2 && -r $2 ]] || usage "$1 のファイルを読めません: $2"; }

# 一般化した説明か(数字・URL・メール・@名前・#番号を含まない)。$1=何の値か $2=本文。だめなら CONV_ERR に理由を入れて 1
CONV_ERR=""
CONV_DIGIT_RE='[0-9]|０|１|２|３|４|５|６|７|８|９'   # 全角は 1 文字ずつ(範囲で書くとロケールによってはバイトの範囲になる)
conv_generalized() {
  CONV_ERR=""
  if [[ $2 =~ $CONV_DIGIT_RE ]]; then CONV_ERR="$1 に数字があります(件数・番号・日付は書かず、一般化した言い方にする)"
  elif [[ $2 == *://* || $2 == *www.* ]]; then CONV_ERR="$1 に URL があります(参照は --ref で渡す)"
  elif [[ $2 == *@* || $2 == *＠* ]]; then CONV_ERR="$1 に @ があります(名前・メールは書かない)"
  elif [[ $2 == *#* || $2 == *＃* ]]; then CONV_ERR="$1 に # があります(チケット・レビューの番号は書かない)"
  elif [[ $2 == *'|'* || $2 == *$'\n'* || $2 == *$'\t'* ]]; then CONV_ERR="$1 に | ・改行・タブは使えません"
  else return 0; fi
  return 1
}

# ---------- パターンの台帳(conversation/kinds/<種別>/patterns.conf) ----------
# 1 行 1 パターン。| 区切りの 8 列: id | state | evidence | situation | intent | ending | conclusion | avoid
#   id: P-<英数字 -> / state: candidate|adopted / evidence: human-edit|human-written|other-source|agent-generated
#   conclusion: first|last(結論を先頭に置くか末尾か)/ avoid: 避ける言い回し(無ければ -)
# 下書きに渡すのは adopted だけ。agent-generated の adopted は無効(自分の癖を強めないため)。
# conv_patterns_read <ファイル>: 正しい行を CONV_PAT(| 区切り 8 列。前後の空白を除いたもの)に、誤りを CONV_PAT_ERRS に入れる
conv_patterns_read() {
  local f=$1 line n=0 i v id state ev concl
  local -a c
  CONV_PAT=(); CONV_PAT_ERRS=()
  [[ -f $f ]] || return 0
  while IFS= read -r line || [[ -n $line ]]; do
    n=$((n + 1)); line=$(trim "$line")
    [[ -z $line || $line == \#* ]] && continue
    IFS='|' read -ra c <<<"$line|"
    if [[ ${#c[@]} -ne 8 ]]; then CONV_PAT_ERRS+=("L$n: 列は 8 つ(id | state | evidence | situation | intent | ending | conclusion | avoid)です"); continue; fi
    for i in 0 1 2 3 4 5 6 7; do c[i]=$(trim "${c[i]}"); done
    id=${c[0]} state=${c[1]} ev=${c[2]} concl=${c[6]}
    [[ $id =~ ^P-[A-Za-z0-9-]{1,20}$ ]] || { CONV_PAT_ERRS+=("L$n: id は P-<英数字>: $id"); continue; }
    [[ $state == candidate || $state == adopted ]] || { CONV_PAT_ERRS+=("L$n $id: state は candidate か adopted: $state"); continue; }
    [[ " $CONV_EVIDENCE " == *" $ev "* ]] || { CONV_PAT_ERRS+=("L$n $id: evidence は $CONV_EVIDENCE のどれか: $ev"); continue; }
    [[ $concl == first || $concl == last ]] || { CONV_PAT_ERRS+=("L$n $id: conclusion は first か last: $concl"); continue; }
    if [[ $state == adopted && $ev == agent-generated ]]; then CONV_PAT_ERRS+=("L$n $id: agent-generated を根拠に adopted にはできません"); continue; fi
    v=ok
    for i in 3 4 5 7; do
      if [[ -z ${c[i]} ]]; then CONV_PAT_ERRS+=("L$n $id: 空の列があります(無いときは -)"); v=ng; break; fi
      if (($(conv_strlen "${c[i]}") > 120)); then CONV_PAT_ERRS+=("L$n $id: 列が長すぎます(120 文字まで)"); v=ng; break; fi
      conv_generalized "$id" "${c[i]}" || { CONV_PAT_ERRS+=("L$n $CONV_ERR"); v=ng; break; }
    done
    [[ $v == ok ]] || continue
    if [[ " ${CONV_PAT[*]+${CONV_PAT[*]%%|*}} " == *" $id "* ]]; then CONV_PAT_ERRS+=("L$n: id が重なっています: $id"); continue; fi
    CONV_PAT+=("$id|$state|$ev|${c[3]}|${c[4]}|${c[5]}|$concl|${c[7]}")
  done <"$f"
  return 0
}

# ---------- 振り分け表(設定 conv-routes) ----------
# 書式(conversation/README.md・taskboard/config/conv-routes.conf.example):
#   version = <整数>                            必須。変えるたびに増やす
#   counterparts = <区分> <区分> ...           登録済みの相手の区分。ここに無い区分は推測せず「未登録」
#   <event> <counterpart> <depth> <severity> <deadline> -> kind=<種別> channel=<経路の区分> timing=<時機> [severity=<重大度>|keep]
# 各欄は * か、名前(英小文字・数字・-)か、カンマ区切りの名前。完全一致だけで比べる。上から見て最初に一致した行で決まる。
# 判定は awk 1 か所(_conv_routes_awk)。同じ依頼にはいつも同じ結果を返す(時刻・乱数・本文を使わない)
_conv_routes_awk() {  # 標準入力なし。awk のプログラムを出す
  cat <<'AWK'
function trim(s) { sub(/^[ \t\r]+/, "", s); sub(/[ \t\r]+$/, "", s); return s }
function okname(s) { return s ~ /^[a-z][a-z0-9-]*$/ && length(s) <= 40 }
function okpat(s,   a, n, i) {
  if (s == "*") return 1
  n = split(s, a, ","); if (n < 1) return 0
  for (i = 1; i <= n; i++) if (!okname(a[i])) return 0
  return 1
}
# 表を読む。t = 0(今の表)か 1(新しい表)。誤りは ERR に入れる
function load(file, t,   line, ln, lhs, rhs, f, nf, o, no, i, k, v, kv, seen) {
  NR_[t] = 0; VER[t] = ""; NCP[t] = 0
  ln = 0
  while ((getline line < file) > 0) {
    ln++; line = trim(line)
    if (line == "" || line ~ /^#/) continue
    if (line ~ /^version[ \t]*=/) {
      v = trim(substr(line, index(line, "=") + 1))
      if (v !~ /^[0-9]+$/ || v + 0 < 1) { ERR = "L" ln ": version は 1 以上の整数です"; return 0 }
      if (VER[t] != "") { ERR = "L" ln ": version が 2 回あります"; return 0 }
      VER[t] = v + 0; continue
    }
    if (line ~ /^counterparts[ \t]*=/) {
      no = split(trim(substr(line, index(line, "=") + 1)), o, /[ \t]+/)
      for (i = 1; i <= no; i++) {
        if (o[i] == "") continue
        if (!okname(o[i])) { ERR = "L" ln ": 相手の区分の名前が不正です: " o[i]; return 0 }
        CP[t, o[i]] = 1; NCP[t]++; CPL[t, NCP[t]] = o[i]
      }
      continue
    }
    if (index(line, "->") == 0) { ERR = "L" ln ": 'version = N' / 'counterparts = ...' / '<5 欄> -> kind=... channel=... timing=...' のどれでもありません"; return 0 }
    lhs = trim(substr(line, 1, index(line, "->") - 1)); rhs = trim(substr(line, index(line, "->") + 2))
    nf = split(lhs, f, /[ \t]+/)
    if (nf != 5) { ERR = "L" ln ": 左辺は 5 欄(event counterpart depth severity deadline)です"; return 0 }
    for (i = 1; i <= 5; i++) if (!okpat(f[i])) { ERR = "L" ln ": 欄 " i " は * か名前(カンマ区切り可)です: " f[i]; return 0 }
    no = split(rhs, o, /[ \t]+/)
    delete seen
    for (i = 1; i <= no; i++) {
      if (index(o[i], "=") == 0) { ERR = "L" ln ": 右辺は キー=値 です: " o[i]; return 0 }
      k = substr(o[i], 1, index(o[i], "=") - 1); v = substr(o[i], index(o[i], "=") + 1)
      if (k != "kind" && k != "channel" && k != "timing" && k != "severity") { ERR = "L" ln ": 右辺のキーは kind / channel / timing / severity です: " k; return 0 }
      if (!okname(v)) { ERR = "L" ln ": " k " の値が不正です: " v; return 0 }
      if (k in seen) { ERR = "L" ln ": " k " が 2 回あります"; return 0 }
      seen[k] = v
    }
    if (!("kind" in seen) || !("channel" in seen) || !("timing" in seen)) { ERR = "L" ln ": 右辺には kind / channel / timing が要ります"; return 0 }
    n = ++NR_[t]
    for (i = 1; i <= 5; i++) R[t, n, i] = f[i]
    R[t, n, "kind"] = seen["kind"]; R[t, n, "channel"] = seen["channel"]; R[t, n, "timing"] = seen["timing"]
    R[t, n, "severity"] = ("severity" in seen) ? seen["severity"] : "keep"
    R[t, n, "line"] = ln
  }
  close(file)
  if (VER[t] == "") { ERR = "version の行がありません"; return 0 }
  return 1
}
function fmatch(p, v,   a, n, i) {
  if (p == "*") return 1
  n = split(p, a, ",")
  for (i = 1; i <= n; i++) if (a[i] == v) return 1
  return 0
}
# 依頼 q[1..5] を表 t で振り分ける。返り値: "routed<TAB>行<TAB>kind<TAB>相手<TAB>経路<TAB>時機<TAB>重大度" か "unregistered<TAB>理由"
function route(t, q,   n, i, ok, sev) {
  if (!((t, q[2]) in CP)) return "unregistered\tcounterpart"
  for (n = 1; n <= NR_[t]; n++) {
    ok = 1
    for (i = 1; i <= 5; i++) if (!fmatch(R[t, n, i], q[i])) { ok = 0; break }
    if (!ok) continue
    sev = R[t, n, "severity"]; if (sev == "keep") sev = q[4]
    return "routed\t" R[t, n, "line"] "\t" R[t, n, "kind"] "\t" q[2] "\t" R[t, n, "channel"] "\t" R[t, n, "timing"] "\t" sev
  }
  return "unregistered\tno-route"
}
function strip_line(r,   a) { if (r !~ /^routed/) return r; split(r, a, "\t"); return a[1] "\t" a[3] "\t" a[4] "\t" a[5] "\t" a[6] "\t" a[7] }
# 欄 i に出てくる名前(両方の表)+ どれにも一致しない値 "~other"
function domain(i,   t, n, a, m, j, cnt, key) {
  cnt = 0; delete DSEEN
  for (t = 0; t <= 1; t++) {
    for (n = 1; n <= NR_[t]; n++) {
      if (R[t, n, i] == "*") continue
      m = split(R[t, n, i], a, ",")
      for (j = 1; j <= m; j++) if (!(a[j] in DSEEN)) { DSEEN[a[j]] = 1; D[i, ++cnt] = a[j] }
    }
    if (i == 2) for (j = 1; j <= NCP[t]; j++) if (!(CPL[t, j] in DSEEN)) { DSEEN[CPL[t, j]] = 1; D[i, ++cnt] = CPL[t, j] }
  }
  D[i, ++cnt] = "~other"; DN[i] = cnt
  return cnt
}
BEGIN {
  FS = "\t"
  if (mode == "check") { if (!load(table, 0)) { print "ERR\t" ERR; exit 1 } print "OK\t" VER[0] "\t" NR_[0]; exit 0 }
  if (mode == "route") {
    if (!load(table, 0)) { print "ERR\t" ERR; exit 1 }
    q[1] = ev; q[2] = cp; q[3] = dp; q[4] = sv; q[5] = dl
    print route(0, q) "\t" VER[0]; exit 0
  }
  if (mode == "compare") {   # 今の表(old。無ければ空の表)と新しい表(new)を、出てくる名前の全組み合わせで比べる
    if (old != "") { if (!load(old, 0)) { print "ERR\t今の表: " ERR; exit 1 } } else { NR_[0] = 0; VER[0] = 0; NCP[0] = 0 }
    if (!load(new, 1)) { print "ERR\t" ERR; exit 1 }
    total = 1
    for (i = 1; i <= 5; i++) { total *= domain(i); if (total > 500000) { print "BIG\t" VER[0] "\t" VER[1]; exit 0 } }
    looser = 0; changed = 0; stricter = 0
    for (a1 = 1; a1 <= DN[1]; a1++) for (a2 = 1; a2 <= DN[2]; a2++) for (a3 = 1; a3 <= DN[3]; a3++)
    for (a4 = 1; a4 <= DN[4]; a4++) for (a5 = 1; a5 <= DN[5]; a5++) {
      q[1] = D[1, a1]; q[2] = D[2, a2]; q[3] = D[3, a3]; q[4] = D[4, a4]; q[5] = D[5, a5]
      o = strip_line(route(0, q)); w = strip_line(route(1, q))
      if (o == w) continue
      if (o ~ /^unregistered/ && w ~ /^unregistered/) continue
      if (o ~ /^unregistered/) looser++
      else if (w ~ /^unregistered/) stricter++
      else changed++
    }
    print "CMP\t" VER[0] "\t" VER[1] "\t" looser "\t" changed "\t" stricter; exit 0
  }
  print "ERR\tmode"; exit 2
}
AWK
}

# 今効いている振り分け表を一時ファイルに書き出す。無ければ 1(CONV_RT_FILE は空)
conv_routes_file() {
  CONV_RT_FILE=""
  config_lookup conv-routes || return 1
  CONV_RT_FILE=$(mktemp); printf '%s\n' "$CFG_BODY" >"$CONV_RT_FILE"
}

# ---------- tb conv-route ----------
# 依頼は 5 つの区分の名前だけ。本文・自由記述は受け付けない(受け取った文面の中身で振り分けが変わらないように)
cmd_conv-route() {
  local ev="" cp="" dp="" sv="" dl="" json=0 stdin=0 j k out a st
  while [[ $# -gt 0 ]]; do
    case $1 in
      --event) need_arg "$@"; ev=$2; shift 2;;
      --counterpart) need_arg "$@"; cp=$2; shift 2;;
      --depth) need_arg "$@"; dp=$2; shift 2;;
      --severity) need_arg "$@"; sv=$2; shift 2;;
      --deadline) need_arg "$@"; dl=$2; shift 2;;
      --stdin) stdin=1; shift;;
      --json) json=1; shift;;
      *) usage "conv-route: 不明な引数 $1(依頼は --event --counterpart --depth --severity --deadline の 5 つの名前だけ。本文は渡さない)";;
    esac
  done
  if ((stdin)); then   # {"event":..,"counterpart":..,"depth":..,"severity":..,"deadline":..} だけ。ほかのキーがあれば断る
    j=$(cat)
    jq -e 'type == "object"' >/dev/null 2>&1 <<<"$j" || usage "conv-route --stdin: JSON のオブジェクトを渡す"
    k=$(jq -r 'keys_unsorted[] | select(. != "event" and . != "counterpart" and . != "depth" and . != "severity" and . != "deadline")' <<<"$j")
    [[ -z $k ]] || usage "conv-route --stdin: 使えないキーがあります: $(tr '\n' ' ' <<<"$k")(5 つの区分の名前だけ)"
    jq -e '[.[] | type == "string"] | all' >/dev/null <<<"$j" || usage "conv-route --stdin: 値は文字列だけ"
    ev=$(jq -r '.event // ""' <<<"$j"); cp=$(jq -r '.counterpart // ""' <<<"$j"); dp=$(jq -r '.depth // ""' <<<"$j")
    sv=$(jq -r '.severity // ""' <<<"$j"); dl=$(jq -r '.deadline // ""' <<<"$j")
  fi
  for a in "event:$ev" "counterpart:$cp" "depth:$dp" "severity:$sv" "deadline:$dl"; do
    [[ -n ${a#*:} ]] || usage "conv-route: --${a%%:*} が要ります"
    conv_need_name "--${a%%:*}" "${a#*:}"
  done
  if conv_routes_file; then
    st=0; out=$(awk -v mode=route -v table="$CONV_RT_FILE" -v ev="$ev" -v cp="$cp" -v dp="$dp" -v sv="$sv" -v dl="$dl" "$(_conv_routes_awk)" </dev/null) || st=$?
    rm -f "$CONV_RT_FILE"
    [[ $st -eq 0 ]] || die "振り分け表が読めません(conv-routes): ${out#ERR$'\t'}"
  else
    out=$'unregistered\tno-table\t0'
  fi
  local -a r; IFS=$'\t' read -ra r <<<"$out"
  if [[ ${r[0]} == routed ]]; then
    if ((json)); then
      jq -nc --arg k "${r[2]}" --arg c "${r[3]}" --arg ch "${r[4]}" --arg t "${r[5]}" --arg s "${r[6]}" --argjson v "${r[7]}" --argjson l "${r[1]}" \
        '{status:"routed", kind:$k, counterpart:$c, channel:$ch, timing:$t, severity:$s, needs_human:false, table_version:$v, rule_line:$l}'
    else
      printf 'routed\tkind=%s\tcounterpart=%s\tchannel=%s\ttiming=%s\tseverity=%s\tversion=%s\tline=%s\n' "${r[2]}" "${r[3]}" "${r[4]}" "${r[5]}" "${r[6]}" "${r[7]}" "${r[1]}"
    fi
  else
    if ((json)); then
      jq -nc --arg why "${r[1]}" --arg c "$cp" --arg s "$sv" --argjson v "${r[2]}" \
        '{status:"unregistered", kind:null, counterpart:$c, channel:null, timing:null, severity:$s, needs_human:true, reason:$why, table_version:$v}'
    else
      printf 'unregistered\treason=%s\tneeds_human=yes\tversion=%s\n' "${r[1]}" "${r[2]}"
    fi
  fi
}

# ---------- tb conv-routes-set(人だけ) ----------
# 新しい表を検査し、今の表と全組み合わせで比べる。未登録 → 振り分け(緩める)や、振り分け先の変更があれば --approve-looser が要る。
# 振り分け → 未登録(厳しくする)だけなら、そのまま書ける。version は今より大きくする
cmd_conv-routes-set() {
  local shared=0 approve=0 dry=0 new body cur="" out st v_old v_new lo ch stc how
  while [[ $# -gt 0 ]]; do
    case $1 in
      --shared) shared=1; shift;;
      --approve-looser) approve=1; shift;;
      --dry-run) dry=1; shift;;
      *) usage "conv-routes-set [--shared] [--approve-looser] [--dry-run] < 新しい表";;
    esac
  done
  [[ ! -t 0 ]] || usage "conv-routes-set: 新しい表は標準入力から渡す(例: tb conv-routes-set < conv-routes.conf)"
  ((dry)) || require_human "tb conv-routes-set"
  if ((shared)) && [[ -n $(config_env_file conv-routes) ]]; then die "TB_CONV_ROUTES でファイルを明示しているので --shared は使えません"; fi
  body=$(cat); new=$(mktemp); printf '%s\n' "$body" >"$new"
  if conv_routes_file; then cur=$CONV_RT_FILE; fi
  st=0; out=$(awk -v mode=compare -v old="$cur" -v new="$new" "$(_conv_routes_awk)" </dev/null) || st=$?
  rm -f "$new" ${cur:+"$cur"}
  [[ $st -eq 0 && $out != ERR* ]] || die "振り分け表を書けません: ${out#ERR$'\t'}"
  local -a r; IFS=$'\t' read -ra r <<<"$out"
  v_old=${r[1]} v_new=${r[2]}
  ((v_new > v_old)) || die "version を今の表($v_old)より大きくしてください(新しい表は $v_new)"
  if [[ ${r[0]} == BIG ]]; then
    echo "組み合わせが多すぎて比べられません。緩める変更として扱います"; lo=1 ch=0 stc=0
  else
    lo=${r[3]} ch=${r[4]} stc=${r[5]}
    printf 'version %s -> %s: 緩める(未登録 → 振り分け)=%s 振り分け先の変更=%s 厳しくする(振り分け → 未登録)=%s\n' "$v_old" "$v_new" "$lo" "$ch" "$stc"
  fi
  if ((lo + ch > 0)); then
    ((approve)) || { echo "tb: 振り分けを緩める・変える変更です。人が内容を確かめたうえで --approve-looser を付けて実行してください" >&2; return 20; }
    how=conv-routes-set:looser-approved
  else
    how=conv-routes-set:stricter
  fi
  if ((dry)); then echo "(dry-run: 何も書いていません)"; return 0; fi
  config_put conv-routes "$body" "$how" "$shared"
  echo "conv-routes: $(config_source conv-routes)($how)"
}

# ---------- tb conv-draft ----------
# 下書きの指示を組み立てて出す。入力は振り分けの結果(tb conv-route --json の出力)か種別の名前、一般化した状況、参照
cmd_conv-draft() {
  local kind="" route="" note="" ref="" max="" rj="" status="" needs=0 why="" team=0 i f
  local cp="-" ch="-" tm="-" sv="-"
  while [[ $# -gt 0 ]]; do
    case $1 in
      --kind) need_arg "$@"; kind=$2; shift 2;;
      --route) need_arg "$@"; route=$2; shift 2;;
      --note) need_arg "$@"; note=$2; shift 2;;
      --ref) need_arg "$@"; ref=$2; shift 2;;
      --max-chars) need_arg "$@"; max=$2; shift 2;;
      --request) need_arg "$@"; conv_need_file --request "$2"; max=$(conv_chars "$2"); shift 2;;
      *) usage "conv-draft (--route <tb conv-route --json の出力のファイル|-> | --kind <種別>) [--note 一般化した状況] [--ref 参照] [--max-chars N | --request <依頼のファイル>]";;
    esac
  done
  [[ -n $kind || -n $route ]] || usage "conv-draft: --route か --kind が要ります"
  [[ -z $kind || -z $route ]] || usage "conv-draft: --route と --kind は一緒に使えません"
  if [[ -n $note ]]; then
    (($(conv_strlen "$note") <= CONV_NOTE_MAX)) || usage "conv-draft: --note は $CONV_NOTE_MAX 文字まで"
    conv_generalized "--note" "$note" || usage "conv-draft: $CONV_ERR"
  fi
  [[ -z $ref || ( ${#ref} -le 200 && ! $ref =~ [[:space:]\"\'\<\>\`] ) ]] || usage "conv-draft: --ref は 200 文字までで、空白・引用符・<> を含まない"
  [[ -z $max || $max =~ ^[1-9][0-9]{0,5}$ ]] || usage "conv-draft: --max-chars は 1 以上の整数"
  if [[ -n $route ]]; then
    if [[ $route == - ]]; then rj=$(cat); else conv_need_file --route "$route"; rj=$(cat "$route"); fi
    jq -e 'type == "object" and (.status == "routed" or .status == "unregistered")' >/dev/null 2>&1 <<<"$rj" || usage "conv-draft --route: tb conv-route --json の出力を渡す"
    status=$(jq -r .status <<<"$rj")
    for f in kind counterpart channel timing severity; do
      i=$(jq -r --arg f "$f" '.[$f] // "" | strings' <<<"$rj")
      [[ -z $i ]] || conv_valid_name "$i" || usage "conv-draft --route: $f の値が不正です"
      case $f in kind) kind=$i;; counterpart) cp=${i:--};; channel) ch=${i:--};; timing) tm=${i:--};; severity) sv=${i:--};; esac
    done
    if [[ $status == unregistered ]]; then needs=1; why="振り分け表に無い依頼です($(jq -r '.reason // "-"' <<<"$rj"))"; kind=""; fi
  else
    conv_need_name --kind "$kind"
  fi
  if [[ -n $kind ]]; then
    if ! conv_teams_on; then why="チームの層は止めてあります(TB_CONV_TEAMS=off)"
    elif conv_has_team "$kind"; then team=1
    else why="種別 $kind のチームはありません"; fi
  fi
  [[ -f $CONV_DIR/_base.md ]] || die "共通の土台がありません: $CONV_DIR/_base.md"

  echo "# 下書きの指示(tb conv-draft)"
  if ((team)); then echo "チーム: $kind"
  else echo "チーム: なし。共通の土台だけで下書きします — ${why:-種別が決まっていません}"; fi
  ((needs)) && echo "要: 人の判断(振り分けが決まっていません。誰に・いつ・どこで送るかは人が決めます)"
  echo
  cat "$CONV_DIR/_base.md"
  if ((team)); then
    echo; echo "## チーム: $kind"; echo
    cat "$CONV_DIR/kinds/$kind/prompt.md"
    conv_patterns_read "$CONV_DIR/kinds/$kind/patterns.conf"
    local p n=0; local -a c
    for p in ${CONV_PAT[@]+"${CONV_PAT[@]}"}; do
      IFS='|' read -ra c <<<"$p"
      [[ ${c[1]} == adopted ]] || continue
      ((n++ == 0)) && { echo; echo "## 採用済みのパターン(人が採用したもの)"; echo; }
      printf -- '- %s: 場面=%s / 意図=%s / 終わり方=%s / 結論=%s / 避ける=%s\n' "${c[0]}" "${c[3]}" "${c[4]}" "${c[5]}" "$( [[ ${c[6]} == first ]] && echo 先頭 || echo 末尾 )" "${c[7]}"
    done
  fi
  echo; echo "## 今回の依頼"; echo
  echo "- 種別: ${kind:--}"
  echo "- 相手の区分: $cp / 経路の区分: $ch / 時機: $tm / 重大度: $sv"
  echo "- 状況(一般化したもの): ${note:--}"
  echo "- 参照: ${ref:--}(元の文面は参照先で読む。ここには写さない)"
  if [[ -n $max ]]; then echo "- 長さ: $max 文字以内(依頼より長くしない)"; else echo "- 長さ: 依頼より長くしない"; fi
}

# ---------- 声の役 ----------
conv_voice_file() {  # $1=役の名前 → ファイルのパス(無ければ 1)
  conv_valid_name "$1" && [[ -f $CONV_DIR/voices/$1.md && ! -L $CONV_DIR/voices/$1.md ]] && printf '%s' "$CONV_DIR/voices/$1.md"
}
conv_voices() { local f; for f in "$CONV_DIR"/voices/*.md; do [[ -f $f ]] && basename "$f" .md; done; }

# tb conv-voice <役> --draft F [--request F | --request-chars N]: 役の指示と下書きだけを出す(ほかの役の出力・依頼の本文は入れない)
cmd_conv-voice() {
  [[ $# -ge 1 ]] || usage "conv-voice <役> --draft <下書きのファイル> [--request <依頼のファイル> | --request-chars N]"
  local role=$1 draft="" rc="" vf dc; shift
  while [[ $# -gt 0 ]]; do
    case $1 in
      --draft) need_arg "$@"; draft=$2; shift 2;;
      --request) need_arg "$@"; conv_need_file --request "$2"; rc=$(conv_chars "$2"); shift 2;;
      --request-chars) need_arg "$@"; [[ $2 =~ ^[0-9]{1,6}$ ]] || usage "--request-chars は整数"; rc=$2; shift 2;;
      *) usage "conv-voice: 不明な引数 $1";;
    esac
  done
  vf=$(conv_voice_file "$role") || usage "conv-voice: 声の役がありません: $role(あるのは: $(conv_voices | tr '\n' ' '))"
  [[ -n $draft ]] || usage "conv-voice: --draft が要ります"
  conv_need_file --draft "$draft"
  [[ $role != brevity || -n $rc ]] || usage "conv-voice brevity: --request か --request-chars が要ります(依頼より長いかを必ず言うため)"
  dc=$(conv_chars "$draft")
  echo "# 声の役: $role(tb conv-voice)"; echo
  cat "$vf"
  echo; echo "## 出力の形"; echo
  echo "- 1 行 1 点、行頭は「- 」。多くても $CONV_VOICE_MAX 点。直す点が無ければ「- なし」"
  if [[ $role == brevity ]]; then
    if ((dc > rc)); then echo "- 1 行目に必ず「長さ: 依頼より長い」と書く(下書き $dc 文字 / 依頼 $rc 文字)"
    else echo "- 1 行目に必ず「長さ: 依頼より長くない」と書く(下書き $dc 文字 / 依頼 $rc 文字)"; fi
  fi
  echo "- 下書きを書き直さない。ほかの役の意見は見ていない前提で、自分の観点だけを書く"
  echo; echo "## 下書き"; echo
  cat "$draft"
}

# tb conv-voice-check <役> [--draft F --request F] < 役の出力: 形を確かめて整えたものを出す。0=正しい / 1=形が違う
cmd_conv-voice-check() {
  [[ $# -ge 1 ]] || usage "conv-voice-check <役> [--draft <下書き> --request <依頼>] < 役の出力"
  local role=$1 draft="" req="" line n=0 len="" exp="" out=() first=1; shift
  while [[ $# -gt 0 ]]; do
    case $1 in
      --draft) need_arg "$@"; conv_need_file --draft "$2"; draft=$2; shift 2;;
      --request) need_arg "$@"; conv_need_file --request "$2"; req=$2; shift 2;;
      *) usage "conv-voice-check: 不明な引数 $1";;
    esac
  done
  conv_voice_file "$role" >/dev/null || usage "conv-voice-check: 声の役がありません: $role"
  [[ ( -z $draft && -z $req ) || ( -n $draft && -n $req ) ]] || usage "conv-voice-check: --draft と --request は両方を渡す"
  while IFS= read -r line || [[ -n $line ]]; do
    line=$(trim "$line"); [[ -z $line ]] && continue
    if ((first)) && [[ $role == brevity && $line =~ ^長さ(:|：)[[:space:]]*(依頼より長い|依頼より長くない)$ ]]; then
      len=${BASH_REMATCH[2]}; first=0; continue
    fi
    first=0
    [[ $line == '- '* || $line == '・'* ]] || { echo "tb: 行頭が「- 」ではない行があります" >&2; return 1; }
    line=${line#- }; line=${line#・}; line=$(trim "$line")
    [[ -n $line ]] || continue
    n=$((n + 1)); out+=("- $line")
  done
  ((n >= 1)) || { echo "tb: 点がありません(直す点が無ければ「- なし」)" >&2; return 1; }
  ((n <= CONV_VOICE_MAX)) || { echo "tb: 点が多すぎます($n 点。多くても $CONV_VOICE_MAX 点)" >&2; return 1; }
  if [[ $role == brevity ]]; then
    [[ -n $len ]] || { echo "tb: brevity は 1 行目に「長さ: 依頼より長い」か「長さ: 依頼より長くない」を書きます" >&2; return 1; }
    if [[ -n $draft ]]; then
      if (($(conv_chars "$draft") > $(conv_chars "$req"))); then exp=依頼より長い; else exp=依頼より長くない; fi
      [[ $len == "$exp" ]] || { echo "tb: 長さの判定が実際と違います(実際は「${exp}」)" >&2; return 1; }
    fi
    echo "長さ: $len"
  fi
  printf '%s\n' "${out[@]}"
}

# tb conv-length --draft F --request F: 文字数を比べる。0=依頼以下 / 1=依頼より長い
cmd_conv-length() {
  local draft="" req="" d r
  while [[ $# -gt 0 ]]; do
    case $1 in
      --draft) need_arg "$@"; draft=$2; shift 2;;
      --request) need_arg "$@"; req=$2; shift 2;;
      *) usage "conv-length --draft <下書き> --request <依頼>";;
    esac
  done
  [[ -n $draft && -n $req ]] || usage "conv-length --draft <下書き> --request <依頼>"
  conv_need_file --draft "$draft"; conv_need_file --request "$req"
  d=$(conv_chars "$draft"); r=$(conv_chars "$req")
  if ((d > r)); then printf 'draft=%s\trequest=%s\tlonger=yes\n' "$d" "$r"; return 1; fi
  printf 'draft=%s\trequest=%s\tlonger=no\n' "$d" "$r"
}

# ---------- 一覧と検査 ----------
conv_kinds() { local d; for d in "$CONV_DIR"/kinds/*/; do [[ -d $d && ! -L ${d%/} ]] && basename "$d"; done | while IFS= read -r d; do conv_valid_name "$d" && echo "$d"; done; }

# tb conv-kinds [--json]: 種別 <TAB> team=yes|no <TAB> adopted=N <TAB> candidate=N <TAB> errors=N
cmd_conv-kinds() {
  local json=0 k t a c p rows=()
  [[ ${1-} == --json ]] && { json=1; shift; }
  [[ $# -eq 0 ]] || usage "conv-kinds [--json]"
  while IFS= read -r k; do
    [[ -n $k ]] || continue
    conv_has_team "$k" && t=yes || t=no
    conv_patterns_read "$CONV_DIR/kinds/$k/patterns.conf"
    a=0 c=0
    for p in ${CONV_PAT[@]+"${CONV_PAT[@]}"}; do [[ $p == *"|adopted|"* ]] && a=$((a + 1)) || c=$((c + 1)); done
    rows+=("$k"$'\t'"team=$t"$'\t'"adopted=$a"$'\t'"candidate=$c"$'\t'"errors=${#CONV_PAT_ERRS[@]}")
  done < <(conv_kinds)
  if ((json)); then
    printf '%s\n' ${rows[@]+"${rows[@]}"} | jq -Rnc '[inputs | select(length > 0) | split("\t") | {kind: .[0], team: (.[1] == "team=yes"),
      adopted: (.[2] | ltrimstr("adopted=") | tonumber), candidate: (.[3] | ltrimstr("candidate=") | tonumber), errors: (.[4] | ltrimstr("errors=") | tonumber)}]'
  else
    ((${#rows[@]} == 0)) || printf '%s\n' "${rows[@]}"
  fi
}

# tb conv-patterns <種別> [--all]: 採用済み(--all なら候補も)のパターン。誤りのある行は標準エラーに出す
cmd_conv-patterns() {
  [[ $# -ge 1 ]] || usage "conv-patterns <種別> [--all]"
  local kind=$1 all=0 p e; shift
  [[ ${1-} == --all ]] && { all=1; shift; }
  [[ $# -eq 0 ]] || usage "conv-patterns <種別> [--all]"
  conv_need_name 種別 "$kind"
  [[ -d $CONV_DIR/kinds/$kind ]] || die "種別がありません: $kind"
  conv_patterns_read "$CONV_DIR/kinds/$kind/patterns.conf"
  for e in ${CONV_PAT_ERRS[@]+"${CONV_PAT_ERRS[@]}"}; do echo "tb: patterns.conf $e" >&2; done
  for p in ${CONV_PAT[@]+"${CONV_PAT[@]}"}; do
    ((all)) || [[ $p == *"|adopted|"* ]] || continue
    printf '%s\n' "${p//|/$'\t'}"
  done
}

# tb conv-lint: 共通の土台・声の役・各種別のファイルと、振り分け表を確かめる。0=問題なし / 1=問題あり
cmd_conv-lint() {
  [[ $# -eq 0 ]] || usage "conv-lint"
  local bad=0 k e v out st
  [[ -f $CONV_DIR/_base.md ]] || { echo "_base.md: ありません"; bad=1; }
  for v in direct empathetic receiver brevity; do conv_voice_file "$v" >/dev/null || { echo "voices/$v.md: ありません"; bad=1; }; done
  while IFS= read -r k; do
    [[ -n $k ]] || continue
    conv_has_team "$k" || { echo "kinds/$k: prompt.md がありません(この種別は共通の土台だけで下書きされます)"; bad=1; }
    conv_patterns_read "$CONV_DIR/kinds/$k/patterns.conf"
    for e in ${CONV_PAT_ERRS[@]+"${CONV_PAT_ERRS[@]}"}; do echo "kinds/$k/patterns.conf $e"; bad=1; done
  done < <(conv_kinds)
  if conv_routes_file; then
    st=0; out=$(awk -v mode=check -v table="$CONV_RT_FILE" "$(_conv_routes_awk)" </dev/null) || st=$?
    rm -f "$CONV_RT_FILE"
    if [[ $st -ne 0 ]]; then echo "conv-routes: ${out#ERR$'\t'}"; bad=1
    else
      IFS=$'\t' read -r _ v k <<<"$out"; echo "conv-routes: version ${v}、$k 行($(config_source conv-routes))"
      # 振り分け先の種別にチームがあるか(無くても動くが、共通の土台だけになる)
      while IFS= read -r e; do
        conv_has_team "$e" || echo "conv-routes: 種別 $e のチームはありません(共通の土台だけで下書きされます)"
      done < <(config_text conv-routes | sed -n 's/.*->.*kind=\([a-z][a-z0-9-]*\).*/\1/p' | sort -u)
    fi
  else
    echo "conv-routes: ありません(すべての依頼が未登録になり、人の判断に回ります)"
  fi
  ((bad == 0)) && echo "ok"
  return $bad
}

# tb conv <サブコマンド>(新しい名前)。古い名前 tb conv-<サブコマンド> も同じに動く(別名。bash を外すときに外す)
cmd_conv() {
  if [[ $# -eq 0 ]]; then conv_usage; exit 2; fi
  local sub=$1; shift
  case $sub in
    candidate-add|candidate-adopt|candidate-reject|draft|gate|incident|kinds|learn-purge|ledger|length|lint|pair-add|pair-show|pairs|patterns|route|routes-set|scan|stage|stage-log|stage-reset|stage-set|voice|voice-check) "cmd_conv-$sub" "$@";;
    help|-h|--help) conv_usage;;
    *) usage "conv: 不明なサブコマンド $sub(tb conv で一覧)";;
  esac
}
conv_usage() {
  cat <<'USAGE'
tb conv route|routes-set|draft|voice|voice-check|length|kinds|patterns|lint ...   会話チームの振り分けと下書き(引数は tb help)
tb conv pair-add|pairs|pair-show|candidate-add|ledger|candidate-adopt|candidate-reject|scan|learn-purge ...   会話の学習
tb conv stage|stage-set|stage-reset|stage-log|incident|gate ...   会話チームの段階
(古い名前 tb conv-<サブコマンド> も同じに使える)
USAGE
}
