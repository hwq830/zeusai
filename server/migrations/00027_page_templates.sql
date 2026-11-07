-- +goose Up
-- Page templates make the report layout a publishable artifact instead of an embedded file.
-- Versions are immutable snapshots; a binding per kind points at the active version.
CREATE TABLE page_templates (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 kind text NOT NULL,
 name text NOT NULL,
 description text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 archived_at timestamptz
);
CREATE INDEX page_templates_kind_idx ON page_templates (kind) WHERE archived_at IS NULL;
-- One live template per kind; the bootstrap import relies on this to stay idempotent.
CREATE UNIQUE INDEX page_templates_one_per_kind ON page_templates (kind) WHERE archived_at IS NULL;

CREATE TABLE page_template_versions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 template_id uuid NOT NULL REFERENCES page_templates(id) ON DELETE CASCADE,
 version int NOT NULL CHECK (version > 0),
 status text NOT NULL CHECK (status IN ('draft','validated','published','retired')),
 files jsonb NOT NULL,
 contract_version text NOT NULL,
 csp_hashes jsonb NOT NULL DEFAULT '[]'::jsonb,
 validation jsonb,
 created_by uuid REFERENCES admin_users(id) ON DELETE SET NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 published_at timestamptz,
 UNIQUE (template_id, version)
);
CREATE INDEX page_template_versions_template_idx ON page_template_versions (template_id, version DESC);

CREATE TABLE page_template_bindings (
 kind text PRIMARY KEY,
 version_id uuid NOT NULL REFERENCES page_template_versions(id) ON DELETE RESTRICT,
 updated_by uuid REFERENCES admin_users(id) ON DELETE SET NULL,
 updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE page_template_audit (
 id bigserial PRIMARY KEY,
 actor_id uuid REFERENCES admin_users(id) ON DELETE SET NULL,
 action text NOT NULL,
 template_id uuid,
 version_id uuid,
 detail jsonb NOT NULL DEFAULT '{}'::jsonb,
 at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX page_template_audit_at_idx ON page_template_audit (at DESC);

-- +goose Down
DROP TABLE page_template_audit;
DROP TABLE page_template_bindings;
DROP TABLE page_template_versions;
DROP TABLE page_templates;
