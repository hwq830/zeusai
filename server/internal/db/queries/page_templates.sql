-- Page template queries. Like other admin queries these read across users on purpose; only
-- the admin process exposes them, behind its role checks.

-- name: CreatePageTemplate :one
INSERT INTO page_templates (kind, name, description) VALUES ($1, $2, $3) RETURNING *;

-- name: GetPageTemplate :one
SELECT * FROM page_templates WHERE id = $1 AND archived_at IS NULL;

-- name: GetPageTemplateByKind :one
SELECT * FROM page_templates WHERE kind = $1 AND archived_at IS NULL ORDER BY created_at LIMIT 1;

-- name: ListPageTemplates :many
SELECT t.*, v.id AS active_version_id, v.version AS active_version, v.published_at AS active_published_at
FROM page_templates t
LEFT JOIN page_template_bindings b ON b.kind = t.kind
LEFT JOIN page_template_versions v ON v.id = b.version_id
WHERE t.archived_at IS NULL
  AND (sqlc.narg(kind)::text IS NULL OR t.kind = sqlc.narg(kind))
ORDER BY t.created_at DESC;

-- name: NextPageTemplateVersion :one
SELECT coalesce(max(version), 0) + 1 AS next FROM page_template_versions WHERE template_id = $1;

-- name: CreatePageTemplateVersion :one
INSERT INTO page_template_versions (template_id, version, status, files, contract_version, csp_hashes, created_by)
VALUES ($1, $2, 'draft', $3, $4, $5, $6)
RETURNING *;

-- name: GetPageTemplateVersion :one
SELECT * FROM page_template_versions WHERE id = $1;

-- name: ListPageTemplateVersions :many
SELECT * FROM page_template_versions WHERE template_id = $1 ORDER BY version DESC;

-- name: SetPageTemplateVersionValidation :exec
UPDATE page_template_versions SET status = $2, validation = $3, csp_hashes = $4 WHERE id = $1;

-- name: SetPageTemplateVersionStatus :exec
UPDATE page_template_versions SET status = $2, published_at = CASE WHEN $2 = 'published' THEN now() ELSE published_at END WHERE id = $1;

-- name: RetirePublishedPageTemplateVersions :exec
UPDATE page_template_versions SET status = 'retired'
WHERE template_id = sqlc.arg(template_id) AND status = 'published';

-- name: PublishPageTemplateVersion :exec
UPDATE page_template_versions SET status = 'published', published_at = now() WHERE id = $1;

-- name: UpsertPageTemplateBinding :exec
INSERT INTO page_template_bindings (kind, version_id, updated_by) VALUES ($1, $2, $3)
ON CONFLICT (kind) DO UPDATE SET version_id = excluded.version_id, updated_by = excluded.updated_by, updated_at = now();

-- name: GetPageTemplateBinding :one
SELECT * FROM page_template_bindings WHERE kind = $1;

-- name: ActivePageTemplateVersion :one
SELECT v.* FROM page_template_versions v
JOIN page_template_bindings b ON b.version_id = v.id
WHERE b.kind = $1;

-- name: PreviousPublishedPageTemplateVersion :one
SELECT * FROM page_template_versions
WHERE template_id = sqlc.arg(template_id)
  AND published_at IS NOT NULL
  AND id <> sqlc.arg(exclude_id)
ORDER BY published_at DESC, version DESC
LIMIT 1;

-- name: InsertPageTemplateAudit :exec
INSERT INTO page_template_audit (actor_id, action, template_id, version_id, detail)
VALUES ($1, $2, $3, $4, $5);
