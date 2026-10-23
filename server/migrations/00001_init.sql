-- +goose Up
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE users (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    nickname         text        NOT NULL DEFAULT '',
    avatar_url       text        NOT NULL DEFAULT '',
    phone            text UNIQUE,
    wechat_unionid   text UNIQUE,
    wechat_openid    text,
    apple_sub        text UNIQUE,
    timezone         text        NOT NULL DEFAULT 'Asia/Shanghai',
    plan_code        text        NOT NULL DEFAULT 'free',
    plan_expires_at  timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE refresh_tokens (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  bytea       NOT NULL UNIQUE,
    expires_at  timestamptz NOT NULL,
    revoked_at  timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX refresh_tokens_user_idx ON refresh_tokens(user_id);

CREATE TABLE devices (
    push_token   text PRIMARY KEY,
    user_id      uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    platform     text        NOT NULL,
    apns_env     text        NOT NULL DEFAULT 'production',
    app_version  text        NOT NULL DEFAULT '',
    locale       text        NOT NULL DEFAULT '',
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX devices_user_idx ON devices(user_id);

-- owner_id NULL = official template.
CREATE TABLE templates (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id             uuid REFERENCES users(id) ON DELETE CASCADE,
    slug                 text UNIQUE,
    name                 text        NOT NULL,
    description          text        NOT NULL DEFAULT '',
    category             text        NOT NULL DEFAULT '',
    icon                 text        NOT NULL DEFAULT '',
    fields               jsonb       NOT NULL DEFAULT '[]',
    sections             jsonb       NOT NULL DEFAULT '[]',
    sources              jsonb       NOT NULL DEFAULT '[]',
    default_schedule     jsonb,
    default_length       text        NOT NULL DEFAULT 'standard',
    required_connectors  text[]      NOT NULL DEFAULT '{}',
    version              int         NOT NULL DEFAULT 1,
    published            boolean     NOT NULL DEFAULT true,
    sort_order           int         NOT NULL DEFAULT 0,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX templates_owner_idx ON templates(owner_id);

CREATE TABLE briefing_subscriptions (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id           uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    template_id       uuid REFERENCES templates(id) ON DELETE SET NULL,
    template_version  int,
    name              text        NOT NULL,
    field_values      jsonb       NOT NULL DEFAULT '{}',
    topics            text[]      NOT NULL DEFAULT '{}',
    schedule          jsonb       NOT NULL,
    length            text        NOT NULL DEFAULT 'standard',
    push_enabled      boolean     NOT NULL DEFAULT true,
    status            text        NOT NULL DEFAULT 'active',
    paused_reason     text,
    next_run_at       timestamptz,
    last_briefing_id  uuid,
    last_opened_at    timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX briefing_subscriptions_user_idx ON briefing_subscriptions(user_id);

CREATE TABLE tasks (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind             text        NOT NULL,
    status           text        NOT NULL DEFAULT 'queued',
    title            text        NOT NULL DEFAULT '',
    subtitle         text        NOT NULL DEFAULT '',
    input            jsonb       NOT NULL DEFAULT '{}',
    subscription_id  uuid REFERENCES briefing_subscriptions(id) ON DELETE SET NULL,
    report_id        uuid,
    briefing_id      uuid,
    plan             jsonb,
    current_step     jsonb,
    pending_confirm  jsonb,
    last_event_id    bigint      NOT NULL DEFAULT 0,
    error            jsonb,
    prompt_version   text        NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    started_at       timestamptz,
    finished_at      timestamptz
);
CREATE INDEX tasks_user_created_idx ON tasks(user_id, created_at DESC);
CREATE INDEX tasks_user_active_idx ON tasks(user_id) WHERE status IN ('queued', 'running', 'waiting_confirm');

CREATE TABLE briefings (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    subscription_id  uuid        NOT NULL REFERENCES briefing_subscriptions(id) ON DELETE CASCADE,
    task_id          uuid REFERENCES tasks(id) ON DELETE SET NULL,
    title            text        NOT NULL,
    date             date        NOT NULL,
    status           text        NOT NULL DEFAULT 'generating',
    headline         text        NOT NULL DEFAULT '',
    sections         jsonb       NOT NULL DEFAULT '[]',
    item_count       int         NOT NULL DEFAULT 0,
    source_count     int         NOT NULL DEFAULT 0,
    read_at          timestamptz,
    generated_at     timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (subscription_id, date)
);
CREATE INDEX briefings_user_date_idx ON briefings(user_id, date DESC, created_at DESC);

CREATE TABLE briefing_feedback (
    briefing_id  uuid        NOT NULL REFERENCES briefings(id) ON DELETE CASCADE,
    item_id      text        NOT NULL DEFAULT '',
    user_id      uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    rating       text        NOT NULL,
    comment      text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (briefing_id, item_id)
);

CREATE TABLE reports (
    id                       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id                  uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    task_id                  uuid REFERENCES tasks(id) ON DELETE SET NULL,
    title                    text        NOT NULL,
    topic                    text        NOT NULL,
    depth                    text        NOT NULL DEFAULT 'standard',
    focuses                  text[]      NOT NULL DEFAULT '{}',
    source_briefing_item_id  text,
    status                   text        NOT NULL DEFAULT 'running',
    summary                  text        NOT NULL DEFAULT '',
    markdown                 text        NOT NULL DEFAULT '',
    sources                  jsonb       NOT NULL DEFAULT '[]',
    source_count             int         NOT NULL DEFAULT 0,
    word_count               int         NOT NULL DEFAULT 0,
    pdf_key                  text,
    pdf_status               text        NOT NULL DEFAULT 'none',
    created_at               timestamptz NOT NULL DEFAULT now(),
    completed_at             timestamptz
);
CREATE INDEX reports_user_created_idx ON reports(user_id, created_at DESC);

CREATE TABLE shares (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    report_id   uuid        NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
    token       text        NOT NULL UNIQUE,
    scope       text        NOT NULL,
    view_count  int         NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    revoked_at  timestamptz
);

-- Catalog of built-in tools and whitelisted MCP servers (managed from the admin console).
CREATE TABLE connectors (
    id             text PRIMARY KEY,
    name           text        NOT NULL,
    description    text        NOT NULL DEFAULT '',
    icon_url       text        NOT NULL DEFAULT '',
    kind           text        NOT NULL,
    category       text        NOT NULL DEFAULT '',
    requires_auth  boolean     NOT NULL DEFAULT false,
    default_on     boolean     NOT NULL DEFAULT false,
    tools          jsonb       NOT NULL DEFAULT '[]',
    transport      text        NOT NULL DEFAULT 'builtin',
    mcp_version    text        NOT NULL DEFAULT '',
    available      boolean     NOT NULL DEFAULT true,
    sort_order     int         NOT NULL DEFAULT 0
);

CREATE TABLE user_connectors (
    user_id       uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    connector_id  text        NOT NULL REFERENCES connectors(id) ON DELETE CASCADE,
    enabled       boolean     NOT NULL,
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, connector_id)
);

-- Envelope-encrypted with KMS; only egress / the MCP gateway can decrypt.
CREATE TABLE connector_credentials (
    user_id       uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    connector_id  text        NOT NULL REFERENCES connectors(id) ON DELETE CASCADE,
    ciphertext    bytea       NOT NULL,
    key_id        text        NOT NULL,
    expires_at    timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, connector_id)
);

CREATE TABLE memories (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind            text        NOT NULL,
    content         text        NOT NULL,
    embedding       vector(1024),
    source_task_id  uuid REFERENCES tasks(id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX memories_user_idx ON memories(user_id, updated_at DESC);
CREATE INDEX memories_embedding_idx ON memories USING hnsw (embedding vector_cosine_ops);

CREATE TABLE model_usage (
    id             bigserial PRIMARY KEY,
    user_id        uuid REFERENCES users(id) ON DELETE CASCADE,
    task_id        uuid REFERENCES tasks(id) ON DELETE SET NULL,
    provider       text        NOT NULL,
    model          text        NOT NULL,
    tier           text        NOT NULL,
    input_tokens   int         NOT NULL,
    output_tokens  int         NOT NULL,
    cached_tokens  int         NOT NULL DEFAULT 0,
    cost_micros    bigint      NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX model_usage_user_created_idx ON model_usage(user_id, created_at);

CREATE TABLE audit_log (
    id          bigserial PRIMARY KEY,
    user_id     uuid REFERENCES users(id) ON DELETE CASCADE,
    task_id     uuid REFERENCES tasks(id) ON DELETE SET NULL,
    action      text        NOT NULL,
    detail      jsonb       NOT NULL DEFAULT '{}',
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_task_idx ON audit_log(task_id);

-- +goose Down
DROP TABLE audit_log, model_usage, memories, connector_credentials, user_connectors, connectors,
    shares, reports, briefing_feedback, briefings, tasks, briefing_subscriptions, templates,
    devices, refresh_tokens, users;
