-- +goose Up
-- Shares cover briefings as well as reports: exactly one of report_id / briefing_id is set.
-- item_id narrows a briefing share to one item (scope 'item'). expires_at NULL never expires;
-- links created before this migration keep NULL. view_count now counts distinct visitors per
-- day (see share_views); cta_clicks counts taps on the page's "make your own" button.
ALTER TABLE shares ALTER COLUMN report_id DROP NOT NULL;
ALTER TABLE shares ADD COLUMN briefing_id uuid REFERENCES briefings(id) ON DELETE CASCADE;
ALTER TABLE shares ADD COLUMN item_id text NOT NULL DEFAULT '';
ALTER TABLE shares ADD COLUMN expires_at timestamptz;
ALTER TABLE shares ADD COLUMN channel text NOT NULL DEFAULT '';
ALTER TABLE shares ADD COLUMN show_name boolean NOT NULL DEFAULT false;
ALTER TABLE shares ADD COLUMN wechat_view_count int NOT NULL DEFAULT 0;
ALTER TABLE shares ADD COLUMN cta_clicks int NOT NULL DEFAULT 0;
ALTER TABLE shares ADD CONSTRAINT shares_target_chk CHECK (num_nonnulls(report_id, briefing_id) = 1);
CREATE INDEX shares_user_created_idx ON shares(user_id, created_at DESC);

-- One row per visitor (hash of IP + user agent) per share per day, so reloads don't inflate counts.
CREATE TABLE share_views (
    share_id   uuid    NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
    visitor    text    NOT NULL,
    view_date  date    NOT NULL,
    in_wechat  boolean NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (share_id, visitor, view_date)
);
CREATE INDEX share_views_created_idx ON share_views(created_at);

-- Briefing PDF export, like reports.pdf_status / pdf_key.
ALTER TABLE briefings ADD COLUMN pdf_status text NOT NULL DEFAULT 'none';
ALTER TABLE briefings ADD COLUMN pdf_key text;

-- +goose Down
ALTER TABLE briefings DROP COLUMN pdf_key;
ALTER TABLE briefings DROP COLUMN pdf_status;
DROP TABLE share_views;
DROP INDEX shares_user_created_idx;
DELETE FROM shares WHERE report_id IS NULL;
ALTER TABLE shares DROP CONSTRAINT shares_target_chk;
ALTER TABLE shares DROP COLUMN cta_clicks;
ALTER TABLE shares DROP COLUMN wechat_view_count;
ALTER TABLE shares DROP COLUMN show_name;
ALTER TABLE shares DROP COLUMN channel;
ALTER TABLE shares DROP COLUMN expires_at;
ALTER TABLE shares DROP COLUMN item_id;
ALTER TABLE shares DROP COLUMN briefing_id;
ALTER TABLE shares ALTER COLUMN report_id SET NOT NULL;
