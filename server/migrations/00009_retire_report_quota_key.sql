-- +goose Up
-- The free quota is metered in tokens (00006); the report-count key is no longer read or
-- seeded, but databases seeded before then still list it in the console and send it to the
-- App. Archive it so it leaves the console and the next published release.
UPDATE config_keys SET archived_at = now(), updated_at = now()
WHERE key = 'quota.free.reports_per_month' AND archived_at IS NULL;

-- +goose Down
UPDATE config_keys SET archived_at = NULL, updated_at = now()
WHERE key = 'quota.free.reports_per_month';
