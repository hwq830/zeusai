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
