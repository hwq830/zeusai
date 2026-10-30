-- +goose Up
-- One row per user per Beijing day on which the App called the api with the user's token
-- (written where users.last_seen_at is refreshed). Daily/weekly/monthly actives, cohort
-- retention and the gate-B D30 metric are counted from here, so a server-run briefing does
-- not make a user active.
CREATE TABLE user_daily_activity (
    user_id        uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    date           date        NOT NULL,
    platform       text        NOT NULL DEFAULT '',
    app_version    text        NOT NULL DEFAULT '',
    first_seen_at  timestamptz NOT NULL DEFAULT now(),
    last_seen_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, date)
);
CREATE INDEX user_daily_activity_date_idx ON user_daily_activity(date);

-- History before this table existed is approximated from what the database already has:
-- the sign-up day, days with a user-started task (not scheduled briefings) and the last
-- day seen.
INSERT INTO user_daily_activity (user_id, date, first_seen_at, last_seen_at)
SELECT id, (created_at AT TIME ZONE 'Asia/Shanghai')::date, created_at, created_at FROM users
ON CONFLICT DO NOTHING;
INSERT INTO user_daily_activity (user_id, date, first_seen_at, last_seen_at)
SELECT user_id, (created_at AT TIME ZONE 'Asia/Shanghai')::date, min(created_at), max(created_at)
FROM tasks WHERE subscription_id IS NULL
GROUP BY 1, 2
ON CONFLICT DO NOTHING;
INSERT INTO user_daily_activity (user_id, date, first_seen_at, last_seen_at)
SELECT id, (last_seen_at AT TIME ZONE 'Asia/Shanghai')::date, last_seen_at, last_seen_at FROM users
WHERE last_seen_at IS NOT NULL
ON CONFLICT DO NOTHING;

-- Each user's most recent platform: from activity, else from a registered push device.
CREATE VIEW user_platform AS
SELECT DISTINCT ON (user_id) user_id, platform
FROM (
    SELECT user_id, platform, last_seen_at AS at FROM user_daily_activity WHERE platform <> ''
    UNION ALL
    SELECT user_id, platform, updated_at AS at FROM devices
) x
ORDER BY user_id, at DESC;

-- +goose Down
DROP VIEW user_platform;
DROP TABLE user_daily_activity;
