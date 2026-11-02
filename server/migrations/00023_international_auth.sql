-- +goose Up
ALTER TABLE users ADD COLUMN email text UNIQUE;
ALTER TABLE users ADD COLUMN google_sub text UNIQUE;
ALTER TABLE users ADD COLUMN google_email text;

-- +goose Down
ALTER TABLE users DROP COLUMN google_email, DROP COLUMN google_sub, DROP COLUMN email;
