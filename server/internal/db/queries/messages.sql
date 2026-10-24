-- name: CreateMessage :one
INSERT INTO messages (user_id, category, title, body, image_url, deeplink)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: CreateCampaignMessage :one
-- Returns no row when the campaign already reached this user (batch retry).
INSERT INTO messages (user_id, category, title, body, image_url, deeplink, campaign_id)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (campaign_id, user_id) DO NOTHING
RETURNING *;

-- name: SetMessagePush :exec
UPDATE messages SET push_status = $2, push_after = $3, pushed_at = $4 WHERE id = $1;

-- name: ClaimDeferredMessages :many
-- Hands due quiet-hours pushes to one flusher; others skip the locked rows.
UPDATE messages SET push_status = 'sending'
WHERE id IN (
    SELECT id FROM messages
    WHERE push_status = 'deferred' AND push_after <= sqlc.arg(due)::timestamptz
    ORDER BY push_after
    LIMIT sqlc.arg(max_rows)
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: ListMessages :many
SELECT * FROM messages
WHERE user_id = $1 AND deleted_at IS NULL AND recalled_at IS NULL
  AND (coalesce(cardinality(sqlc.arg(categories)::text[]), 0) = 0 OR category = ANY(sqlc.arg(categories)::text[]))
ORDER BY created_at DESC, id DESC
LIMIT $2 OFFSET $3;

-- name: CountUnreadMessages :one
SELECT count(*)::int FROM messages
WHERE user_id = $1 AND read_at IS NULL AND deleted_at IS NULL AND recalled_at IS NULL;

-- name: GetMessage :one
SELECT * FROM messages WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL AND recalled_at IS NULL;

-- name: MarkMessageRead :execrows
UPDATE messages SET read_at = coalesce(read_at, now())
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL AND recalled_at IS NULL;

-- name: MarkMessageOpened :execrows
UPDATE messages SET read_at = coalesce(read_at, now()), opened_at = coalesce(opened_at, now())
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL AND recalled_at IS NULL;

-- name: MarkAllMessagesRead :exec
UPDATE messages SET read_at = now()
WHERE user_id = $1 AND read_at IS NULL AND deleted_at IS NULL AND recalled_at IS NULL
  AND (coalesce(cardinality(sqlc.arg(categories)::text[]), 0) = 0 OR category = ANY(sqlc.arg(categories)::text[]));

-- name: DeleteMessage :execrows
UPDATE messages SET deleted_at = now() WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;

-- name: CountMarketingPushesSince :one
SELECT count(*)::int FROM messages
WHERE user_id = $1 AND category = 'marketing' AND push_status = 'sent' AND pushed_at >= $2;

-- name: GetNotificationPrefs :one
SELECT * FROM notification_prefs WHERE user_id = $1;

-- name: UpsertNotificationPrefs :one
INSERT INTO notification_prefs (user_id, briefing, report, marketing, marketing_opt_in_at, quiet_enabled, quiet_start, quiet_end)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (user_id) DO UPDATE SET
    briefing = excluded.briefing, report = excluded.report, marketing = excluded.marketing,
    marketing_opt_in_at = excluded.marketing_opt_in_at, quiet_enabled = excluded.quiet_enabled,
    quiet_start = excluded.quiet_start, quiet_end = excluded.quiet_end, updated_at = now()
RETURNING *;

-- name: TouchUserSeen :exec
UPDATE users SET last_seen_at = $2 WHERE id = $1 AND (last_seen_at IS NULL OR last_seen_at < $2);

-- ---------------------------------------------------------------- campaigns (admin)

-- name: CreateCampaign :one
INSERT INTO campaigns (name, category, title, body, image_url, deeplink, audience, send_at, audience_estimate, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: UpdateCampaignDraft :one
UPDATE campaigns SET name = $2, category = $3, title = $4, body = $5, image_url = $6, deeplink = $7,
    audience = $8, send_at = $9, audience_estimate = $10, updated_at = now()
WHERE id = $1 AND status = 'draft'
RETURNING *;

-- name: DeleteCampaignDraft :execrows
DELETE FROM campaigns WHERE id = $1 AND status = 'draft';

-- name: GetCampaign :one
SELECT * FROM campaigns WHERE id = $1;

-- name: ListCampaigns :many
SELECT * FROM campaigns
WHERE (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
ORDER BY created_at DESC
LIMIT $1 OFFSET $2;

-- name: SubmitCampaign :one
UPDATE campaigns SET status = 'pending_approval', submitted_at = now(), reject_reason = '', audience_estimate = $2, updated_at = now()
WHERE id = $1 AND status = 'draft'
RETURNING *;

-- name: RejectCampaign :one
UPDATE campaigns SET status = 'draft', reject_reason = $2, updated_at = now()
WHERE id = $1 AND status = 'pending_approval'
RETURNING *;

-- name: ApproveCampaign :one
UPDATE campaigns SET status = 'scheduled', approved_by = $2, approved_at = now(), updated_at = now()
WHERE id = $1 AND status = 'pending_approval'
RETURNING *;

-- name: StartCampaignSending :one
UPDATE campaigns SET status = 'sending', started_at = coalesce(started_at, now()), updated_at = now()
WHERE id = $1 AND status IN ('scheduled', 'sending')
RETURNING *;

-- name: FinishCampaign :exec
UPDATE campaigns SET status = 'sent', finished_at = now(), updated_at = now() WHERE id = $1 AND status = 'sending';

-- name: CancelCampaign :one
UPDATE campaigns SET status = 'cancelled', cancelled_at = now(), updated_at = now()
WHERE id = $1 AND status IN ('pending_approval', 'scheduled', 'sending')
RETURNING *;

-- name: RecallCampaign :one
UPDATE campaigns SET status = 'recalled', recalled_at = now(), updated_at = now()
WHERE id = $1 AND status IN ('sending', 'sent', 'cancelled')
RETURNING *;

-- name: RecallCampaignMessages :execrows
UPDATE messages SET recalled_at = now() WHERE campaign_id = $1 AND recalled_at IS NULL;

-- name: CampaignStats :one
SELECT count(*)::int                                                     AS messages,
       count(*) FILTER (WHERE push_status = 'sent')::int                 AS pushed,
       count(*) FILTER (WHERE push_status = 'failed')::int               AS push_failed,
       count(*) FILTER (WHERE push_status IN ('no_device', 'off', 'capped', 'skipped'))::int AS push_skipped,
       count(*) FILTER (WHERE push_status IN ('pending', 'deferred', 'sending'))::int        AS push_pending,
       count(*) FILTER (WHERE read_at IS NOT NULL)::int                  AS read,
       count(*) FILTER (WHERE opened_at IS NOT NULL)::int                AS opened
FROM messages WHERE campaign_id = $1;

-- name: CampaignAudiencePage :many
-- Users matching a campaign's targeting rule, keyset-paged by id. Suspended users never match.
SELECT u.id FROM users u
WHERE u.id > sqlc.arg(after_id)::uuid
  AND u.disabled_at IS NULL
  AND (sqlc.narg(user_ids)::uuid[] IS NULL OR u.id = ANY(sqlc.narg(user_ids)::uuid[]))
  AND (sqlc.narg(registered_after)::timestamptz IS NULL OR u.created_at >= sqlc.narg(registered_after))
  AND (sqlc.narg(registered_before)::timestamptz IS NULL OR u.created_at < sqlc.narg(registered_before))
  AND (sqlc.narg(seen_after)::timestamptz IS NULL OR u.last_seen_at >= sqlc.narg(seen_after))
  AND (sqlc.narg(not_seen_after)::timestamptz IS NULL OR u.last_seen_at IS NULL OR u.last_seen_at < sqlc.narg(not_seen_after))
  AND (sqlc.narg(min_app_version)::int[] IS NULL OR EXISTS (
        SELECT 1 FROM devices d WHERE d.user_id = u.id AND CASE
            WHEN d.app_version ~ '^[0-9]+(\.[0-9]+)*$' THEN string_to_array(d.app_version, '.')::int[] >= sqlc.narg(min_app_version)::int[]
            ELSE false END))
  AND (sqlc.narg(template_id)::uuid IS NULL OR EXISTS (
        SELECT 1 FROM briefing_subscriptions s WHERE s.user_id = u.id AND s.template_id = sqlc.narg(template_id) AND s.status = 'active'))
ORDER BY u.id
LIMIT sqlc.arg(page_size);

-- name: CountCampaignAudience :one
SELECT count(*)::int FROM users u
WHERE u.disabled_at IS NULL
  AND (sqlc.narg(user_ids)::uuid[] IS NULL OR u.id = ANY(sqlc.narg(user_ids)::uuid[]))
  AND (sqlc.narg(registered_after)::timestamptz IS NULL OR u.created_at >= sqlc.narg(registered_after))
  AND (sqlc.narg(registered_before)::timestamptz IS NULL OR u.created_at < sqlc.narg(registered_before))
  AND (sqlc.narg(seen_after)::timestamptz IS NULL OR u.last_seen_at >= sqlc.narg(seen_after))
  AND (sqlc.narg(not_seen_after)::timestamptz IS NULL OR u.last_seen_at IS NULL OR u.last_seen_at < sqlc.narg(not_seen_after))
  AND (sqlc.narg(min_app_version)::int[] IS NULL OR EXISTS (
        SELECT 1 FROM devices d WHERE d.user_id = u.id AND CASE
            WHEN d.app_version ~ '^[0-9]+(\.[0-9]+)*$' THEN string_to_array(d.app_version, '.')::int[] >= sqlc.narg(min_app_version)::int[]
            ELSE false END))
  AND (sqlc.narg(template_id)::uuid IS NULL OR EXISTS (
        SELECT 1 FROM briefing_subscriptions s WHERE s.user_id = u.id AND s.template_id = sqlc.narg(template_id) AND s.status = 'active'));

-- name: RevertCampaignApproval :exec
-- The workflow could not be started; the campaign goes back to waiting for approval.
UPDATE campaigns SET status = 'pending_approval', approved_by = NULL, approved_at = NULL, updated_at = now()
WHERE id = $1 AND status = 'scheduled';
