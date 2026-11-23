# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- チケットの読み取り(show / tree / labels / sprints / board / history / events)の使い方・文面・終了コード(期待値は消した bash 版の出力から作った。今はこれが約束)。project は trc / trc2 だけを使う
trerr() { "$@" 2>&1; echo "rc=$?"; }
trmask() { sed -E -e 's/[0-9]{4}-[0-9]{2}-[0-9]{2}[T ][0-9:]{8}Z?/TS/g' -e 's/\b([ES])-[0-9]+\b/\1-N/g' -e 's/"sprint_id": ?[0-9]+/"sprint_id":N/g'; }

# ---- 使い方と引数の誤り
eq "show: no id" "$(trerr $TB show)" $'tb: show <id>\n(tb help で使い方)\nrc=2'
eq "show: bad id" "$(trerr $TB show x)" $'tb: タスク ID は T-<project>-NNN 形式: x\n(tb help で使い方)\nrc=2'
eq "show: missing" "$(trerr $TB show T-trc-999)" $'tb: タスクが見つかりません: T-trc-999\nrc=1'
eq "tree: no arg" "$(trerr $TB tree)" $'tb: tree <id> | tree -p <project>\n(tb help で使い方)\nrc=2'
eq "tree: bad project" "$(trerr $TB tree -p 'a b')" $'rc=0'
eq "labels: unknown" "$(trerr $TB labels --bogus)" $'tb: labels: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "sprints: bad state" "$(trerr $TB sprints --state x)" $'tb: state は planned / active / closed のどれか: x\n(tb help で使い方)\nrc=2'
eq "sprints: unknown" "$(trerr $TB sprints --bogus)" $'tb: sprints: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "board: no arg" "$(trerr $TB board)" $'tb: board S-<n> | board -p <project>\n(tb help で使い方)\nrc=2'
eq "board: bad sprint" "$(trerr $TB board S-x)" $'tb: スプリント ID は S-<番号> 形式: S-x\n(tb help で使い方)\nrc=2'
eq "board: missing sprint" "$(trerr $TB board S-999999)" $'tb: スプリントが見つかりません: S-999999\nrc=1'
eq "board: no active" "$(trerr $TB board -p trc)" $'tb: project trc に active なスプリントがありません(tb sprints -p trc)\nrc=1'
eq "history: no id" "$(trerr $TB history)" $'tb: history <id> [--json]\n(tb help で使い方)\nrc=2'
eq "history: two ids" "$(trerr $TB history T-trc-001 T-trc-002)" $'tb: history <id> [--json]\n(tb help で使い方)\nrc=2'
eq "history: unknown" "$(trerr $TB history --bogus)" $'tb: history: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "history: missing" "$(trerr $TB history T-trc-999)" $'tb: タスクが見つかりません: T-trc-999\nrc=1'
eq "events: bad since" "$(trerr $TB events --since 5)" $'tb: events: --since は E-<数字>: 5\n(tb help で使い方)\nrc=2'
eq "events: bad kind" "$(trerr $TB events --kind X)" $'tb: events: --kind が不正です: X\n(tb help で使い方)\nrc=2'
eq "events: bad limit" "$(trerr $TB events --limit 0)" $'tb: events: --limit は 1〜999999: 0\n(tb help で使い方)\nrc=2'
eq "events: limit needs value" "$(trerr $TB events --limit)" $'tb: --limit に値が必要です\n(tb help で使い方)\nrc=2'
eq "events: unknown" "$(trerr $TB events --bogus)" $'tb: events: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'

# ---- 中身
trs1=$($TB sprint-add -p trc -n "Sprint 1" --start 2026-11-17 --end 2026-11-30 --goal "ゴール")
trs2=$($TB sprint-add -p trc -n "Sprint 2")
a=$($TB add -p trc -t "親	A" --sprint "$trs1" --points 3 -l ui -l api)
b=$($TB add -p trc -t B --parent "$a" --sprint "$trs1" --points 5 --priority high -l ui)
c=$($TB add -p trc -t C --parent "$b" --desc $'説明\n2 行' --due 2026-12-01 --ref gh=https://x.test/1)
d=$($TB add -p trc2 -t D -l other)
$TB link "$a" blocks "$c" >/dev/null; $TB link "$b" relates "$c" >/dev/null
$TB claim "$b" --owner w1 >/dev/null 2>&1; $TB sprint-start "$trs1" >/dev/null
eq "show" "$(trerr $TB show "$c" | trmask)" $'{\n  "id": "T-trc-003",\n  "project": "trc",\n  "title": "C",\n  "owner_agent": null,\n  "session_id": null,\n  "account": null,\n  "status": "todo",\n  "parent_id": "T-trc-002",\n  "deliverables": [],\n  "summary": null,\n  "note": null,\n  "reported_at": null,\n  "created_at": "TS",\n  "updated_at": "TS",\n  "herdr_session": "default",\n  "type": "task",\n  "priority": "medium",\n  "labels": [],\n  "description": "説明\\n2 行",\n  "refs": [\n    {\n      "kind": "gh",\n      "url": "https://x.test/1"\n    }\n  ],\n  "due": "2026-12-01",\n  "sprint_id": null,\n  "points": null,\n  "activity": null,\n  "links": {\n    "blocked_by": [\n      {\n        "id": "T-trc-001",\n        "title": "親\\tA",\n        "status": "todo"\n      }\n    ],\n    "relates": [\n      {\n        "id": "T-trc-002",\n        "title": "B",\n        "status": "in_progress"\n      }\n    ]\n  }\n}\nrc=0'
eq "show: parent with links" "$(trerr $TB show "$a" | trmask)" $'{\n  "id": "T-trc-001",\n  "project": "trc",\n  "title": "親\\tA",\n  "owner_agent": null,\n  "session_id": null,\n  "account": null,\n  "status": "todo",\n  "parent_id": null,\n  "deliverables": [],\n  "summary": null,\n  "note": null,\n  "reported_at": null,\n  "created_at": "TS",\n  "updated_at": "TS",\n  "herdr_session": "default",\n  "type": "task",\n  "priority": "medium",\n  "labels": [\n    "api",\n    "ui"\n  ],\n  "description": null,\n  "refs": [],\n  "due": null,\n  "sprint_id":N,\n  "points": 3,\n  "activity": null,\n  "links": {\n    "blocks": [\n      {\n        "id": "T-trc-003",\n        "title": "C",\n        "status": "todo"\n      }\n    ]\n  }\n}\nrc=0'
eq "tree id" "$(trerr $TB tree "$a")" $'T-trc-001\ttask\tmedium\ttodo\t-\t親\tA\n  T-trc-002\ttask\thigh\tin_progress\tw1\tB\n    T-trc-003\ttask\tmedium\ttodo\t-\tC\nrc=0'
eq "tree project" "$(trerr $TB tree -p trc)" $'T-trc-001\ttask\tmedium\ttodo\t-\t親\tA\n  T-trc-002\ttask\thigh\tin_progress\tw1\tB\n    T-trc-003\ttask\tmedium\ttodo\t-\tC\nrc=0'
eq "labels" "$(trerr $TB labels -p trc)" $'api\t1\t1\nui\t2\t2\nrc=0'
eq "labels --json" "$(trerr $TB labels -p trc --json)" $'{"label":"api","open":1,"total":1}\n{"label":"ui","open":2,"total":2}\nrc=0'
eq "sprints" "$(trerr $TB sprints -p trc | trmask)" $'S-N\tactive\ttrc\tSprint 1\t2026-11-17..2026-11-30\t0/2\t0/8\nS-N\tplanned\ttrc\tSprint 2\t-..-\t0/0\t0/0\nrc=0'
eq "sprints --json" "$(trerr $TB sprints -p trc --json | trmask)" $'{"id":"S-N","state":"active","project":"trc","name":"Sprint 1","start_date":"2026-11-17","end_date":"2026-11-30","goal":"ゴール","tasks":2,"done":0,"points":8,"done_points":0}\n{"id":"S-N","state":"planned","project":"trc","name":"Sprint 2","start_date":null,"end_date":null,"goal":null,"tasks":0,"done":0,"points":0,"done_points":0}\nrc=0'
eq "sprints --state active" "$(trerr $TB sprints -p trc --state active | trmask)" $'S-N\tactive\ttrc\tSprint 1\t2026-11-17..2026-11-30\t0/2\t0/8\nrc=0'
eq "board sprint" "$(trerr $TB board "$trs1" | trmask)" $'S-N Sprint 1 [active] 2026-11-17..2026-11-30\n目標: ゴール\n\n== todo ==\nT-trc-001\tmedium\t-\t3\t親\tA\n\n== in_progress ==\nT-trc-002\thigh\tw1\t5\tB\n\n== review ==\n\n== blocked ==\n\n== done ==\n\n== cancelled ==\nrc=0'
eq "board project" "$(trerr $TB board -p trc -l ui | trmask)" $'S-N Sprint 1 [active] 2026-11-17..2026-11-30\n目標: ゴール\n\n== todo ==\nT-trc-001\tmedium\t-\t3\t親\tA\n\n== in_progress ==\nT-trc-002\thigh\tw1\t5\tB\n\n== review ==\n\n== blocked ==\n\n== done ==\n\n== cancelled ==\nrc=0'
eq "board --json" "$(trerr $TB board -p trc --json | trmask)" $'{"sprint":{"id":"S-N","project":"trc","name":"Sprint 1","goal":"ゴール","start_date":"2026-11-17","end_date":"2026-11-30","state":"active","created_at":"TS","started_at":"TS","closed_at":null},"columns":{"todo":[{"id":"T-trc-001","status":"todo","type":"task","priority":"medium","owner":"-","points":3,"labels":["api","ui"],"title":"親\\tA"}],"in_progress":[{"id":"T-trc-002","status":"in_progress","type":"task","priority":"high","owner":"w1","points":5,"labels":["ui"],"title":"B"}],"review":[],"blocked":[],"done":[],"cancelled":[]}}\nrc=0'
eq "board empty sprint" "$(trerr $TB board "$trs2" | trmask)" $'S-N Sprint 2 [planned] -..-\n\n== todo ==\n\n== in_progress ==\n\n== review ==\n\n== blocked ==\n\n== done ==\n\n== cancelled ==\nrc=0'
eq "history" "$(trerr $TB history "$c" | trmask)" $'E-N\tTS\tT-trc-003\tcreated\t-\ttodo\thuman\t-\nE-N\tTS\tT-trc-003\tref_added\t-\thttps://x.test/1\thuman\t-\nE-N\tTS\tT-trc-003\tlink\t-\tblocked_by T-trc-001\thuman\t-\nE-N\tTS\tT-trc-003\tlink\t-\trelates T-trc-002\thuman\t-\nrc=0'
eq "history --json" "$(trerr $TB history "$b" --json | trmask)" $'{"id":"E-N","task_id":"T-trc-002","kind":"created","old":null,"new":"todo","actor":"human","ref":null,"created_at":"TS"}\n{"id":"E-N","task_id":"T-trc-002","kind":"label_added","old":null,"new":"ui","actor":"human","ref":null,"created_at":"TS"}\n{"id":"E-N","task_id":"T-trc-002","kind":"link","old":null,"new":"relates T-trc-003","actor":"human","ref":null,"created_at":"TS"}\n{"id":"E-N","task_id":"T-trc-002","kind":"status","old":"todo","new":"in_progress","actor":"human","ref":null,"created_at":"TS"}\n{"id":"E-N","task_id":"T-trc-002","kind":"owner","old":null,"new":"w1","actor":"human","ref":null,"created_at":"TS"}\nrc=0'
eq "events project" "$(trerr $TB events -p trc2 | trmask)" $'E-N\tTS\tT-trc2-001\tcreated\t-\ttodo\thuman\t-\nE-N\tTS\tT-trc2-001\tlabel_added\t-\tother\thuman\t-\nrc=0'
eq "events kind limit" "$(trerr $TB events -p trc --kind link --limit 1 --json | trmask)" $'{"id":"E-N","task_id":"T-trc-003","kind":"link","old":null,"new":"relates T-trc-002","actor":"human","ref":null,"created_at":"TS"}\nrc=0'
unset -f trerr trmask; unset trs1 trs2 a b c d
