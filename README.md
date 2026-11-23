# herdrOrchestrator

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
| [`taskboard/`](taskboard/README.md) | `tb` CLI: SQLite task table, worker reports batched to the conductor every 10 min (`tb digest`), conductor → worker sends with sender check and routing rules (`tb message send`), a log of every message tb sends, hook-based activity tracking, agent lifecycle (`tb agent-new` / `agent-stop` / `agent-revive`, with a "safe to stop" flag set by a light-model sweeper agent) (bash, sqlite3, jq) |
| [`webui/`](webui/README.md) | Progress web UI with login (Python standard library only). Mostly read-only; it can also change job schedules, mark reports resolved, and stop / revive / create agents (`/agents`, `/agent-new`) |
| [`scheduler/`](scheduler/README.md) | Periodic prompts / commands via launchd, driven by job definitions in the DB (`tb job`) |
| [`presets/`](presets/example.conf) | Example workspace presets for `herdr-up.sh` |
| [`herdr-up.sh`](herdr-up.sh) | Creates workspaces and starts agents from a preset |
| [`conductor/`](conductor/README.md) | Description of the conductor role |
| [`roles/`](roles/README.md) | Text handed to each agent on startup / after compact (SessionStart hook). By default only the rules shared by every agent (`_common.md`); no roles are assumed |
| [`setup/examples/`](setup/examples/README.md) | Usage example of a setup with roles (conductor, HR, watcher, sweeper, auditor ...): role texts, name → role map and a preset. Installed with `tb role-write` (see `setup/CLAUDE.md`) |
| [`cmd/herdrctl/`](cmd/herdrctl/main.go) | 1 つのバイナリ `herdrctl`(仮称)。tb の中身(`taskboard/bin/tb` は `herdrctl tb` を呼ぶだけ)で、残りの bash と Python の部品も順に移していく先([ADR 0002](docs/adr/0002-services-and-binary-distribution.md))。今は `version`、`db status` / `db migrate`(tasks.db を tb と同じスキーマにする。Windows の既定の置き場所は `%LOCALAPPDATA%\herdr\state\tasks.db`)、`tb` のコマンド(`bin/tb` が渡す。herdrctl は必須で、`TB_HERDRCTL` > PATH の `herdrctl`。移した順と残りは [docs/go-migration.md](docs/go-migration.md)) (Go) |
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
bash taskboard/test/run.sh     # ケースを並列に(-j N で本数、ケース名を渡すとそれだけ)
bash webui/tests/run.sh
bash scheduler/tests/run.sh
bash setup/tests/run.sh
bash repoindex/tests/run.sh
```

Tests use fake `herdr` binaries and do not touch a real herdr, launchd, or the network. Runtime state (`data/`, `run/`, `logs/`, `generated/`) is git-ignored.

Put job definitions into the DB with `taskboard/bin/tb job set <job>` (start from `scheduler/jobs/*.conf.example`; existing `*.conf` files are imported with `tb job import` — the scheduler no longer reads them), or start the setup agent with `./herdr-up.sh setup` and let it guide you. `bash setup/check.sh` shows what is installed and what is missing.

## Release

`herdrctl` は macOS・Windows・Linux(各 amd64 / arm64)向けにビルドして GitHub Releases に置く。`v*` のタグを push すると `.github/workflows/release.yml` が GoReleaser で作り、`checksums.txt` とビルドの来歴の証明を付ける。

```
go test ./...                          # 手元のテスト
go run ./cmd/herdrctl version
git tag v0.1.0 && git push origin v0.1.0   # リリース(タグは人が打つ)
```

バイナリはまだ署名していない。macOS では初回に「開発元を確認できない」と出るので、Finder で右クリック →「開く」か `xattr -d com.apple.quarantine ./herdrctl`。Windows では SmartScreen が出るので「詳細情報」→「実行」。

## License

Copyright 2026 imohiyoko

Licensed under the [Apache License, Version 2.0](LICENSE).
