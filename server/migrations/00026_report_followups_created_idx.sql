-- +goose Up
-- The console's global follow-up list sorts by created_at DESC; the report-scoped list uses
-- the existing (report_id, created_at DESC) index.
CREATE INDEX report_followups_created_idx ON report_followups (created_at DESC);
-- +goose Down
DROP INDEX report_followups_created_idx;
