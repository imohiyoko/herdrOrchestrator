-- 025: レシピ(ADR 0005 の段 2)。部品(lib/recipes.sh にコードで固定)の並びに名前を付けて登録する。登録・変更・削除は人だけ(tb recipe-set / recipe-rm)。
-- body は登録したときの本文そのまま(desc / risk / arg / step の行)。実行(tb recipe-run)のたびに読み直して検査する(コードの部品が変わっても古い検査を信じない)。
-- risk は登録のときにコードが計算した値(中の部品の最大。宣言で下げられない)。一覧に出す参考で、判定には使わない(実行のたびに計算し直す)。
CREATE TABLE IF NOT EXISTS recipes (
  herdr_session TEXT NOT NULL,
  name          TEXT NOT NULL,        -- 英小文字で始まる英小文字・数字・-(: は使えない。操作名 recipe:<名前>:<対象> を最後の : で分けるため)
  body          TEXT NOT NULL,
  risk          TEXT NOT NULL CHECK (risk IN ('low','mid','high','human-only')),
  steps         INTEGER NOT NULL,
  updated_by    TEXT NOT NULL,        -- 人だけが書くので human
  updated_at    TEXT NOT NULL,
  PRIMARY KEY (herdr_session, name)
);
-- 変更の履歴。追記だけ。消したときは new_body が NULL
CREATE TABLE IF NOT EXISTS recipe_changes (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  herdr_session TEXT NOT NULL,
  name          TEXT NOT NULL,
  how           TEXT NOT NULL CHECK (how IN ('add','update','rm')),
  old_body      TEXT,
  new_body      TEXT,
  risk          TEXT,
  changed_by    TEXT NOT NULL,
  changed_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS recipe_changes_name ON recipe_changes(herdr_session, name, id);
