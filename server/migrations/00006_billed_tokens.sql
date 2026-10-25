-- +goose Up
-- Free quota is metered in billed tokens: each call's raw tokens times the per-model weights
-- in remote config (quota.token_weights), fixed when the call is recorded so later weight
-- changes never rewrite past usage. Existing rows are backfilled at weight 1.
ALTER TABLE model_usage ADD COLUMN billed_tokens bigint NOT NULL DEFAULT 0;
UPDATE model_usage SET billed_tokens = input_tokens + output_tokens;

-- +goose Down
ALTER TABLE model_usage DROP COLUMN billed_tokens;
