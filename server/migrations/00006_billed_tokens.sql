-- +goose Up
-- The free quota is metered in billed tokens instead of report counts. Billed tokens are each
-- call's raw tokens times the per-model weights in remote config (quota.token_weights), fixed
-- when the call is recorded so later weight changes never rewrite past usage. Existing rows
-- are backfilled at weight 1.
ALTER TABLE model_usage ADD COLUMN billed_tokens bigint NOT NULL DEFAULT 0;
UPDATE model_usage SET billed_tokens = input_tokens + output_tokens;

-- NULL keeps the plan's monthly token allowance.
ALTER TABLE user_quota_overrides ADD COLUMN tokens_per_month bigint;
ALTER TABLE user_quota_overrides DROP COLUMN reports_per_month;

-- Extra tokens an operator gives a user; they count toward the month they were granted in.
CREATE TABLE quota_grants (
    id          bigserial PRIMARY KEY,
    user_id     uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tokens      bigint      NOT NULL CHECK (tokens > 0),
    reason      text        NOT NULL,
    granted_by  uuid REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX quota_grants_user_idx ON quota_grants(user_id, created_at);

-- One row per usage reminder sent (80% / 100%), so each is sent once per quota month.
CREATE TABLE quota_notices (
    user_id     uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    period      date        NOT NULL,
    threshold   int         NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, period, threshold)
);

-- +goose Down
DROP TABLE quota_notices;
DROP TABLE quota_grants;
ALTER TABLE user_quota_overrides ADD COLUMN reports_per_month int;
ALTER TABLE user_quota_overrides DROP COLUMN tokens_per_month;
ALTER TABLE model_usage DROP COLUMN billed_tokens;
