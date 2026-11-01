-- name: ListModelProviders :many
SELECT sqlc.embed(p), a.email AS created_by_email
FROM model_providers p LEFT JOIN admin_users a ON a.id = p.created_by
ORDER BY p.created_at;

-- name: CreateModelProvider :one
INSERT INTO model_providers (name, display_name, base_url, model_strong, model_fast, timeout_seconds, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: UpdateModelProvider :one
UPDATE model_providers SET display_name = $2, base_url = $3, model_strong = $4, model_fast = $5,
    timeout_seconds = $6, enabled = $7, updated_at = now()
WHERE name = $1
RETURNING *;

-- name: DeleteModelProvider :execrows
DELETE FROM model_providers WHERE name = $1;

-- name: ModelOverridesUsingProvider :many
SELECT scope FROM model_overrides WHERE provider = $1 OR $1 = ANY(fallbacks) ORDER BY scope;
