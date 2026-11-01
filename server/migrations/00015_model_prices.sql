-- +goose Up
-- Bailian (Model Studio) model prices, CNY per million tokens, managed on the console's pricing
-- page. tiers is the list price as last synced from Bailian (one entry for a flat price, one per
-- prompt-length band otherwise); manual_* is an admin's price, which wins and survives syncs.
-- Cost metering on Bailian endpoints uses manual_*, else the first tier, else the built-in
-- catalog shipped with the server.
CREATE TABLE model_prices (
    model           text PRIMARY KEY,
    vendor          text        NOT NULL DEFAULT '',
    context_window  integer     NOT NULL DEFAULT 0,
    tiers           jsonb       NOT NULL DEFAULT '[]',
    synced_at       timestamptz,
    manual_input    double precision,
    manual_output   double precision,
    manual_cached   double precision,
    note            text        NOT NULL DEFAULT '',
    updated_by      uuid REFERENCES admin_users(id) ON DELETE SET NULL,
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE model_prices;
