-- name: ListModelPrices :many
SELECT p.*, a.email AS updated_by_email
FROM model_prices p LEFT JOIN admin_users a ON a.id = p.updated_by
ORDER BY p.model;

-- name: SyncModelPrice :exec
INSERT INTO model_prices (model, vendor, context_window, tiers, synced_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (model) DO UPDATE SET vendor = EXCLUDED.vendor, context_window = EXCLUDED.context_window,
    tiers = EXCLUDED.tiers, synced_at = EXCLUDED.synced_at;

-- name: SetManualModelPrice :exec
INSERT INTO model_prices (model, manual_input, manual_output, manual_cached, note, updated_by)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (model) DO UPDATE SET manual_input = EXCLUDED.manual_input, manual_output = EXCLUDED.manual_output,
    manual_cached = EXCLUDED.manual_cached, note = EXCLUDED.note, updated_by = EXCLUDED.updated_by, updated_at = now();

-- name: ClearManualModelPrice :execrows
UPDATE model_prices SET manual_input = NULL, manual_output = NULL, manual_cached = NULL, note = '',
    updated_by = $2, updated_at = now()
WHERE model = $1 AND manual_input IS NOT NULL;

-- name: DeleteUnsyncedModelPrice :execrows
DELETE FROM model_prices WHERE model = $1 AND synced_at IS NULL AND manual_input IS NULL;
