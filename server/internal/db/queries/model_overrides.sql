-- name: ListModelOverrides :many
SELECT o.*, a.email AS updated_by_email
FROM model_overrides o LEFT JOIN admin_users a ON a.id = o.updated_by
ORDER BY o.scope;

-- name: UpsertModelOverride :one
INSERT INTO model_overrides (scope, provider, model, fallbacks, price_input, price_output, price_cached, updated_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (scope) DO UPDATE SET provider = EXCLUDED.provider, model = EXCLUDED.model,
    fallbacks = EXCLUDED.fallbacks, price_input = EXCLUDED.price_input, price_output = EXCLUDED.price_output,
    price_cached = EXCLUDED.price_cached, updated_by = EXCLUDED.updated_by, updated_at = now()
RETURNING *;

-- name: DeleteModelOverride :execrows
DELETE FROM model_overrides WHERE scope = $1;

-- name: ListPurposeOverrides :many
SELECT o.scope, o.provider, o.model, o.updated_at, a.email AS updated_by_email
FROM model_overrides o LEFT JOIN admin_users a ON a.id = o.updated_by
WHERE o.scope NOT IN ('strong', 'fast')
ORDER BY o.scope;

-- name: SetPurposeOverride :exec
INSERT INTO model_overrides (scope, provider, model, updated_by)
VALUES ($1, $2, $3, $4)
ON CONFLICT (scope) DO UPDATE SET provider = EXCLUDED.provider, model = EXCLUDED.model, fallbacks = NULL,
    price_input = NULL, price_output = NULL, price_cached = NULL, updated_by = EXCLUDED.updated_by, updated_at = now();
