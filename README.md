# heardrOrchestrator

Building blocks for running several Claude agents on herdr: one **conductor** agent, a **task board**, a mostly read-only **web UI** (it can also stop / revive / create agents), and a **scheduler** for periodic prompts.

```
conductor  <--- reports ---  workers (herdr panes)
    |                              |
    +---- task board (sqlite) -----+
              |             |
           web UI       scheduler (launchd)
```

## Layout

| Path | What it is |
|---|---|
| [`taskboard/`](taskboard/README.md) | `tb` CLI: SQLite task table, worker reports batched to the conductor every 10 min (`tb digest`), conductor → worker sends with sender check and routing rules (`tb send`), a log of every message tb sends, hook-based activity tracking, agent lifecycle (`tb agent-new` / `agent-stop` / `agent-revive`, with a "safe to stop" flag set by a light-model sweeper agent) (bash, sqlite3, jq) |
| [`webui/`](webui/README.md) | Progress web UI with login (Python standard library only). Mostly read-only; it can also change job schedules, mark reports resolved, and stop / revive / create agents (`/agents`, `/agent-new`) |
| [`scheduler/`](scheduler/README.md) | Periodic prompts / commands via launchd, driven by `jobs/*.conf` |
| [`presets/`](presets/example.conf) | Example workspace presets for `herdr-up.sh` |
| [`herdr-up.sh`](herdr-up.sh) | Creates workspaces and starts agents from a preset |
| [`conductor/`](conductor/README.md) | Description of the conductor role |
| [`roles/`](roles/README.md) | Role files handed to each agent on startup / after compact (SessionStart hook), maintained by the HR agent |
| [`lib/`](lib/herdr-env.sh) | Defaults shared by every component (repo root, DB path, herdr binary, schema dir), in bash and Python |
| [`setup/`](setup/README.md) | Setup agent (`./herdr-up.sh setup`) that walks you through the setup and asks before writing any settings, plus a read-only doctor `setup/check.sh` |
| [`repoindex/`](repoindex/README.md) | Optional (opt-in) module: a local index of per-repository summaries with its own SQLite database (outside the checkout, `0700`/`0600`) and a read-only MCP tool interface. The core never calls it |

## Usage

Each component has its own README and test script:

```
bash tests/run-all.sh          # 全部。webui の SQL を本物のスキーマで確かめる tests/test_webui_queries.py も含む
bash tests/run-all.sh -j 4     # 全部を 4 本ずつ並列に
bash tests/run-all.sh --list   # 組の一覧(GitHub Actions は組ごとに別のジョブで並列に流す)
bash tests/run-all.sh webui    # 組を選んで流す
bash taskboard/test/run.sh     # --shard <組> で taskboard/test/shards の組だけ
bash webui/tests/run.sh
bash scheduler/tests/run.sh
bash setup/tests/run.sh
bash repoindex/tests/run.sh
```

Tests use fake `herdr` binaries and do not touch a real herdr, launchd, or the network. Runtime state (`data/`, `run/`, `logs/`, `generated/`) is git-ignored.

Copy `scheduler/jobs/*.conf.example` to `*.conf` and adjust before use, or start the setup agent with `./herdr-up.sh setup` and let it guide you. `bash setup/check.sh` shows what is installed and what is missing.

## License

Copyright 2026 imohiyoko

Licensed under the [Apache License, Version 2.0](LICENSE).
