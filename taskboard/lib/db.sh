# shellcheck shell=bash
# DB 層: 接続・SQL リテラル化・スキーマの移行。tb から source される。
# スキーマは $HERDR_SCHEMA_DIR/NNN_*.sql(テストでは TB_SCHEMA_DIR で差し替え)。DB の PRAGMA user_version より新しい番号だけを順に流す。
# 新しい変更は既存ファイルを書き換えず、次の番号のファイルを足す。

# sql: このセッションとして開く(下の SESSION_SETUP)。sql_all: 見張りなしで開く(移行と、全セッションを書く例外だけ)
sql() {
  [[ -n ${SESSION_SETUP-} ]] || session_setup_load
  sqlite3 -bail -batch -cmd ".timeout ${SQL_TIMEOUT:-5000}" -cmd "$SESSION_SETUP" "$DB" "$@"
}
sql_all() { sqlite3 -bail -batch -cmd ".timeout ${SQL_TIMEOUT:-5000}" "$DB" "$@"; }
q()   { local s=${1//\'/\'\'}; printf "'%s'" "$s"; }          # SQL 文字列リテラル化

# 1 つの DB を複数の herdr セッションで共有し、行は herdr_session 列で分ける(012)。
# tb は今のセッション(SESS)の行だけを読み書きする。読みは表の名前のまま書けば下のビューがこのセッションに絞る。
# 書きは main.<表> に、INSERT は herdr_session に SQS(リテラル化済み)を、UPDATE / DELETE は WHERE herdr_session=$SQS を書く(見張りが別セッションの行を止める)。
SESS=$(herdr_session_name)
SQS=$(q "$SESS")

# sql() は接続ごとに、herdr_session 列を持つ表(taskboard/sql/session_tables)について次を作る(sqlite3 の -cmd。接続を閉じれば消える):
# - 表と同じ名前の一時ビュー(このセッションの行だけ)。表の名前のまま読めば、このセッションの行だけを見る。全セッションを見る読みは main.<表> と書く
# - main.<表> への書き込みの見張り(一時トリガー)。別のセッションの行を足す・変える・消すと ABORT で止まる(絞り忘れが黙って他人の行を変えない)
# 書き込みは main.<表> と書く(ビューには書けない: SQLite が「cannot modify … because it is a view」で止める)。
# ビュー経由で書かない(INSTEAD OF トリガーにしない)のは、changes() / last_insert_rowid() / RETURNING がビュー経由では効かないため
SESSION_TABLES_FILE=$TB_ROOT/sql/session_tables
session_tables() { grep -v '^#' "$SESSION_TABLES_FILE" | grep -E '^[a-z_]+$'; }
# 一覧のうち、この DB に herdr_session 列つきで今ある表(壊れた・古い DB でも、ある表だけで動く)。
# tb の main が init_db の後に 1 回だけ引く(sql() の中で引くと $(...) のたびに引き直すため)。hook の touch は sql_all だけなので引かない
session_tables_present() {
  local want; want=$(session_tables | sed "s/.*/'&'/" | paste -sd, -)
  [[ -n $want ]] || return 0
  sql_all "SELECT m.name FROM sqlite_master m WHERE m.type='table' AND m.name IN ($want)
    AND EXISTS (SELECT 1 FROM pragma_table_info(m.name) p WHERE p.name='herdr_session') ORDER BY m.name;" 2>/dev/null || true
}
session_setup() {
  local t ok m="tb: 別の herdr セッションの行は書き換えられません"
  for t in $(session_tables_present); do
    # 設定(config / config_changes)だけは、全セッション共通の行('*')も書ける(tb config set --shared など)
    case $t in config|config_changes) ok="IN ($SQS, '*')";; *) ok="IS $SQS";; esac
    printf "CREATE TEMP VIEW %s AS SELECT * FROM main.%s WHERE herdr_session=%s;" "$t" "$t" "$SQS"
    printf "CREATE TEMP TRIGGER _tb_guard_%s_i BEFORE INSERT ON main.%s WHEN NOT (NEW.herdr_session %s) BEGIN SELECT RAISE(ABORT,'%s(%s)'); END;" "$t" "$t" "$ok" "$m" "$t"
    printf "CREATE TEMP TRIGGER _tb_guard_%s_u BEFORE UPDATE ON main.%s WHEN NOT (OLD.herdr_session %s AND NEW.herdr_session %s) BEGIN SELECT RAISE(ABORT,'%s(%s)'); END;" "$t" "$t" "$ok" "$ok" "$m" "$t"
    printf "CREATE TEMP TRIGGER _tb_guard_%s_d BEFORE DELETE ON main.%s WHEN NOT (OLD.herdr_session %s) BEGIN SELECT RAISE(ABORT,'%s(%s)'); END;" "$t" "$t" "$ok" "$m" "$t"
  done
}
SESSION_SETUP=
session_setup_load() { SESSION_SETUP=$(session_setup); SESSION_SETUP=${SESSION_SETUP:-SELECT 1 WHERE 0;}; }
# このセッションの 1 行(JSON。無ければ空)。$1=表 $2=列 $3=値(リテラル化する)。表の名前はこのセッションのビュー
session_row() { sql -json "SELECT * FROM $1 WHERE $2=$(q "$3");" | jq -c '.[0] // empty'; }

schema_files() { local f; for f in "${TB_SCHEMA_DIR:-$HERDR_SCHEMA_DIR}"/[0-9][0-9][0-9]_*.sql; do [[ -f $f ]] && printf '%s\n' "$f"; done; }
schema_latest() { local f n=0; f=$(schema_files | tail -n 1); [[ -n $f ]] && n=$((10#$(basename "$f" | cut -c1-3))); echo "$n"; }

# 移行 1 本分の SQL。ロックを取った直後に版を確かめ、別の tb が先に済ませていたら CHECK 違反で何も流さずに止まる
migration_sql() {  # $1=番号 $2=ファイル
  echo "BEGIN IMMEDIATE;"
  echo "CREATE TEMP TABLE _tb_migrate_guard (v INTEGER CHECK (v < $1)); INSERT INTO _tb_migrate_guard SELECT user_version FROM pragma_user_version;"
  # @SESSION@ = 移行を流すセッション(012: それまでの 1 セッション = 1 DB の行を、そのセッションのものとして埋める)
  local body; body=$(cat "$2")
  ( shopt -u patsub_replacement 2>/dev/null || true; printf '%s\n' "${body//@SESSION@/$SQS}" )
  echo "PRAGMA user_version=$1; COMMIT;"
}

# 最新なら PRAGMA 1 回で戻る(hook から毎回呼ばれるので軽くしておく)
init_db() {
  local v latest f n err
  # スキーマが無ければ DB に触る前に止める(sqlite3 は開いただけで空の DB ファイルを作る)
  latest=$(schema_latest)
  ((latest > 0)) || { echo "tb: スキーマのファイルが見つかりません: ${TB_SCHEMA_DIR:-$HERDR_SCHEMA_DIR}" >&2; return 1; }
  mkdir -p "$(dirname "$DB")"
  v=$(sql_all "PRAGMA user_version;") || return 1
  ((v >= latest)) && return 0
  while IFS= read -r f; do
    n=$((10#$(basename "$f" | cut -c1-3)))
    ((n > v)) || continue
    # 失敗しても、別の tb が先に済ませて版が進んでいれば良しとする
    if ! err=$(migration_sql "$n" "$f" | sql_all 2>&1 >/dev/null); then
      v=$(sql_all "PRAGMA user_version;") || return 1
      ((v >= n)) || { echo "tb: 移行 $(basename "$f") に失敗: $err" >&2; return 1; }
    fi
    v=$n
  done < <(schema_files)
}
