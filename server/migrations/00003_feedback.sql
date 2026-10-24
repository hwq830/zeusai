-- +goose Up
-- User feedback: free-form feedback with screenshots and official replies, and report ratings
-- (the "report satisfaction" metric behind gate B).

CREATE TABLE feedback (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id           uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type              text        NOT NULL CHECK (type IN ('bug', 'suggestion', 'other')),
    content           text        NOT NULL,
    contact           text        NOT NULL DEFAULT '',
    -- App version, OS, device model, locale as sent by the client.
    client            jsonb       NOT NULL DEFAULT '{}',
    report_id         uuid REFERENCES reports(id) ON DELETE SET NULL,
    task_id           uuid REFERENCES tasks(id) ON DELETE SET NULL,
    status            text        NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'in_progress', 'resolved', 'closed')),
    -- Keyword moderation only flags feedback; it is never rejected.
    flagged_reason    text        NOT NULL DEFAULT '',
    admin_note        text        NOT NULL DEFAULT '',
    handled_by        uuid REFERENCES admin_users(id) ON DELETE SET NULL,
    last_reply_at     timestamptz,
    user_read_at      timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX feedback_user_idx ON feedback(user_id, created_at DESC);
CREATE INDEX feedback_status_idx ON feedback(status, created_at DESC);

-- Uploaded before the feedback is submitted; feedback_id is set on submit.
CREATE TABLE feedback_attachments (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    feedback_id  uuid REFERENCES feedback(id) ON DELETE CASCADE,
    blob_key     text        NOT NULL,
    size_bytes   int         NOT NULL,
    position     int         NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX feedback_attachments_feedback_idx ON feedback_attachments(feedback_id);

CREATE TABLE feedback_replies (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    feedback_id  uuid        NOT NULL REFERENCES feedback(id) ON DELETE CASCADE,
    admin_id     uuid REFERENCES admin_users(id) ON DELETE SET NULL,
    content      text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX feedback_replies_feedback_idx ON feedback_replies(feedback_id, created_at);

-- One rating per report; re-rating overwrites.
CREATE TABLE report_feedback (
    report_id   uuid PRIMARY KEY REFERENCES reports(id) ON DELETE CASCADE,
    user_id     uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    rating      text        NOT NULL CHECK (rating IN ('satisfied', 'unsatisfied')),
    reasons     text[]      NOT NULL DEFAULT '{}',
    comment     text        NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX report_feedback_updated_idx ON report_feedback(updated_at DESC);

-- +goose Down
DROP TABLE report_feedback, feedback_replies, feedback_attachments, feedback;
