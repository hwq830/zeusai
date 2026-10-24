-- name: CreateFeedbackAttachment :one
INSERT INTO feedback_attachments (user_id, blob_key, size_bytes) VALUES ($1, $2, $3) RETURNING *;

-- name: DeleteStaleFeedbackAttachments :many
-- Uploads never attached to a submitted feedback.
DELETE FROM feedback_attachments
WHERE user_id = $1 AND feedback_id IS NULL AND created_at < $2
RETURNING blob_key;

-- name: CreateFeedback :one
INSERT INTO feedback (user_id, type, content, contact, client, report_id, task_id, flagged_reason)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: AttachFeedbackAttachment :execrows
-- Claims an upload of this user that is not yet attached to any feedback.
UPDATE feedback_attachments SET feedback_id = $3, position = $4
WHERE id = $1 AND user_id = $2 AND feedback_id IS NULL;

-- name: CountFeedbackSince :one
SELECT count(*)::int FROM feedback WHERE user_id = $1 AND created_at >= $2;

-- name: GetFeedback :one
SELECT * FROM feedback WHERE id = $1 AND user_id = $2;

-- name: ListFeedback :many
SELECT * FROM feedback WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3;

-- name: MarkFeedbackRead :exec
UPDATE feedback SET user_read_at = now() WHERE id = $1 AND user_id = $2;

-- name: ListFeedbackAttachments :many
SELECT * FROM feedback_attachments WHERE feedback_id = $1 ORDER BY position;

-- name: ListFeedbackReplies :many
SELECT r.*, a.name AS admin_name, a.email AS admin_email
FROM feedback_replies r LEFT JOIN admin_users a ON a.id = r.admin_id
WHERE r.feedback_id = $1 ORDER BY r.created_at;

-- name: UpsertReportFeedback :execrows
INSERT INTO report_feedback (report_id, user_id, rating, reasons, comment)
SELECT r.id, r.user_id, $3, $4, $5 FROM reports r WHERE r.id = $1 AND r.user_id = $2
ON CONFLICT (report_id) DO UPDATE SET
    rating = excluded.rating, reasons = excluded.reasons, comment = excluded.comment, updated_at = now();

-- name: GetReportFeedback :one
SELECT * FROM report_feedback WHERE report_id = $1 AND user_id = $2;

-- ---------------------------------------------------------------- admin

-- name: AdminListFeedback :many
SELECT f.*, u.nickname, u.phone,
    (SELECT count(*) FROM feedback_attachments a WHERE a.feedback_id = f.id)::int AS attachment_count,
    (SELECT count(*) FROM feedback_replies r WHERE r.feedback_id = f.id)::int AS reply_count
FROM feedback f JOIN users u ON u.id = f.user_id
WHERE (sqlc.narg(status)::text IS NULL OR f.status = sqlc.narg(status))
  AND (sqlc.narg(type)::text IS NULL OR f.type = sqlc.narg(type))
  AND (sqlc.narg(user_id)::uuid IS NULL OR f.user_id = sqlc.narg(user_id))
  AND (sqlc.narg(q)::text IS NULL OR f.content ILIKE '%' || sqlc.narg(q) || '%')
ORDER BY f.created_at DESC
LIMIT $1 OFFSET $2;

-- name: AdminGetFeedback :one
SELECT f.*, u.nickname, u.phone, a.name AS handler_name, a.email AS handler_email
FROM feedback f JOIN users u ON u.id = f.user_id LEFT JOIN admin_users a ON a.id = f.handled_by
WHERE f.id = $1;

-- name: AdminUpdateFeedback :one
UPDATE feedback SET
    status = coalesce(sqlc.narg(status), status),
    admin_note = coalesce(sqlc.narg(admin_note), admin_note),
    handled_by = sqlc.arg(handled_by),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: AdminCreateFeedbackReply :one
WITH reply AS (
    INSERT INTO feedback_replies (feedback_id, admin_id, content) VALUES ($1, $2, $3) RETURNING *
), touched AS (
    UPDATE feedback SET last_reply_at = now(), updated_at = now(), handled_by = $2,
        status = CASE WHEN status = 'new' THEN 'in_progress' ELSE status END
    WHERE id = $1
)
SELECT * FROM reply;

-- name: AdminListReportFeedback :many
SELECT f.*, r.title AS report_title, r.depth AS report_depth, r.task_id, r.created_at AS report_created_at,
    u.nickname, u.phone
FROM report_feedback f JOIN reports r ON r.id = f.report_id JOIN users u ON u.id = f.user_id
WHERE (sqlc.narg(rating)::text IS NULL OR f.rating = sqlc.narg(rating))
  AND (sqlc.narg(reason)::text IS NULL OR sqlc.narg(reason) = ANY(f.reasons))
ORDER BY f.updated_at DESC
LIMIT $1 OFFSET $2;

-- name: CountFeedbackByStatus :many
SELECT status, count(*)::int AS n FROM feedback GROUP BY status;

-- name: ReportSatisfactionSince :one
-- Reports created since the given time: how many finished and how they were rated.
SELECT
    count(*) FILTER (WHERE r.status = 'done')::int AS completed,
    count(f.report_id) FILTER (WHERE f.rating = 'satisfied')::int AS satisfied,
    count(f.report_id) FILTER (WHERE f.rating = 'unsatisfied')::int AS unsatisfied
FROM reports r LEFT JOIN report_feedback f ON f.report_id = r.id
WHERE r.created_at >= $1;

-- name: UnsatisfiedReasonsSince :many
SELECT reason::text AS reason, count(*)::int AS n
FROM report_feedback f JOIN reports r ON r.id = f.report_id, unnest(f.reasons) AS reason
WHERE f.rating = 'unsatisfied' AND r.created_at >= $1
GROUP BY reason ORDER BY n DESC;

-- name: BriefingUsefulnessSince :one
-- Whole-issue ratings only (item_id = '').
SELECT
    count(*) FILTER (WHERE f.rating = 'useful')::int AS useful,
    count(*) FILTER (WHERE f.rating = 'meh')::int AS meh
FROM briefing_feedback f JOIN briefings b ON b.id = f.briefing_id
WHERE f.item_id = '' AND b.created_at >= $1;
