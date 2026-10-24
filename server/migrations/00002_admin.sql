-- +goose Up
-- Admin console: operator accounts, audit trail, per-user quota overrides, account suspension,
-- DB-managed moderation terms and a log of blocked content.

CREATE TABLE admin_users (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email          text        NOT NULL UNIQUE,
    name           text        NOT NULL DEFAULT '',
    password_hash  text        NOT NULL,
    role           text        NOT NULL CHECK (role IN ('owner', 'operator', 'moderator', 'viewer')),
    disabled_at    timestamptz,
    last_login_at  timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- Every write made from the console.
CREATE TABLE admin_audit_log (
    id           bigserial PRIMARY KEY,
    admin_id     uuid REFERENCES admin_users(id) ON DELETE SET NULL,
    action       text        NOT NULL,
    target_type  text        NOT NULL DEFAULT '',
    target_id    text        NOT NULL DEFAULT '',
    detail       jsonb       NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX admin_audit_log_created_idx ON admin_audit_log(created_at DESC);

ALTER TABLE users
    ADD COLUMN disabled_at     timestamptz,
    ADD COLUMN disabled_reason text NOT NULL DEFAULT '';
CREATE INDEX users_created_idx ON users(created_at DESC);

-- NULL keeps the plan's limit.
CREATE TABLE user_quota_overrides (
    user_id            uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    subscriptions      int,
    reports_per_month  int,
    note               text        NOT NULL DEFAULT '',
    updated_by         uuid REFERENCES admin_users(id) ON DELETE SET NULL,
    updated_at         timestamptz NOT NULL DEFAULT now()
);

-- Rows edited in the console are no longer overwritten by `zeusctl seed`.
ALTER TABLE templates ADD COLUMN admin_managed boolean NOT NULL DEFAULT false;
ALTER TABLE connectors ADD COLUMN admin_managed boolean NOT NULL DEFAULT false;

CREATE TABLE moderation_terms (
    id          bigserial PRIMARY KEY,
    term        text        NOT NULL UNIQUE,
    note        text        NOT NULL DEFAULT '',
    created_by  uuid REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE moderation_events (
    id          bigserial PRIMARY KEY,
    user_id     uuid REFERENCES users(id) ON DELETE CASCADE,
    kind        text        NOT NULL,
    reason      text        NOT NULL DEFAULT '',
    excerpt     text        NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX moderation_events_created_idx ON moderation_events(created_at DESC);

CREATE INDEX tasks_created_idx ON tasks(created_at DESC);
CREATE INDEX model_usage_created_idx ON model_usage(created_at);
CREATE INDEX model_usage_task_idx ON model_usage(task_id);
CREATE INDEX shares_created_idx ON shares(created_at DESC);

-- +goose Down
DROP INDEX shares_created_idx, model_usage_task_idx, model_usage_created_idx, tasks_created_idx;
DROP TABLE moderation_events, moderation_terms, user_quota_overrides, admin_audit_log, admin_users;
ALTER TABLE connectors DROP COLUMN admin_managed;
ALTER TABLE templates DROP COLUMN admin_managed;
DROP INDEX users_created_idx;
ALTER TABLE users DROP COLUMN disabled_reason, DROP COLUMN disabled_at;
