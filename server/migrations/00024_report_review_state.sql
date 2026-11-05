-- +goose Up
ALTER TABLE reports ADD COLUMN IF NOT EXISTS review_state text;

-- +goose Down
ALTER TABLE reports DROP COLUMN IF EXISTS review_state;
