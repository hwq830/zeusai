-- +goose Up
-- The user's region (TW / HK / MO / SG / MY / US / other), reported by the App from the phone's
-- region setting. Official templates can be limited to some markets (markets, empty = all) and
-- can replace their sources per market (market_sources: {"TW": [{type, value}], ...}).
ALTER TABLE users ADD COLUMN market text NOT NULL DEFAULT 'other'
    CHECK (market IN ('TW', 'HK', 'MO', 'SG', 'MY', 'US', 'other'));
ALTER TABLE templates
    ADD COLUMN markets        text[] NOT NULL DEFAULT '{}',
    ADD COLUMN market_sources jsonb  NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE templates DROP COLUMN markets, DROP COLUMN market_sources;
ALTER TABLE users DROP COLUMN market;
