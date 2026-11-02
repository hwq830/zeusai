-- +goose Up
ALTER TABLE users DROP CONSTRAINT users_market_check;
ALTER TABLE users ADD CONSTRAINT users_market_check
    CHECK (market IN ('CN', 'TW', 'HK', 'MO', 'SG', 'MY', 'US', 'other'));
ALTER TABLE search_settings ADD COLUMN regional_chains jsonb NOT NULL DEFAULT '{}'
    CHECK (jsonb_typeof(regional_chains) = 'object');
-- Keep the global chain exactly as configured. Existing clients that omit regional_chains
-- preserve overrides; recommended CN/global routes are explicitly applied in the console.

ALTER TABLE templates ADD COLUMN search_region text NOT NULL DEFAULT ''
    CHECK (search_region IN ('', 'CN', 'TW', 'HK', 'MO', 'SG', 'MY', 'US', 'other', 'global'));

-- +goose Down
ALTER TABLE templates DROP COLUMN search_region;
-- Do not silently rewrite CN users on rollback; rollback requires moving them first.
ALTER TABLE users DROP CONSTRAINT users_market_check;
ALTER TABLE users ADD CONSTRAINT users_market_check
    CHECK (market IN ('TW', 'HK', 'MO', 'SG', 'MY', 'US', 'other'));
ALTER TABLE search_settings DROP COLUMN regional_chains;
