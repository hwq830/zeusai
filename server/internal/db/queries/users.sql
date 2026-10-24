-- name: CreateUser :one
INSERT INTO users (nickname, phone, wechat_unionid, wechat_openid, apple_sub, timezone)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserByPhone :one
SELECT * FROM users WHERE phone = $1;

-- name: GetUserByAppleSub :one
SELECT * FROM users WHERE apple_sub = $1;

-- name: GetUserByWechatUnionID :one
SELECT * FROM users WHERE wechat_unionid = $1;

-- name: UpdateUserProfile :one
UPDATE users SET
    nickname = coalesce(sqlc.narg(nickname), nickname),
    timezone = coalesce(sqlc.narg(timezone), timezone),
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteUser :exec
DELETE FROM users WHERE id = $1;

-- name: CreateRefreshToken :exec
INSERT INTO refresh_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, $3);

-- name: ConsumeRefreshToken :one
UPDATE refresh_tokens SET revoked_at = now()
WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()
RETURNING user_id;

-- name: RevokeRefreshToken :exec
UPDATE refresh_tokens SET revoked_at = now()
WHERE token_hash = $1 AND user_id = $2 AND revoked_at IS NULL;

-- name: UpsertDevice :exec
INSERT INTO devices (push_token, user_id, platform, apns_env, app_version, locale)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (push_token) DO UPDATE SET
    user_id = excluded.user_id, platform = excluded.platform, apns_env = excluded.apns_env,
    app_version = excluded.app_version, locale = excluded.locale, updated_at = now();

-- name: DeleteDevice :exec
DELETE FROM devices WHERE push_token = $1 AND user_id = $2;

-- name: ListDevices :many
SELECT * FROM devices WHERE user_id = $1;

-- name: RevokeAllRefreshTokens :exec
UPDATE refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL;

-- name: GetQuotaOverride :one
SELECT * FROM user_quota_overrides WHERE user_id = $1;

-- name: InsertModerationEvent :exec
INSERT INTO moderation_events (user_id, kind, reason, excerpt) VALUES ($1, $2, $3, $4);

-- name: ListModerationTerms :many
SELECT term FROM moderation_terms ORDER BY id;
