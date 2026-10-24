-- Remote config center. Keys are written only from the admin process; the api process reads
-- the newest published release.

-- name: ListConfigKeys :many
SELECT * FROM config_keys
WHERE (sqlc.arg(include_archived)::bool OR archived_at IS NULL)
ORDER BY namespace, key;

-- name: GetConfigKey :one
SELECT * FROM config_keys WHERE key = $1;

-- name: InsertConfigKey :one
-- Seeds built-in keys too, so a key that already exists is left untouched.
INSERT INTO config_keys (key, namespace, type, description, owner, default_value, value_schema,
                         apply_mode, client_visible, expose, created_by, updated_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11)
ON CONFLICT (key) DO NOTHING
RETURNING *;

-- name: UpdateConfigKey :one
UPDATE config_keys SET
    namespace      = coalesce(sqlc.narg(namespace), namespace),
    description    = coalesce(sqlc.narg(description), description),
    owner          = coalesce(sqlc.narg(owner), owner),
    default_value  = coalesce(sqlc.narg(default_value), default_value),
    value_schema   = CASE WHEN sqlc.arg(set_schema)::bool THEN sqlc.narg(value_schema) ELSE value_schema END,
    rules          = coalesce(sqlc.narg(rules), rules),
    apply_mode     = coalesce(sqlc.narg(apply_mode), apply_mode),
    client_visible = coalesce(sqlc.narg(client_visible), client_visible),
    expose         = coalesce(sqlc.narg(expose), expose),
    archived_at    = CASE WHEN sqlc.narg(archived)::bool IS NULL THEN archived_at
                          WHEN sqlc.narg(archived)::bool THEN coalesce(archived_at, now())
                          ELSE NULL END,
    updated_by     = sqlc.arg(updated_by),
    updated_at     = now()
WHERE key = sqlc.arg(key)
RETURNING *;

-- name: ResetConfigKeyRules :exec
-- Rollback puts the working copy back to the restored snapshot.
UPDATE config_keys SET default_value = $2, rules = $3, updated_by = $4, updated_at = now() WHERE key = $1;

-- name: LatestPublishedConfig :one
SELECT * FROM config_releases WHERE status = 'published' ORDER BY version DESC LIMIT 1;

-- name: LatestConfigVersion :one
SELECT coalesce(max(version), 0)::bigint FROM config_releases WHERE status = 'published';

-- name: GetConfigRelease :one
SELECT * FROM config_releases WHERE id = $1;

-- name: GetConfigReleaseForUpdate :one
SELECT * FROM config_releases WHERE id = $1 FOR UPDATE;

-- name: ListConfigReleases :many
SELECT r.*, coalesce(c.email, '')::text AS created_by_email, coalesce(d.email, '')::text AS decided_by_email
FROM config_releases r
LEFT JOIN admin_users c ON c.id = r.created_by
LEFT JOIN admin_users d ON d.id = r.decided_by
ORDER BY r.id DESC
LIMIT $1 OFFSET $2;

-- name: SupersedePendingConfig :exec
UPDATE config_releases SET status = 'superseded', decided_at = now(), decided_by = $1
WHERE status = 'pending';

-- name: InsertPendingConfig :one
INSERT INTO config_releases (status, snapshot, note, rolled_back_from, created_by)
VALUES ('pending', $1, $2, $3, $4)
RETURNING *;

-- name: InsertPublishedConfig :one
INSERT INTO config_releases (status, version, snapshot, note, rolled_back_from, created_by, decided_by, decided_at, published_at)
VALUES ('published', nextval('config_version_seq'), $1, $2, $3, $4, $4, now(), now())
RETURNING *;

-- name: PublishPendingConfig :one
UPDATE config_releases SET status = 'published', version = nextval('config_version_seq'),
    decided_by = $2, decision_note = $3, decided_at = now(), published_at = now()
WHERE id = $1 AND status = 'pending'
RETURNING *;

-- name: RejectPendingConfig :one
UPDATE config_releases SET status = 'rejected', decided_by = $2, decision_note = $3, decided_at = now()
WHERE id = $1 AND status = 'pending'
RETURNING *;

-- name: PreviousPublishedConfig :one
SELECT * FROM config_releases WHERE status = 'published' AND version < $1 ORDER BY version DESC LIMIT 1;
