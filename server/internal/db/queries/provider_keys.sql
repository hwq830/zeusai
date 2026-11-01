-- name: ListActiveProviderKeys :many
SELECT provider, ciphertext, base_url FROM provider_keys WHERE active;

-- name: ListProviderKeys :many
SELECT k.*, a.email AS created_by_email
FROM provider_keys k LEFT JOIN admin_users a ON a.id = k.created_by
ORDER BY k.provider, k.created_at DESC;

-- name: GetProviderKey :one
SELECT * FROM provider_keys WHERE id = $1;

-- name: CreateProviderKey :one
INSERT INTO provider_keys (provider, label, ciphertext, last4, created_by, base_url)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: DeactivateProviderKeys :exec
UPDATE provider_keys SET active = false, updated_at = now() WHERE provider = $1 AND active;

-- name: SetProviderKeyActive :execrows
UPDATE provider_keys SET active = $2, updated_at = now() WHERE id = $1;

-- name: RecordProviderKeyTest :exec
UPDATE provider_keys
SET last_tested_at = now(), last_test_ok = $2, last_test_message = $3, last_test_latency_ms = $4
WHERE id = $1;

-- name: DeleteProviderKey :execrows
DELETE FROM provider_keys WHERE id = $1;

-- name: UpdateProviderKey :one
UPDATE provider_keys SET label = $2, base_url = $3, updated_at = now() WHERE id = $1
RETURNING *;

-- name: DeleteProviderKeysFor :execrows
DELETE FROM provider_keys WHERE provider = $1;
