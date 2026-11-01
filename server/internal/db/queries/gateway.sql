-- name: GetGatewaySettings :one
SELECT sqlc.embed(g), a.email AS updated_by_email
FROM gateway_settings g LEFT JOIN admin_users a ON a.id = g.updated_by;

-- name: InsertGatewaySettings :execrows
-- Creates the settings unless another process already has; 0 rows means it lost the race.
INSERT INTO gateway_settings (routes, breaker_failures, breaker_cooldown_seconds, imported_from_file, updated_by)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (id) DO NOTHING;

-- name: SaveGatewaySettings :exec
INSERT INTO gateway_settings (routes, breaker_failures, breaker_cooldown_seconds, updated_by)
VALUES ($1, $2, $3, $4)
ON CONFLICT (id) DO UPDATE SET routes = EXCLUDED.routes, breaker_failures = EXCLUDED.breaker_failures,
    breaker_cooldown_seconds = EXCLUDED.breaker_cooldown_seconds, updated_by = EXCLUDED.updated_by, updated_at = now();

-- name: ImportModelProvider :execrows
INSERT INTO model_providers (name, display_name, base_url, model_strong, model_fast, timeout_seconds, api_key_env, imported)
VALUES ($1, $2, $3, $4, $5, $6, $7, true)
ON CONFLICT (name) DO NOTHING;

-- name: ImportModelPrice :exec
-- A routing-file price becomes an admin price unless the model already has one.
INSERT INTO model_prices (model, manual_input, manual_output, manual_cached, note)
VALUES ($1, $2, $3, $4, '从路由文件导入')
ON CONFLICT (model) DO UPDATE SET manual_input = EXCLUDED.manual_input, manual_output = EXCLUDED.manual_output,
    manual_cached = EXCLUDED.manual_cached, note = EXCLUDED.note, updated_at = now()
WHERE model_prices.manual_input IS NULL;

-- name: InsertRoutingVersion :one
INSERT INTO routing_versions (snapshot, action, created_by) VALUES ($1, $2, $3) RETURNING id;

-- name: ListRoutingVersions :many
SELECT v.id, v.action, v.created_at, a.email AS created_by_email
FROM routing_versions v LEFT JOIN admin_users a ON a.id = v.created_by
ORDER BY v.id DESC LIMIT $1;

-- name: GetRoutingVersion :one
SELECT * FROM routing_versions WHERE id = $1;

-- name: DeletePurposeOverrides :exec
DELETE FROM model_overrides WHERE scope NOT IN ('strong', 'fast');
