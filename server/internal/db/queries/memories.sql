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
SELECT coalesce(sum(billed_tokens), 0)::bigint FROM model_usage WHERE user_id = $1 AND created_at >= $2;

-- name: SumTaskBilledTokens :one
SELECT coalesce(sum(billed_tokens), 0)::bigint FROM model_usage WHERE task_id = $1;

-- name: AppendAudit :exec
INSERT INTO audit_log (user_id, task_id, action, detail) VALUES ($1, $2, $3, $4);
