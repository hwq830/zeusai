-- +goose Up
-- Messaging: every notification is first an in-app message (the message center), then maybe an
-- APNs push depending on the user's preferences, quiet hours and frequency caps. Operators send
-- campaigns from the admin console.

-- Bumped on authenticated requests (throttled in the api process); used for campaign targeting.
ALTER TABLE users ADD COLUMN last_seen_at timestamptz;

CREATE TABLE campaigns (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name               text        NOT NULL,
    category           text        NOT NULL CHECK (category IN ('marketing', 'service')),
    title              text        NOT NULL,
    body               text        NOT NULL DEFAULT '',
    image_url          text        NOT NULL DEFAULT '',
    deeplink           text        NOT NULL DEFAULT '',
    -- Targeting rule, see notify.Audience.
    audience           jsonb       NOT NULL DEFAULT '{}',
    status             text        NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft', 'pending_approval', 'scheduled', 'sending', 'sent', 'cancelled', 'recalled')),
    -- NULL sends right after approval.
    send_at            timestamptz,
    audience_estimate  int         NOT NULL DEFAULT 0,
    reject_reason      text        NOT NULL DEFAULT '',
    created_by         uuid REFERENCES admin_users(id) ON DELETE SET NULL,
    submitted_at       timestamptz,
    approved_by        uuid REFERENCES admin_users(id) ON DELETE SET NULL,
    approved_at        timestamptz,
    started_at         timestamptz,
    finished_at        timestamptz,
    cancelled_at       timestamptz,
    recalled_at        timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX campaigns_created_idx ON campaigns(created_at DESC);

CREATE TABLE messages (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    category     text        NOT NULL CHECK (category IN ('briefing', 'report', 'service', 'marketing')),
    title        text        NOT NULL,
    body         text        NOT NULL DEFAULT '',
    image_url    text        NOT NULL DEFAULT '',
    deeplink     text        NOT NULL DEFAULT '',
    campaign_id  uuid REFERENCES campaigns(id) ON DELETE SET NULL,
    -- pending → sent | failed | no_device | off (preference) | capped (frequency cap) | skipped;
    -- deferred waits for push_after (quiet hours) and is claimed as sending by the flusher.
    push_status  text        NOT NULL DEFAULT 'pending'
        CHECK (push_status IN ('pending', 'deferred', 'sending', 'sent', 'failed', 'no_device', 'off', 'capped', 'skipped')),
    push_after   timestamptz,
    pushed_at    timestamptz,
    read_at      timestamptz,
    -- Set when the user opened the app from the push.
    opened_at    timestamptz,
    deleted_at   timestamptz,
    recalled_at  timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX messages_user_idx ON messages(user_id, created_at DESC) WHERE deleted_at IS NULL AND recalled_at IS NULL;
CREATE INDEX messages_deferred_idx ON messages(push_after) WHERE push_status = 'deferred';
CREATE INDEX messages_marketing_pushed_idx ON messages(user_id, pushed_at) WHERE category = 'marketing' AND push_status = 'sent';
-- A campaign reaches a user once, so batch retries are idempotent.
CREATE UNIQUE INDEX messages_campaign_user_idx ON messages(campaign_id, user_id);

-- No row = defaults (briefing and report on, marketing off, quiet hours 22:00–08:00).
CREATE TABLE notification_prefs (
    user_id              uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    briefing             boolean     NOT NULL DEFAULT true,
    report               boolean     NOT NULL DEFAULT true,
    marketing            boolean     NOT NULL DEFAULT false,
    marketing_opt_in_at  timestamptz,
    quiet_enabled        boolean     NOT NULL DEFAULT true,
    -- Minutes after local midnight in users.timezone; start > end spans midnight.
    quiet_start          int         NOT NULL DEFAULT 1320 CHECK (quiet_start BETWEEN 0 AND 1439),
    quiet_end            int         NOT NULL DEFAULT 480 CHECK (quiet_end BETWEEN 0 AND 1439),
    updated_at           timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE notification_prefs, messages, campaigns;
ALTER TABLE users DROP COLUMN last_seen_at;
