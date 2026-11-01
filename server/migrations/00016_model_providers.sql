-- +goose Up
-- Model providers added in the admin console (OpenAI-compatible APIs), alongside the ones in
-- the routing file. Keys live in provider_keys under the same name. The gateway reloads
-- this table about once a minute; a provider is used once a tier or purpose names it in
-- model_overrides (or lists it as a fallback).
CREATE TABLE model_providers (
    name            text PRIMARY KEY,
    display_name    text        NOT NULL,
    base_url        text        NOT NULL,
    model_strong    text        NOT NULL DEFAULT '',
    model_fast      text        NOT NULL DEFAULT '',
    timeout_seconds integer     NOT NULL DEFAULT 120,
    enabled         boolean     NOT NULL DEFAULT true,
    created_by      uuid REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE model_providers;
