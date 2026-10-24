-- name: ListOfficialTemplates :many
SELECT * FROM templates
WHERE owner_id IS NULL AND published
  AND (sqlc.narg(category)::text IS NULL OR category = sqlc.narg(category))
  AND (sqlc.narg(q)::text IS NULL OR name ILIKE '%' || sqlc.narg(q) || '%' OR description ILIKE '%' || sqlc.narg(q) || '%')
ORDER BY sort_order, created_at
LIMIT $1 OFFSET $2;

-- name: ListOfficialCategories :many
SELECT DISTINCT category, min(sort_order)::int AS ord FROM templates
WHERE owner_id IS NULL AND published AND category <> ''
GROUP BY category ORDER BY ord, category;

-- name: ListUserTemplates :many
SELECT * FROM templates
WHERE owner_id = $1
  AND (sqlc.narg(q)::text IS NULL OR name ILIKE '%' || sqlc.narg(q) || '%')
ORDER BY updated_at DESC
LIMIT $2 OFFSET $3;

-- name: GetTemplateForUser :one
-- Official templates are visible to everyone; custom ones only to their owner.
SELECT * FROM templates WHERE id = $1 AND (owner_id IS NULL OR owner_id = $2);

-- name: CreateTemplate :one
INSERT INTO templates (owner_id, slug, name, description, category, icon, fields, sections, sources,
                       default_schedule, default_length, required_connectors, sort_order)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: UpdateUserTemplate :one
UPDATE templates SET
    name = $3, description = $4, category = $5, icon = $6, fields = $7, sections = $8, sources = $9,
    default_schedule = $10, default_length = $11, required_connectors = $12,
    version = version + 1, updated_at = now()
WHERE id = $1 AND owner_id = $2
RETURNING *;

-- name: DeleteUserTemplate :execrows
DELETE FROM templates WHERE id = $1 AND owner_id = $2;

-- name: UpsertOfficialTemplate :exec
-- Templates edited in the admin console keep their edits.
INSERT INTO templates (slug, name, description, category, icon, fields, sections, sources,
                       default_schedule, default_length, required_connectors, sort_order)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (slug) DO UPDATE SET
    name = excluded.name, description = excluded.description, category = excluded.category,
    icon = excluded.icon, fields = excluded.fields, sections = excluded.sections,
    sources = excluded.sources, default_schedule = excluded.default_schedule,
    default_length = excluded.default_length, required_connectors = excluded.required_connectors,
    sort_order = excluded.sort_order,
    version = CASE WHEN (templates.fields, templates.sections, templates.sources)
                     IS DISTINCT FROM (excluded.fields, excluded.sections, excluded.sources)
                   THEN templates.version + 1 ELSE templates.version END,
    updated_at = now()
WHERE NOT templates.admin_managed;
