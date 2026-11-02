-- +goose Up
-- Console management of users' reports and briefings (内容 → 调研报告 / 简报 / 内容质量).

-- Tasks an operator started from the console (重新生成 / 立即运行). They are the platform's
-- compensation, so their model calls are never counted against the user's token quota.
ALTER TABLE tasks ADD COLUMN admin_id uuid REFERENCES admin_users(id) ON DELETE SET NULL;

-- Takedown (下架): the row stays, the app shows a placeholder, every share link is revoked.
ALTER TABLE reports ADD COLUMN hidden_at timestamptz;
ALTER TABLE reports ADD COLUMN hidden_reason text NOT NULL DEFAULT '';
ALTER TABLE reports ADD COLUMN hidden_by uuid REFERENCES admin_users(id) ON DELETE SET NULL;
-- A console re-run creates a new report that points back to the one it replaces.
ALTER TABLE reports ADD COLUMN regenerated_from uuid REFERENCES reports(id) ON DELETE SET NULL;
CREATE INDEX reports_created_idx ON reports(created_at DESC);

ALTER TABLE briefings ADD COLUMN hidden_at timestamptz;
ALTER TABLE briefings ADD COLUMN hidden_reason text NOT NULL DEFAULT '';
ALTER TABLE briefings ADD COLUMN hidden_by uuid REFERENCES admin_users(id) ON DELETE SET NULL;
CREATE INDEX briefings_date_idx ON briefings(date DESC, created_at DESC);

-- One briefing item taken out of briefings.sections, kept here so it can be put back.
CREATE TABLE briefing_hidden_items (
    briefing_id  uuid        NOT NULL REFERENCES briefings(id) ON DELETE CASCADE,
    item_id      text        NOT NULL,
    section_key  text        NOT NULL,
    position     int         NOT NULL,
    item         jsonb       NOT NULL,
    reason       text        NOT NULL,
    hidden_by    uuid REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (briefing_id, item_id)
);

-- The issue as it was before a console re-run overwrote it (one briefing per subscription per day).
CREATE TABLE briefing_revisions (
    id            bigserial PRIMARY KEY,
    briefing_id   uuid        NOT NULL REFERENCES briefings(id) ON DELETE CASCADE,
    task_id       uuid REFERENCES tasks(id) ON DELETE SET NULL,
    status        text        NOT NULL,
    headline      text        NOT NULL,
    sections      jsonb       NOT NULL,
    item_count    int         NOT NULL,
    source_count  int         NOT NULL,
    generated_at  timestamptz,
    created_by    uuid REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX briefing_revisions_briefing_idx ON briefing_revisions(briefing_id, created_at DESC);

-- Internal notes operators leave on a report, briefing or subscription.
CREATE TABLE content_notes (
    id           bigserial PRIMARY KEY,
    target_type  text        NOT NULL CHECK (target_type IN ('report', 'briefing', 'subscription')),
    target_id    uuid        NOT NULL,
    admin_id     uuid REFERENCES admin_users(id) ON DELETE SET NULL,
    body         text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX content_notes_target_idx ON content_notes(target_type, target_id, created_at DESC);

-- The exception queue (异常队列) is computed from reports and briefings; this records the ones
-- an operator has handled so they leave the open list.
CREATE TABLE content_issue_resolutions (
    target_type  text        NOT NULL CHECK (target_type IN ('report', 'briefing')),
    target_id    uuid        NOT NULL,
    kind         text        NOT NULL,
    note         text        NOT NULL DEFAULT '',
    resolved_by  uuid REFERENCES admin_users(id) ON DELETE SET NULL,
    resolved_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (target_type, target_id, kind)
);

-- A refund (退还额度) is a token grant tied to the content it refunds, at most once each.
ALTER TABLE quota_grants ADD COLUMN ref text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX quota_grants_ref_idx ON quota_grants(ref) WHERE ref <> '';

-- +goose Down
DROP INDEX quota_grants_ref_idx;
ALTER TABLE quota_grants DROP COLUMN ref;
DROP TABLE content_issue_resolutions, content_notes, briefing_revisions, briefing_hidden_items;
DROP INDEX briefings_date_idx;
ALTER TABLE briefings DROP COLUMN hidden_by, DROP COLUMN hidden_reason, DROP COLUMN hidden_at;
DROP INDEX reports_created_idx;
ALTER TABLE reports DROP COLUMN regenerated_from, DROP COLUMN hidden_by, DROP COLUMN hidden_reason, DROP COLUMN hidden_at;
ALTER TABLE tasks DROP COLUMN admin_id;
