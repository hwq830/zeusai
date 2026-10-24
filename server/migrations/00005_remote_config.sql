-- +goose Up
-- Remote config center. config_keys is the working copy edited in the console; a release
-- freezes every key into an immutable snapshot. Clients and servers read only the newest
-- published release; rolling back publishes an old snapshot again under a new version.
-- Each deployment (staging, prod, overseas) has its own database, so there is no env column.

CREATE TABLE config_keys (
    key            text PRIMARY KEY CHECK (key ~ '^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$'),
    namespace      text        NOT NULL CHECK (namespace IN ('feature', 'ui', 'quota', 'endpoint', 'killswitch', 'app')),
    type           text        NOT NULL CHECK (type IN ('bool', 'int', 'float', 'string', 'json')),
    description    text        NOT NULL DEFAULT '',
    owner          text        NOT NULL DEFAULT '',
    default_value  jsonb       NOT NULL,
    -- JSON Schema for type json; checked on edit and before publishing.
    value_schema   jsonb,
    -- Ordered targeting rules, first match wins; see remoteconfig.Rule.
    rules          jsonb       NOT NULL DEFAULT '[]',
    apply_mode     text        NOT NULL DEFAULT 'next_launch' CHECK (apply_mode IN ('immediate', 'next_launch')),
    -- false: server-side only, never sent to the App.
    client_visible boolean     NOT NULL DEFAULT true,
    -- true: the App reports config_exposure the first time it reads the key in a session.
    expose         boolean     NOT NULL DEFAULT false,
    archived_at    timestamptz,
    created_by     uuid REFERENCES admin_users(id) ON DELETE SET NULL,
    updated_by     uuid REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- version is assigned when a release is published, so published versions have no gaps
-- caused by rejected or superseded submissions.
CREATE SEQUENCE config_version_seq;

CREATE TABLE config_releases (
    id                bigserial PRIMARY KEY,
    version           bigint UNIQUE,
    status            text        NOT NULL CHECK (status IN ('pending', 'published', 'rejected', 'superseded')),
    snapshot          jsonb       NOT NULL,
    note              text        NOT NULL DEFAULT '',
    rolled_back_from  bigint REFERENCES config_releases(id),
    created_by        uuid REFERENCES admin_users(id) ON DELETE SET NULL,
    decided_by        uuid REFERENCES admin_users(id) ON DELETE SET NULL,
    decision_note     text        NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),
    decided_at        timestamptz,
    published_at      timestamptz,
    CHECK ((status = 'published') = (version IS NOT NULL))
);
CREATE INDEX config_releases_created_idx ON config_releases(id DESC);
-- At most one release waits for approval at a time.
CREATE UNIQUE INDEX config_releases_one_pending ON config_releases(status) WHERE status = 'pending';

-- +goose Down
DROP TABLE config_releases;
DROP SEQUENCE config_version_seq;
DROP TABLE config_keys;
