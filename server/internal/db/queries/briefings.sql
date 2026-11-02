-- name: CountActiveSubscriptions :one
SELECT count(*)::int FROM briefing_subscriptions WHERE user_id = $1 AND status <> 'deleted';

-- name: CreateSubscription :one
INSERT INTO briefing_subscriptions (user_id, template_id, template_version, name, field_values, topics,
                                    schedule, length, push_enabled, next_run_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: ListSubscriptions :many
SELECT * FROM briefing_subscriptions WHERE user_id = $1 AND status <> 'deleted' ORDER BY created_at;

-- name: GetSubscription :one
SELECT * FROM briefing_subscriptions WHERE id = $1 AND user_id = $2 AND status <> 'deleted';

-- name: UpdateSubscription :one
UPDATE briefing_subscriptions SET
    name = $3, field_values = $4, topics = $5, schedule = $6, length = $7, push_enabled = $8,
    status = $9, paused_reason = $10, next_run_at = $11, updated_at = now()
WHERE id = $1 AND user_id = $2 AND status <> 'deleted'
RETURNING *;

-- name: DeleteSubscription :execrows
-- Keep the subscription identity and all issues for history and existing shares.
UPDATE briefing_subscriptions SET status = 'deleted', paused_reason = 'user_deleted',
    next_run_at = NULL, push_enabled = false, updated_at = now()
WHERE id = $1 AND user_id = $2;

-- name: SetSubscriptionLastBriefing :exec
UPDATE briefing_subscriptions SET last_briefing_id = $3, next_run_at = $4, updated_at = now()
WHERE id = $1 AND user_id = $2 AND status <> 'deleted';

-- name: StartBriefing :one
-- One briefing per subscription per day; a re-run of the same day regenerates it.
INSERT INTO briefings (user_id, subscription_id, task_id, title, date, language)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (subscription_id, date) DO UPDATE SET
    task_id = excluded.task_id, title = excluded.title, language = excluded.language, status = 'generating'
RETURNING *;

-- name: CompleteBriefing :one
UPDATE briefings SET
    status = 'ready', headline = $3, sections = $4, item_count = $5, source_count = $6, generated_at = now()
WHERE id = $1 AND user_id = $2
RETURNING *;

-- name: FailBriefing :exec
UPDATE briefings SET status = 'failed' WHERE id = $1 AND user_id = $2;

-- name: GetBriefing :one
SELECT * FROM briefings WHERE id = $1 AND user_id = $2;

-- name: MarkBriefingRead :exec
UPDATE briefings SET read_at = coalesce(read_at, now()) WHERE id = $1 AND user_id = $2;

-- name: TouchSubscriptionOpened :exec
UPDATE briefing_subscriptions SET last_opened_at = now() WHERE id = $1 AND user_id = $2 AND status <> 'deleted';

-- name: ListBriefings :many
SELECT * FROM briefings
WHERE user_id = $1 AND (sqlc.narg(subscription_id)::uuid IS NULL OR subscription_id = sqlc.narg(subscription_id))
  AND (sqlc.narg(date_from)::date IS NULL OR date >= sqlc.narg(date_from))
  AND (sqlc.narg(date_to)::date IS NULL OR date <= sqlc.narg(date_to))
  AND (NOT sqlc.arg(unread_only)::boolean OR read_at IS NULL)
ORDER BY date DESC, created_at DESC, id DESC
LIMIT $2 OFFSET $3;

-- name: ListBriefingsOnDate :many
SELECT * FROM briefings WHERE user_id = $1 AND date = $2 ORDER BY created_at;

-- name: UpsertBriefingFeedback :exec
INSERT INTO briefing_feedback (briefing_id, item_id, user_id, rating, comment)
SELECT b.id, $3, b.user_id, $4, $5 FROM briefings b WHERE b.id = $1 AND b.user_id = $2
ON CONFLICT (briefing_id, item_id) DO UPDATE SET rating = excluded.rating, comment = excluded.comment, created_at = now();

-- name: GetBriefingFeedback :one
SELECT rating FROM briefing_feedback WHERE briefing_id = $1 AND user_id = $2 AND item_id = '';

-- name: SetBriefingPDF :exec
UPDATE briefings SET pdf_status = $3, pdf_key = $4 WHERE id = $1 AND user_id = $2;

-- name: ListBriefingHistorySubscriptions :many
SELECT s.id, s.name, (s.status = 'deleted')::boolean AS deleted
FROM briefing_subscriptions s
WHERE s.user_id = $1
  AND EXISTS (SELECT 1 FROM briefings b WHERE b.subscription_id = s.id AND b.user_id = $1)
ORDER BY s.created_at, s.id;
