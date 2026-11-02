-- name: ListMemories :many
SELECT id, user_id, kind, content, source_task_id, created_at, updated_at FROM memories
WHERE user_id = $1 AND (sqlc.narg(kind)::text IS NULL OR kind = sqlc.narg(kind))
ORDER BY updated_at DESC
LIMIT $2 OFFSET $3;

-- name: CreateMemory :one
INSERT INTO memories (user_id, kind, content, embedding, source_task_id)
VALUES ($1, $2, $3, $4, $5)
RETURNING id;

-- name: DeleteMemory :execrows
DELETE FROM memories WHERE id = $1 AND user_id = $2;

-- name: DeleteAllMemories :exec
DELETE FROM memories WHERE user_id = $1;

-- name: ListInterestMemories :many
SELECT content FROM memories WHERE user_id = $1 AND kind = 'interest' ORDER BY updated_at DESC LIMIT $2;

-- name: SearchMemories :many
SELECT id, kind, content FROM memories
WHERE user_id = $1 AND embedding IS NOT NULL
ORDER BY embedding <=> $2
LIMIT $3;

-- name: RecordModelUsage :exec
INSERT INTO model_usage (user_id, task_id, provider, model, tier, input_tokens, output_tokens, cached_tokens, cost_micros, billed_tokens)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: SumBilledTokensSince :one
-- Tokens counted against the quota: calls of failed tasks and of console re-runs are not charged.
SELECT coalesce(sum(m.billed_tokens), 0)::bigint FROM model_usage m LEFT JOIN tasks t ON t.id = m.task_id
WHERE m.user_id = $1 AND m.created_at >= $2 AND (t.id IS NULL OR (t.status <> 'failed' AND t.admin_id IS NULL));

-- name: DailyBilledTokens :many
SELECT (m.created_at AT TIME ZONE sqlc.arg(tz)::text)::date AS day, coalesce(sum(m.billed_tokens), 0)::bigint AS tokens
FROM model_usage m LEFT JOIN tasks t ON t.id = m.task_id
WHERE m.user_id = sqlc.arg(user_id) AND m.created_at >= sqlc.arg(since) AND (t.id IS NULL OR (t.status <> 'failed' AND t.admin_id IS NULL))
GROUP BY day ORDER BY day;

-- name: TaskBilledTokensSince :many
SELECT t.id, t.kind, t.title, t.status, t.report_id, t.briefing_id, t.created_at, sum(m.billed_tokens)::bigint AS tokens
FROM tasks t JOIN model_usage m ON m.task_id = t.id
WHERE t.user_id = $1 AND m.created_at >= $2
GROUP BY t.id ORDER BY t.created_at DESC
LIMIT $3;

-- name: SumQuotaGrantsSince :one
SELECT coalesce(sum(tokens), 0)::bigint FROM quota_grants WHERE user_id = $1 AND created_at >= $2;

-- name: InsertQuotaNotice :execrows
INSERT INTO quota_notices (user_id, period, threshold) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING;

-- name: SumTaskBilledTokens :one
SELECT coalesce(sum(billed_tokens), 0)::bigint FROM model_usage WHERE task_id = $1;

-- name: AppendAudit :exec
INSERT INTO audit_log (user_id, task_id, action, detail) VALUES ($1, $2, $3, $4);

-- name: SumReportBilledTokens :one
-- The report's own task plus its follow-ups.
SELECT coalesce(sum(m.billed_tokens), 0)::bigint FROM model_usage m JOIN tasks t ON t.id = m.task_id
WHERE t.report_id = $1 AND t.user_id = $2;
