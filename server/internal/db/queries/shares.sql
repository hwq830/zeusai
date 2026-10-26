-- name: CreateShare :one
INSERT INTO shares (user_id, report_id, briefing_id, item_id, token, scope, expires_at, channel, show_name)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: CountSharesSince :one
SELECT count(*)::int FROM shares WHERE user_id = $1 AND created_at >= $2;

-- name: ListShares :many
-- The user's links, newest first, with the title of what they point at.
SELECT s.*, coalesce(r.title, b.title, '')::text AS target_title,
       coalesce(r.summary, b.headline, '')::text AS target_summary
FROM shares s
LEFT JOIN reports r ON r.id = s.report_id
LEFT JOIN briefings b ON b.id = s.briefing_id
WHERE s.user_id = $1
ORDER BY s.created_at DESC
LIMIT $2 OFFSET $3;

-- name: RevokeShare :execrows
UPDATE shares SET revoked_at = now() WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL;

-- name: RevokeAllShares :execrows
UPDATE shares SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL;

-- name: GetShareByToken :one
-- Public lookup by share token; with RecordShareView and ClickShareCTA the only share queries
-- that do not take user_id. Revoked links are not found; expiry is checked by the caller so it
-- can say "expired".
SELECT * FROM shares WHERE token = $1 AND revoked_at IS NULL;

-- name: RecordShareView :exec
-- Counts a visitor once per share per day.
WITH ins AS (
    INSERT INTO share_views (share_id, visitor, view_date, in_wechat)
    VALUES (sqlc.arg(share_id), sqlc.arg(visitor), sqlc.arg(view_date), sqlc.arg(in_wechat))
    ON CONFLICT DO NOTHING
    RETURNING in_wechat
)
UPDATE shares SET
    view_count = view_count + 1,
    wechat_view_count = wechat_view_count + (SELECT count(*) FROM ins WHERE ins.in_wechat)::int
WHERE id = sqlc.arg(share_id) AND EXISTS (SELECT 1 FROM ins);

-- name: ClickShareCTA :exec
UPDATE shares SET cta_clicks = cta_clicks + 1 WHERE id = $1;
