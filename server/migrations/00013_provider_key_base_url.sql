-- +goose Up
-- An API address saved with a key (e.g. a Bailian workspace endpoint). Empty means the
-- provider's address from the routing file (or Bocha's default).
ALTER TABLE provider_keys ADD COLUMN base_url text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE provider_keys DROP COLUMN base_url;
