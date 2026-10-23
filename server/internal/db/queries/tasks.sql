-- name: CreateTask :one
INSERT INTO tasks (user_id, kind, title, subtitle, input, subscription_id, report_id, prompt_version)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetTask :one
SELECT * FROM tasks WHERE id = $1 AND user_id = $2;

-- name: ListTasks :many
SELECT * FROM tasks
WHERE user_id = $1 AND (NOT sqlc.arg(active_only)::bool OR status IN ('queued', 'running', 'waiting_confirm'))
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: CountActiveTasks :one
SELECT count(*)::int FROM tasks WHERE user_id = $1 AND status IN ('queued', 'running', 'waiting_confirm');

-- name: SetTaskLinks :exec
UPDATE tasks SET report_id = coalesce(sqlc.narg(report_id), report_id), briefing_id = coalesce(sqlc.narg(briefing_id), briefing_id)
WHERE id = $1 AND user_id = $2;

-- name: UpdateTaskProgress :exec
-- Applied by the orchestrator after each event; only moves last_event_id forward.
UPDATE tasks SET
    status = $3,
    plan = coalesce(sqlc.narg(plan), plan),
    current_step = coalesce(sqlc.narg(current_step), current_step),
    pending_confirm = sqlc.narg(pending_confirm),
    last_event_id = greatest(last_event_id, sqlc.arg(last_event_id)),
    error = coalesce(sqlc.narg(error), error),
    started_at = CASE WHEN started_at IS NULL AND $3 <> 'queued' THEN now() ELSE started_at END,
    finished_at = CASE WHEN $3 IN ('done', 'failed', 'cancelled') THEN coalesce(finished_at, now()) ELSE finished_at END
WHERE id = $1 AND user_id = $2;
