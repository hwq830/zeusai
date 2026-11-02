-- +goose Up
-- Search providers are now configured in the admin console (「服务商」 page) instead of being
-- fixed to 博查. Each row speaks one of the supported APIs (protocol); keys live in
-- provider_keys under the same name, with api_key_env as the fallback.
CREATE TABLE search_providers (
    name         text PRIMARY KEY,
    display_name text        NOT NULL,
    protocol     text        NOT NULL CHECK (protocol IN ('bocha', 'tavily', 'brave', 'serper')),
    base_url     text        NOT NULL DEFAULT '',
    api_key_env  text        NOT NULL DEFAULT '',
    enabled      boolean     NOT NULL DEFAULT true,
    builtin      boolean     NOT NULL DEFAULT false,
    created_by   uuid REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- Which providers are used: chain[1] is the primary, the rest are fallbacks tried in order.
-- Reloaded by the orchestrator about once a minute.
CREATE TABLE search_settings (
    id         boolean PRIMARY KEY DEFAULT true CHECK (id),
    chain      text[]      NOT NULL DEFAULT '{}',
    updated_by uuid REFERENCES admin_users(id) ON DELETE SET NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- 博查 was the only provider so far; it keeps its env key and stays the primary.
INSERT INTO search_providers (name, display_name, protocol, api_key_env, builtin)
VALUES ('bocha', '博查搜索', 'bocha', 'ZEUS_BOCHA_API_KEY', true);
INSERT INTO search_settings (chain) VALUES ('{bocha}');

-- +goose Down
DROP TABLE search_settings;
DROP TABLE search_providers;
