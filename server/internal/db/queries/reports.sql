-- name: CountReportsSince :one
SELECT count(*)::int FROM reports WHERE user_id = $1 AND created_at >= $2 AND status <> 'failed';

-- name: CreateReport :one
INSERT INTO reports (user_id, task_id, title, topic, depth, focuses, source_briefing_item_id, language)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetReport :one
SELECT * FROM reports WHERE id = $1 AND user_id = $2;

-- name: ListReports :many
SELECT * FROM reports
WHERE user_id = $1
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
  AND (sqlc.narg(q)::text IS NULL OR title ILIKE '%' || sqlc.narg(q) || '%' OR topic ILIKE '%' || sqlc.narg(q) || '%')
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: CompleteReport :one
UPDATE reports SET
    status = 'done', title = $3, summary = $4, markdown = $5, sources = $6, source_count = $7,
    word_count = $8, completed_at = now()
WHERE id = $1 AND user_id = $2
RETURNING *;

-- name: AppendReportMarkdown :exec
UPDATE reports SET markdown = markdown || $3 WHERE id = $1 AND user_id = $2;

-- name: SetReportStatus :exec
UPDATE reports SET status = $3, completed_at = CASE WHEN $3 <> 'running' THEN now() ELSE completed_at END
WHERE id = $1 AND user_id = $2;

-- name: SetReportPDF :exec
UPDATE reports SET pdf_status = $3, pdf_key = $4 WHERE id = $1 AND user_id = $2;

-- name: DeleteReport :execrows
DELETE FROM reports WHERE id = $1 AND user_id = $2;

-- name: CreateShare :one
INSERT INTO shares (user_id, report_id, token, scope) VALUES ($1, $2, $3, $4) RETURNING *;

-- name: RevokeShare :execrows
UPDATE shares SET revoked_at = now() WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL;

-- name: GetSharedReport :one
-- Public lookup by share token; the only query that does not take user_id.
SELECT s.scope, r.* FROM shares s JOIN reports r ON r.id = s.report_id
WHERE s.token = $1 AND s.revoked_at IS NULL AND r.status = 'done';

-- name: IncrementShareViews :exec
UPDATE shares SET view_count = view_count + 1 WHERE token = $1;
