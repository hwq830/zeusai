-- name: ListSearchProviders :many
SELECT * FROM search_providers ORDER BY builtin DESC, created_at;

-- name: CreateSearchProvider :one
INSERT INTO search_providers (name, display_name, protocol, base_url, created_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: UpdateSearchProvider :one
UPDATE search_providers SET display_name = $2, base_url = $3, enabled = $4, updated_at = now()
WHERE name = $1
RETURNING *;

-- name: DeleteSearchProvider :execrows
DELETE FROM search_providers WHERE name = $1;

-- name: GetSearchSettings :one
SELECT * FROM search_settings WHERE id;

-- name: SaveSearchSettings :one
INSERT INTO search_settings (id, chain, updated_by, updated_at) VALUES (true, $1, $2, now())
ON CONFLICT (id) DO UPDATE SET chain = EXCLUDED.chain, updated_by = EXCLUDED.updated_by, updated_at = now()
RETURNING *;
