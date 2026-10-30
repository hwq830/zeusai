-- +goose Up
-- API keys for model and search providers, managed from the admin console. Keys are sealed
-- with AES-256-GCM under ZEUS_SECRETS_KEY; only the last four characters are kept in clear
-- for display. At most one key per provider is active; with none, the environment variable
-- named in the routing file is used.
CREATE TABLE provider_keys (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider              text        NOT NULL,
    label                 text        NOT NULL DEFAULT '',
    ciphertext            bytea       NOT NULL,
    last4                 text        NOT NULL,
    active                boolean     NOT NULL DEFAULT false,
    created_by            uuid REFERENCES admin_users(id) ON DELETE SET NULL,
    last_tested_at        timestamptz,
    last_test_ok          boolean,
    last_test_message     text        NOT NULL DEFAULT '',
    last_test_latency_ms  int,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX provider_keys_active_idx ON provider_keys(provider) WHERE active;
CREATE INDEX provider_keys_provider_idx ON provider_keys(provider, created_at DESC);

-- +goose Down
DROP TABLE provider_keys;
