# shellcheck shell=bash
# 設定(config 表。schema/013)。bin/tb から source される。単体では実行しない。
#
# 設定はキーで引く。本文は conf ファイルの中身そのまま(各機能の読み方は変えない)。引く順:
#   1. 環境変数で明示したファイル(TB_APPROVALS など。テストや一時の差し替え。無ければその .example)
#   2. DB のこのセッションの行
#   3. DB の全セッション共通の行(herdr_session='*')
#   4. checkout のファイル(taskboard/config/<名前>。tb config import で DB に取り込むまでの移行用)
#   5. 例のファイル(<名前>.example。既定値として読む設定だけ。コードと一緒に版を合わせる既定値)
# agent が別のセッション向けや古い本文をファイルから直接読まないよう、取り込んだファイルは checkout から退かす(tb config import)。
# agent は設定を tb config show <キー> で読む(自分のセッションの行だけが返る)
# 書くのは人だけ。環境変数でファイルを明示しているときはそのファイルを、そうでなければ DB のこのセッションの行(--shared なら共通の行)を書く。

CONFIG_DIR=${TB_CONFIG_DIR:-$TB_ROOT/config}   # checkout の設定ファイルの置き場(テストで差し替え)
CONFIG_KEYS="projects approvals grants routes delivery limits notify notify-rules compact slash sched-watch spawn-policy role-models conv-routes"

# キーの情報: CFG_ENV(上書きの環境変数)、CFG_FILE(checkout のファイル名)、CFG_EXAMPLE(1 なら .example も既定値として読む)
config_key_info() {
  CFG_EXAMPLE=0
  case $1 in
    projects)     CFG_ENV=TB_CONFIG;          CFG_FILE=projects.conf; CFG_EXAMPLE=1;;
    approvals)    CFG_ENV=TB_APPROVALS;       CFG_FILE=approvals.conf; CFG_EXAMPLE=1;;
    grants)       CFG_ENV=TB_GRANTS;          CFG_FILE=grants.conf;;
    routes)       CFG_ENV=TB_ROUTES;          CFG_FILE=routes.conf;;
    delivery)     CFG_ENV=TB_DELIVERY;        CFG_FILE=delivery.conf;;
    notify)       CFG_ENV=TB_NOTIFY_CONF;     CFG_FILE=notify.conf;;
    notify-rules) CFG_ENV=TB_NOTIFY_RULES;    CFG_FILE=notify-rules.conf;;   # チケットのイベントの通知の規則(lib/notifyrules.sh)。.example は既定値にしない(無ければ規則なし)
    limits)       CFG_ENV=TB_LIMITS;          CFG_FILE=limits.conf; CFG_EXAMPLE=1;;
    compact)      CFG_ENV=TB_COMPACT_CONF;    CFG_FILE=compact.conf; CFG_EXAMPLE=1;;
    slash)        CFG_ENV=TB_SLASH_CONF;      CFG_FILE=slash.conf; CFG_EXAMPLE=1;;
    sched-watch)  CFG_ENV=TB_SCHEDWATCH_CONF; CFG_FILE=sched-watch.conf; CFG_EXAMPLE=1;;
    spawn-policy) CFG_ENV=TB_SPAWN_POLICY;    CFG_FILE=spawn-policy.conf; CFG_EXAMPLE=1;;
    role-models)  CFG_ENV=TB_ROLE_MODELS;     CFG_FILE=role-models.conf; CFG_EXAMPLE=1;;
    conv-routes)  CFG_ENV=TB_CONV_ROUTES;     CFG_FILE=conv-routes.conf;;   # 会話の振り分け表(lib/conv.sh)。.example は既定値にしない(無ければすべて未登録)
    *) return 1;;
  esac
}
config_key_ok() { config_key_info "$1" || usage "不明な設定のキー: $1(使えるのは: $CONFIG_KEYS)"; }
config_env_file() { config_key_info "$1" && printf '%s' "${!CFG_ENV:-}"; }   # 環境変数で明示したファイル(無ければ空)
config_checkout_file() { config_key_info "$1" && printf '%s' "$CONFIG_DIR/$CFG_FILE"; }

# DB の行を読む。DB や表が無くても失敗にしない(空)。書き込みで開かない(読むだけで空の DB を作らない)
config_db_row() {  # $1=キー → "<s|*>:<本文>"(このセッションの行なら s)
  [[ -f $DB ]] || return 0
  sqlite3 -readonly -batch -cmd ".timeout ${SQL_TIMEOUT:-5000}" "$DB" \
    "SELECT CASE herdr_session WHEN '*' THEN '*' ELSE 's' END || ':' || body FROM config
       WHERE key=$(q "$1") AND herdr_session IN ($SQS, '*') ORDER BY herdr_session='*' LIMIT 1;" 2>/dev/null || true
}

# config_lookup <キー>: 見つかれば 0 で CFG_BODY(本文)と CFG_SRC(出どころ)を入れる。どこにも無ければ 1(CFG_SRC は none)
# 1 プロセスの中では引いた結果を覚えておく(書いたら config_forget)
config_lookup() {
  local key=$1 cv="CFGC_${1//-/_}" sv="CFGS_${1//-/_}" f r
  if [[ -n ${!sv:-} ]]; then CFG_BODY=${!cv}; CFG_SRC=${!sv}; [[ $CFG_SRC != none ]]; return; fi
  config_key_info "$key" || return 2
  CFG_BODY=""; CFG_SRC=none
  f=${!CFG_ENV:-}
  if [[ -n $f ]]; then
    if [[ -f $f ]]; then CFG_BODY=$(cat "$f"); CFG_SRC="file:$f"
    elif ((CFG_EXAMPLE)) && [[ -f $f.example ]]; then CFG_BODY=$(cat "$f.example"); CFG_SRC="example:$f.example"; fi
  else
    r=$(config_db_row "$key")
    if [[ -n $r ]]; then
      CFG_BODY=${r#?:}; if [[ ${r:0:1} == '*' ]]; then CFG_SRC="db:*"; else CFG_SRC="db:$SESS"; fi
    else
      f=$CONFIG_DIR/$CFG_FILE
      if [[ -f $f ]]; then CFG_BODY=$(cat "$f"); CFG_SRC="file:$f"
      elif ((CFG_EXAMPLE)) && [[ -f $f.example ]]; then CFG_BODY=$(cat "$f.example"); CFG_SRC="example:$f.example"; fi
    fi
  fi
  printf -v "$cv" '%s' "$CFG_BODY"; printf -v "$sv" '%s' "$CFG_SRC"
  [[ $CFG_SRC != none ]]
}
config_forget() { unset "CFGC_${1//-/_}" "CFGS_${1//-/_}"; }

# 本文を出す(1 行ずつ読む側のため、空でなければ末尾に改行を付ける)。無ければ 1
config_text() { config_lookup "$1" || return 1; [[ -z $CFG_BODY ]] || printf '%s\n' "$CFG_BODY"; }
config_has() { config_lookup "$1"; }
config_source() { config_lookup "$1" >/dev/null || true; printf '%s' "$CFG_SRC"; }

# 書く(人だけが呼ぶ)。$1=キー $2=本文 $3=how(履歴に残す) [$4=共通の行に書くなら 1]
# キーごとの検査 config_validate_<キー>(- は _)があれば、書く前に通す(だめなら何も書かない)
config_put() {
  local key=$1 body=$2 how=$3 shared=${4:-0} f tmp hs
  config_key_info "$key" || die "不明な設定のキー: $key"
  if declare -F "config_validate_${key//-/_}" >/dev/null; then
    "config_validate_${key//-/_}" "$body" || die "$key の本文に問題があるため書きませんでした"
  fi
  f=${!CFG_ENV:-}
  if [[ -n $f ]]; then   # ファイルを明示しているときはそのファイルを書く(DB には書かない)
    [[ ! -L $f ]] || die "$(basename "$f") がシンボリックリンクです: $f"
    mkdir -p "$(dirname "$f")"; tmp=$(mktemp "$f.XXXXXX")
    if [[ -f $f ]]; then cp -p "$f" "$tmp"; else chmod 644 "$tmp"; fi   # 今のファイルの権限を保つ(notify.conf など)
    if [[ -n $body ]]; then printf '%s\n' "$body" >"$tmp"; else : >"$tmp"; fi
    mv "$tmp" "$f"
  else
    config_db_put "$key" "$body" "$how" "$shared"
  fi
  config_forget "$key"
}

# DB の行を書く(キーの検査は呼び出し側)。$1=キー $2=本文 $3=how [$4=共通の行なら 1] [$5=書いた人。既定 human]
config_db_put() {
  local hs=$SQS by; by=$(q "${5:-human}"); ((${4:-0})) && hs="'*'"
  sql "BEGIN IMMEDIATE;
    INSERT INTO main.config (herdr_session, key, body, updated_by, updated_at) VALUES ($hs, $(q "$1"), $(q "$2"), $by, $(q "$(now)"))
      ON CONFLICT (herdr_session, key) DO UPDATE SET body=excluded.body, updated_by=excluded.updated_by, updated_at=excluded.updated_at;
    INSERT INTO main.config_changes (herdr_session, key, body, changed_by, how, changed_at) VALUES ($hs, $(q "$1"), $(q "$2"), $by, $(q "$3"), $(q "$(now)"));
    COMMIT;" >/dev/null
}

config_unset() {  # $1=キー [$2=共通の行なら 1]。DB の行だけを消す
  local key=$1 hs=$SQS; ((${2:-0})) && hs="'*'"
  sql "BEGIN IMMEDIATE; DELETE FROM main.config WHERE herdr_session=$hs AND key=$(q "$key");
    INSERT INTO main.config_changes (herdr_session, key, body, changed_by, how, changed_at) SELECT $hs, $(q "$key"), NULL, 'human', 'unset', $(q "$(now)") WHERE changes() > 0;
    COMMIT;" >/dev/null
  config_forget "$key"
}

# ---------- tb config ----------
cmd_config() {
  local sub=${1:-list}; shift || true
  case $sub in
    list) config_list "$@";;
    show) config_show "$@";;
    set|edit|unset) config_change "$sub" "$@";;
    import) config_import "$@";;
    history) config_history "$@";;
    *) usage "config [list | show <キー> | set <キー> [--shared] < 本文 | edit <キー> [--shared] | unset <キー> [--shared] | import [--dry-run] | history <キー>]";;
  esac
}

config_list() {
  [[ $# -eq 0 ]] || usage "config list"
  local k
  printf 'セッション: %s\n' "$SESS"
  for k in $CONFIG_KEYS; do printf '%-13s %s\n' "$k" "$(config_source "$k")"; done
}

config_show() {
  [[ $# -eq 1 ]] || usage "config show <キー>"
  config_key_ok "$1"
  config_lookup "$1" || die "$1 の設定はありません(DB にもファイルにも無い。tb config set $1 で作る)"
  echo "# $1: $CFG_SRC" >&2
  [[ -z $CFG_BODY ]] || printf '%s\n' "$CFG_BODY"
}

config_change() {
  local sub=$1 key="" shared=0 body tmp; shift
  while [[ $# -gt 0 ]]; do
    case $1 in
      --shared) shared=1; shift;;
      -*) usage "config $sub: 不明な引数 $1";;
      *) [[ -z $key ]] || usage "config $sub <キー> [--shared]"; key=$1; shift;;
    esac
  done
  [[ -n $key ]] || usage "config $sub <キー> [--shared]"
  config_key_ok "$key"
  require_human "tb config $sub"
  # 会話の振り分け表は、緩める変更の確かめと version の検査を通す tb conv-routes-set だけで書く(消すのは unset でよい)
  [[ $key != conv-routes || $sub == unset ]] || die "conv-routes は tb conv-routes-set < 新しい表 で書きます(緩める変更は --approve-looser が要ります)"
  if ((shared)) && [[ -n $(config_env_file "$key") ]]; then die "$CFG_ENV でファイルを明示しているので --shared は使えません"; fi
  case $sub in
    unset)
      [[ -z $(config_env_file "$key") ]] || die "$CFG_ENV でファイルを明示しています(DB の行は使っていません)"
      config_unset "$key" "$shared"; echo "$key: $(config_source "$key")";;
    set)
      [[ ! -t 0 ]] || usage "config set <キー>: 本文は標準入力から渡す(例: tb config set $key < 本文のファイル)"
      body=$(cat); config_put "$key" "$body" set "$shared"; echo "$key: $(config_source "$key")";;
    edit)
      tmp=$(mktemp); config_text "$key" >"$tmp" || true
      "${VISUAL:-${EDITOR:-vi}}" "$tmp" || { rm -f "$tmp"; die "編集を中止しました"; }
      body=$(cat "$tmp"); rm -f "$tmp"
      config_put "$key" "$body" edit "$shared"; echo "$key: $(config_source "$key")";;
  esac
}

# checkout のファイルを、共通の行(*)として取り込む。既に共通の行があるキーは飛ばす。
# 取り込んだファイルは checkout から退かす(agent が古い本文を直接読まないように): $HERDR_STATE_DIR/config-imported/<日時>/
# roles/sessions/<セッション>/ の役割もそのセッションの行として取り込む(共有の roles/*.md は既定値なのでファイルのまま)
config_import() {
  local dry=0 k f n=0 dest=""
  while [[ $# -gt 0 ]]; do case $1 in --dry-run) dry=1; shift;; *) usage "config import [--dry-run]";; esac; done
  require_human "tb config import"
  for k in $CONFIG_KEYS; do
    f=$(config_checkout_file "$k")
    [[ -f $f && ! -L $f ]] || continue
    if [[ -n $(sql "SELECT 1 FROM main.config WHERE herdr_session='*' AND key=$(q "$k");") ]]; then echo "$k: 共通の行が既にあるので飛ばします($f はそのまま)"; continue; fi
    n=$((n + 1))
    if ((dry)); then echo "$k: $f を共通の行として取り込みます"; continue; fi
    if [[ -z $dest ]]; then
      dest=$HERDR_STATE_DIR/config-imported/$(date +%Y%m%d-%H%M%S)
      mkdir -p "$dest" || die "退かす先を作れません: $dest"
    fi
    sql "BEGIN IMMEDIATE;
      INSERT INTO main.config (herdr_session, key, body, updated_by, updated_at) VALUES ('*', $(q "$k"), $(q "$(cat "$f")"), 'human', $(q "$(now)"));
      INSERT INTO main.config_changes (herdr_session, key, body, changed_by, how, changed_at) VALUES ('*', $(q "$k"), $(q "$(cat "$f")"), 'human', 'import', $(q "$(now)"));
      COMMIT;" >/dev/null
    mv "$f" "$dest/" || die "$f を退かせません(DB には取り込み済み)"
    echo "$k: $f を共通の行として取り込み、$dest/ へ退かしました"
  done
  # セッションごとの役割(roles/sessions/<セッション>/*.md と map.conf)は、そのセッションの行として取り込み、ディレクトリごと退かす
  local d sname rf key
  for d in "$ROLES_BASE"/sessions/*/; do
    [[ -d $d && ! -L ${d%/} ]] || continue
    sname=$(basename "$d")
    [[ $sname =~ ^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$ ]] || { echo "roles/sessions/$sname: セッション名に使えない文字があるので飛ばします"; continue; }
    n=$((n + 1))
    for rf in "$d"*.md "$d"map.conf; do
      [[ -f $rf && ! -L $rf ]] || continue
      if [[ $(basename "$rf") == map.conf ]]; then key=role-map; else key="role:$(basename "$rf" .md)"; fi
      [[ $key == role:README ]] && continue
      role_key_of "${key#role:}" >/dev/null 2>&1 || [[ $key == role-map ]] || { echo "$rf: 役割名に使えない文字があるので飛ばします"; continue; }
      if [[ -n $(sql "SELECT 1 FROM main.config WHERE herdr_session=$(q "$sname") AND key=$(q "$key");") ]]; then echo "$key(セッション $sname): 行が既にあるので飛ばします"; continue; fi
      if ((dry)); then echo "$key: $rf をセッション $sname の行として取り込みます"; continue; fi
      # 例外: ほかのセッションの行を書く(人が実行する取り込み。見張りなしで開く)
      sql_all "BEGIN IMMEDIATE;
        INSERT INTO main.config (herdr_session, key, body, updated_by, updated_at) VALUES ($(q "$sname"), $(q "$key"), $(q "$(cat "$rf")"), 'human', $(q "$(now)"));
        INSERT INTO main.config_changes (herdr_session, key, body, changed_by, how, changed_at) VALUES ($(q "$sname"), $(q "$key"), $(q "$(cat "$rf")"), 'human', 'import', $(q "$(now)"));
        COMMIT;" >/dev/null
      echo "$key: $rf をセッション $sname の行として取り込みました"
    done
    ((dry)) && continue
    if [[ -z $dest ]]; then dest=$HERDR_STATE_DIR/config-imported/$(date +%Y%m%d-%H%M%S); mkdir -p "$dest" || die "退かす先を作れません: $dest"; fi
    mkdir -p "$dest/roles-sessions" && mv "${d%/}" "$dest/roles-sessions/" || die "$d を退かせません(DB には取り込み済み)"
    echo "roles/sessions/$sname/ を $dest/roles-sessions/ へ退かしました"
  done
  ((dry)) && echo "(dry-run: 何も書いていません)"
  ((n > 0)) || echo "取り込むファイルはありません"
  return 0
}

config_history() {
  [[ $# -eq 1 ]] || usage "config history <キー>"
  config_key_ok "$1"
  sql -separator $'\t' "SELECT id, herdr_session, how, changed_by, changed_at, CASE WHEN body IS NULL THEN '(消した)' ELSE length(body) || ' 文字' END
    FROM main.config_changes WHERE key=$(q "$1") AND herdr_session IN ($SQS, '*') ORDER BY id DESC LIMIT 50;"
}
