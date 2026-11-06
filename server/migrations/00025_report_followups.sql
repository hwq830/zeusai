-- +goose Up
CREATE TABLE report_followups (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 report_id uuid NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 task_id uuid REFERENCES tasks(id) ON DELETE SET NULL,
 question text NOT NULL,
 answer text NOT NULL,
 -- Sources the answer cites, in first-appearance order: [{index,title,url,siteName}].
 sources jsonb NOT NULL DEFAULT '[]'::jsonb,
 -- Null until the user adds the answer to the report body.
 attached_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX report_followups_report_idx ON report_followups (report_id, created_at DESC);
-- +goose Down
DROP TABLE report_followups;
