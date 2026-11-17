-- agent の関係エッジ(主体 -動詞→ 対象)。tb agents --graph と Web UI の /agent-graph が同じこの SQL を読む。
-- role は人事役が tb role-set でロールを変えた記録(人の操作は辺にしない)。
-- 列: verb, src, dst, project, status, n(件数), last_at
-- src / dst が NULL の行は project の指揮官(config/projects.conf、無ければ conductor)で埋める(lead は src、report は dst)。
-- status は記録時点の判定(send の denied = routes.conf で拒否された試み)。期間は直近 7 日(lead と spawn は今の状態)
SELECT 'spawn' AS verb, spawned_by AS src, name AS dst, NULL AS project, 'ok' AS status, 1 AS n, created_at AS last_at
  FROM agents
  WHERE spawned_by IS NOT NULL AND spawned_by != ''
    AND (state = 'running' OR stopped_at > strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-7 days'))
UNION ALL
SELECT 'lead', NULL, owner_agent, project, 'ok', COUNT(*), MAX(updated_at)
  FROM tasks
  WHERE owner_agent IS NOT NULL AND owner_agent != '' AND status IN ('in_progress', 'review', 'blocked')
  GROUP BY owner_agent, project
UNION ALL
SELECT 'send:' || kind, from_name, target, NULL, CASE WHEN status = 'denied' THEN 'denied' ELSE 'ok' END, COUNT(*), MAX(created_at)
  FROM outbox
  WHERE status IN ('sent', 'queued', 'pending', 'denied') AND created_at > strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-7 days')
  GROUP BY from_name, target, kind, status = 'denied'
UNION ALL
SELECT 'report', c.from_name, NULL, t.project, 'ok', COUNT(*), MAX(c.created_at)
  FROM comments c JOIN tasks t ON t.id = c.task_id
  WHERE c.created_at > strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-7 days')
  GROUP BY c.from_name, t.project
UNION ALL
SELECT 'analyze', source, analyst, NULL, 'ok', COUNT(*), MAX(COALESCE(analysis_started_at, last_seen))
  FROM alerts
  WHERE analyst IS NOT NULL AND analyst != '' AND last_seen > strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-7 days')
  GROUP BY source, analyst
UNION ALL
SELECT 'role', changed_by, agent, NULL, 'ok', COUNT(*), MAX(created_at)
  FROM role_changes
  WHERE changed_by != 'human' AND created_at > strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-7 days')
  GROUP BY changed_by, agent
