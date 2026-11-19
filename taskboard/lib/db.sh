# shellcheck shell=bash
# DB 層: 接続・SQL リテラル化・スキーマの移行。tb から source される。
# スキーマは $HERDR_SCHEMA_DIR/NNN_*.sql(テストでは TB_SCHEMA_DIR で差し替え)。DB の PRAGMA user_version より新しい番号だけを順に流す。
# 新しい変更は既存ファイルを書き換えず、次の番号のファイルを足す。

# sql: このセッションとして開く(下の SESSION_SETUP)。sql_all: 見張りなしで開く(移行と、全セッションを書く例外だけ)
sql() {
  [[ -n ${SESSION_SETUP-} ]] || session_setup_load
  sqlite3 -bail -batch -cmd ".timeout ${SQL_TIMEOUT:-5000}" -cmd "$SESSION_SETUP" -cmd "$EVENT_SETUP" "$DB" "$@"
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
  HAS_TASK_EVENTS=0
  for t in $(session_tables_present); do
    [[ $t != task_events ]] || HAS_TASK_EVENTS=1
    # 設定(config / config_changes)だけは、全セッション共通の行('*')も書ける(tb config set --shared など)
    case $t in config|config_changes) ok="IN ($SQS, '*')";; *) ok="IS $SQS";; esac
    # task_events だけはビューを作らない: 履歴を書く一時トリガー(event_setup)は表を main. 付きで書けず、同じ名前のビューがあるとそちらに当たるため。
    # 読みは herdr_session=$SQS を明示して絞る(tasks.sh の events_out)。見張りは他の表と同じく作る
    [[ $t == task_events ]] || printf "CREATE TEMP VIEW %s AS SELECT * FROM main.%s WHERE herdr_session=%s;" "$t" "$t" "$SQS"
    printf "CREATE TEMP TRIGGER _tb_guard_%s_i BEFORE INSERT ON main.%s WHEN NOT (NEW.herdr_session %s) BEGIN SELECT RAISE(ABORT,'%s(%s)'); END;" "$t" "$t" "$ok" "$m" "$t"
    printf "CREATE TEMP TRIGGER _tb_guard_%s_u BEFORE UPDATE ON main.%s WHEN NOT (OLD.herdr_session %s AND NEW.herdr_session %s) BEGIN SELECT RAISE(ABORT,'%s(%s)'); END;" "$t" "$t" "$ok" "$ok" "$m" "$t"
    printf "CREATE TEMP TRIGGER _tb_guard_%s_d BEFORE DELETE ON main.%s WHEN NOT (OLD.herdr_session %s) BEGIN SELECT RAISE(ABORT,'%s(%s)'); END;" "$t" "$t" "$ok" "$m" "$t"
  done
}
SESSION_SETUP=
HAS_TASK_EVENTS=0
session_setup_load() {
  # $(...) の中では HAS_TASK_EVENTS が親に残らないので、一時ファイルを使わず 2 回に分けない: 出力の最後の行で受け取る
  local out; out=$(session_setup; printf '\n%s' "$HAS_TASK_EVENTS")
  HAS_TASK_EVENTS=${out##*$'\n'}; SESSION_SETUP=${out%$'\n'*}
  SESSION_SETUP=${SESSION_SETUP:-SELECT 1 WHERE 0;}
  event_setup_load
}

# ---------- チケットの履歴(018 の task_events。ADR 0003 の段 1) ----------
# tb の接続ごとに一時トリガーを作り、tasks / comments / task_links への書き込みと同じトランザクションで task_events に 1 件ずつ残す。
# 一時トリガーにするのは、変えた者(EVENT_ACTOR)を接続ごとに埋め込むため(main のトリガーは一時の表を読めない)。
# トリガーの中では表に main. を付けられない(SQLite の制約)ので、task_events には同じ名前の一時ビューを作らない(session_setup)。
# tb 以外(移行・db-import・手で開いた sqlite3)の書き込みは履歴に残らない。
# 変えた者: agent の pane なら herdr から引いた agent 名(event_actor_resolve。引けなければ pane:<id>)、人の端末なら human
EVENT_ACTOR=
EVENT_SETUP="SELECT 1 WHERE 0;"
event_actor_default() { if [[ -n ${HERDR_PANE_ID:-} ]]; then printf 'pane:%s' "$HERDR_PANE_ID"; else printf human; fi; }
event_setup_load() {
  [[ -n $EVENT_ACTOR ]] || EVENT_ACTOR=$(event_actor_default)
  if [[ ${HAS_TASK_EVENTS:-0} == 1 ]]; then EVENT_SETUP=$(event_setup "$EVENT_ACTOR"); else EVENT_SETUP="SELECT 1 WHERE 0;"; fi
}
# 書き込む前に呼ぶ(tasks.sh)。pane からなら agent 名を引く。失敗しても書き込みは止めない(pane:<id> のまま)
event_actor_resolve() {
  if [[ -n ${HERDR_PANE_ID:-} ]] && resolve_label >/dev/null 2>&1 && [[ -n ${LABEL_NAME:-} ]]; then EVENT_ACTOR=$LABEL_NAME; fi
  event_setup_load
}
event_setup() {  # $1=変えた者
  local a ts="strftime('%Y-%m-%dT%H:%M:%SZ','now')" ins
  a=$(q "$1")
  ins="INSERT INTO task_events (herdr_session, task_id, kind, old, new, actor, ref, created_at)"
  # 1 列の変化 → 1 件。$1=種類 $2=列
  col() { printf "%s SELECT NEW.herdr_session, NEW.id, '%s', OLD.%s, NEW.%s, %s, NULL, %s WHERE OLD.%s IS NOT NEW.%s;" "$ins" "$1" "$2" "$2" "$a" "$ts" "$2" "$2"; }
  printf 'CREATE TEMP TRIGGER _tb_ev_tasks_i AFTER INSERT ON main.tasks BEGIN '
  printf "%s SELECT NEW.herdr_session, NEW.id, 'created', NULL, NEW.status, %s, NULL, %s;" "$ins" "$a" "$ts"
  printf "%s SELECT NEW.herdr_session, NEW.id, 'label_added', NULL, value, %s, NULL, %s FROM json_each(NEW.labels);" "$ins" "$a" "$ts"
  printf "%s SELECT NEW.herdr_session, NEW.id, 'ref_added', NULL, json_extract(value,'\$.url'), %s, NULL, %s FROM json_each(NEW.refs);" "$ins" "$a" "$ts"
  printf 'END;'
  printf 'CREATE TEMP TRIGGER _tb_ev_tasks_u AFTER UPDATE ON main.tasks BEGIN '
  col status status; col title title; col type type; col priority priority; col owner owner_agent; col due due; col parent parent_id; col points points
  # スプリント(019)は S-<n> で残す(NULL = バックログ)
  printf "%s SELECT NEW.herdr_session, NEW.id, 'sprint', 'S-'||OLD.sprint_id, 'S-'||NEW.sprint_id, %s, NULL, %s WHERE OLD.sprint_id IS NOT NEW.sprint_id;" "$ins" "$a" "$ts"
  # 説明は本文を履歴に写さない(変わったことだけ)
  printf "%s SELECT NEW.herdr_session, NEW.id, 'description', NULL, NULL, %s, NULL, %s WHERE OLD.description IS NOT NEW.description;" "$ins" "$a" "$ts"
  printf "%s SELECT NEW.herdr_session, NEW.id, 'label_added', NULL, value, %s, NULL, %s FROM json_each(NEW.labels) WHERE value NOT IN (SELECT value FROM json_each(OLD.labels));" "$ins" "$a" "$ts"
  printf "%s SELECT NEW.herdr_session, NEW.id, 'label_removed', value, NULL, %s, NULL, %s FROM json_each(OLD.labels) WHERE value NOT IN (SELECT value FROM json_each(NEW.labels));" "$ins" "$a" "$ts"
  printf "%s SELECT NEW.herdr_session, NEW.id, 'ref_added', NULL, json_extract(value,'\$.url'), %s, NULL, %s FROM json_each(NEW.refs)
            WHERE json_extract(value,'\$.url') NOT IN (SELECT json_extract(value,'\$.url') FROM json_each(OLD.refs));" "$ins" "$a" "$ts"
  printf "%s SELECT NEW.herdr_session, NEW.id, 'ref_removed', json_extract(value,'\$.url'), NULL, %s, NULL, %s FROM json_each(OLD.refs)
            WHERE json_extract(value,'\$.url') NOT IN (SELECT json_extract(value,'\$.url') FROM json_each(NEW.refs));" "$ins" "$a" "$ts"
  printf 'END;'
  # コメントは報告者(herdr から引いた名前)を変えた者にする。本文は写さない(種類と C-<n> だけ)
  printf "CREATE TEMP TRIGGER _tb_ev_comments_i AFTER INSERT ON main.comments BEGIN %s SELECT NEW.herdr_session, NEW.task_id, 'comment', NULL, NEW.kind, NEW.from_name, 'C-'||NEW.id, %s; END;" "$ins" "$ts"
  # リンクは両方のタスクに、それぞれから見た向きで残す
  local inv="CASE X.link_type WHEN 'blocks' THEN 'blocked_by' WHEN 'duplicates' THEN 'duplicated_by' ELSE X.link_type END"
  printf "CREATE TEMP TRIGGER _tb_ev_links_i AFTER INSERT ON main.task_links BEGIN
            %s SELECT NEW.herdr_session, NEW.from_id, 'link', NULL, NEW.link_type||' '||NEW.to_id, NEW.created_by, NULL, %s;
            %s SELECT NEW.herdr_session, NEW.to_id, 'link', NULL, %s||' '||NEW.from_id, NEW.created_by, NULL, %s; END;" \
    "$ins" "$ts" "$ins" "${inv//X./NEW.}" "$ts"
  printf "CREATE TEMP TRIGGER _tb_ev_links_d AFTER DELETE ON main.task_links BEGIN
            %s SELECT OLD.herdr_session, OLD.from_id, 'unlink', OLD.link_type||' '||OLD.to_id, NULL, %s, NULL, %s;
            %s SELECT OLD.herdr_session, OLD.to_id, 'unlink', %s||' '||OLD.from_id, NULL, %s, NULL, %s; END;" \
    "$ins" "$a" "$ts" "$ins" "${inv//X./OLD.}" "$a" "$ts"
  unset -f col
}
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

# 最新なら SELECT 1 回で戻る(hook から毎回呼ばれるので軽くしておく)
init_db() {
  local v jm latest f n err
  # スキーマが無ければ DB に触る前に止める(sqlite3 は開いただけで空の DB ファイルを作る)
  latest=$(schema_latest)
  ((latest > 0)) || { echo "tb: スキーマのファイルが見つかりません: ${TB_SCHEMA_DIR:-$HERDR_SCHEMA_DIR}" >&2; return 1; }
  mkdir -p "$(dirname "$DB")"
  IFS='|' read -r v jm < <(sql_all "SELECT (SELECT user_version FROM pragma_user_version) || '|' || (SELECT journal_mode FROM pragma_journal_mode);") || return 1
  [[ $v =~ ^[0-9]+$ ]] || return 1
  # (版と journal_mode は 1 列にまとめて読む: ~/.sqliterc の区切り文字に左右されない)
  # WAL にする(DB ファイルに残るので 1 回だけ)。全セッションが同じ DB を使うので、読みと書きが互いを待たないようにする。
  # 切り替えには一瞬の排他ロックが要る。取れなければ今回は諦め、次の tb で切り替える(失敗しても動作は変わらない)
  [[ $jm == wal ]] || sql_all "PRAGMA journal_mode=WAL;" >/dev/null 2>&1 || true
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
