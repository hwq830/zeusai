-- +goose Up
-- The model gateway's routing now lives in the database and is edited on the console's 模型路由
-- page; the routing file (ZEUS_MODELGW_CONFIG) is imported once, when this row does not exist
-- yet, and is otherwise only a fallback when the database cannot be read.
-- routes: {"strong": [{"provider": "qwen", "model": "qwen-max"}, {"provider": "deepseek", "model": ""}], "fast": [...]}
-- tried in order; model "" means the provider's default model for the tier.
CREATE TABLE gateway_settings (
    id                       boolean PRIMARY KEY DEFAULT true CHECK (id),
    routes                   jsonb       NOT NULL,
    breaker_failures         integer     NOT NULL DEFAULT 3,
    breaker_cooldown_seconds integer     NOT NULL DEFAULT 30,
    imported_from_file       boolean     NOT NULL DEFAULT false,
    updated_by               uuid REFERENCES admin_users(id) ON DELETE SET NULL,
    updated_at               timestamptz NOT NULL DEFAULT now()
);

-- A snapshot of the routing (gateway_settings plus per-purpose choices) after every change, so
-- the console can go back to an earlier version.
CREATE TABLE routing_versions (
    id         bigserial PRIMARY KEY,
    snapshot   jsonb       NOT NULL,
    action     text        NOT NULL DEFAULT '',
    created_by uuid REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Providers imported from the routing file keep its key variable as a fallback for the key.
ALTER TABLE model_providers
    ADD COLUMN api_key_env text    NOT NULL DEFAULT '',
    ADD COLUMN imported    boolean NOT NULL DEFAULT false;

-- Prices entered with a model choice move to the pricing page, now the only place for prices.
-- (model_overrides keeps its price and tier rows for a rollback to the previous release; the
-- gateway no longer reads them.)
INSERT INTO model_prices (model, manual_input, manual_output, manual_cached, note, updated_by, updated_at)
SELECT DISTINCT ON (model) model, price_input, coalesce(price_output, 0), coalesce(price_cached, 0),
       '从「模型指定」迁来', updated_by, updated_at
FROM model_overrides
WHERE price_input IS NOT NULL
ORDER BY model, updated_at DESC
ON CONFLICT (model) DO UPDATE SET manual_input = EXCLUDED.manual_input, manual_output = EXCLUDED.manual_output,
    manual_cached = EXCLUDED.manual_cached, note = EXCLUDED.note, updated_by = EXCLUDED.updated_by,
    updated_at = EXCLUDED.updated_at
WHERE model_prices.manual_input IS NULL;

-- +goose Down
ALTER TABLE model_providers DROP COLUMN api_key_env, DROP COLUMN imported;
DROP TABLE routing_versions;
DROP TABLE gateway_settings;
