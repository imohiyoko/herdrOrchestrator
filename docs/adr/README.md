# ADR(設計判断の記録)

| 番号 | タイトル | 状態 |
|---|---|---|
| [0001](0001-enforce-agent-limits-by-environment.md) | agent の操作の制限は、プロンプトではなく環境(能力の有無)で強制する | Proposed |
| [0002](0002-services-and-binary-distribution.md) | 部品をサービスの境界で分け、OS ごとのバイナリで配布する | Accepted |
| [0003](0003-ticket-events-and-notify-rules.md) | 通知・期限・アラート・提案をチケットのイベントと規則に寄せる | Accepted |
| [0004](0004-teams-and-ticket-handoff.md) | 機能を層で分け、仕事は「チーム」にチケットで頼む | Proposed |
| [0005](0005-minimal-parts-recipes-and-registered-grants.md) | tb の部品を最小にし、組み合わせ(レシピ)と権限(操作の種類)を DB に登録できるようにする | Accepted |
| [0006](0006-common-permission-tiers-for-all-commands.md) | tb の全コマンドに共通の権限段階を割り当て、設定で変えられるようにする | Accepted |
| [0007](0007-authenticated-principals-and-one-policy.md) | 認証した主体に認可を与える 1 つのモデルにし、過去の権限の仕組みをなくす | Accepted |
| [0008](0008-application-api-under-tb-and-webui.md) | tb と Web UI の下にアプリケーション API の層を置き、入口を薄くする | Proposed |
| [0009](0009-specific-subject-rows-override-defaults.md) | 既定の表を一番下の層にし、細かい主体の行が広い行と既定を上書きする | Accepted |
