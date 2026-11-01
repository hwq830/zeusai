-- +goose Up
-- Model choices made in the admin console, layered over the routing file. scope is a tier
-- ("strong", "fast") or a call purpose ("report.write"). A tier override replaces the tier's
-- first provider and model, and optionally its fallback order; a purpose override is tried
-- first for that purpose, then the tier's chain. Prices (CNY per million tokens) are set for
-- models the routing file has no price for, so cost metering stays right.
CREATE TABLE model_overrides (
    scope         text PRIMARY KEY,
    provider      text        NOT NULL,
    model         text        NOT NULL,
    fallbacks     text[],
    price_input   double precision,
    price_output  double precision,
    price_cached  double precision,
    updated_by    uuid REFERENCES admin_users(id) ON DELETE SET NULL,
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE model_overrides;
