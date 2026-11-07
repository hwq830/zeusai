-- Console management of reports, briefings and subscriptions (adminapi/content_*.go).
-- These read across users on purpose; the user-facing queries stay scoped by user_id.

-- ------------------------------------------------------------------ reports

-- name: AdminListReports :many
WITH x AS (
    SELECT r.id, r.user_id, r.task_id, r.title, r.topic, r.depth, r.status, r.source_count, r.word_count,
           r.source_briefing_item_id, r.created_at, r.completed_at, r.hidden_at, r.regenerated_from,
           u.nickname, u.phone,
           coalesce(t.prompt_version, '')::text AS prompt_version, t.started_at, t.finished_at, t.admin_id,
           coalesce((SELECT sum(m.cost_micros) FROM model_usage m JOIN tasks mt ON mt.id = m.task_id
                     WHERE mt.report_id = r.id), 0)::bigint AS cost_micros,
           f.rating, coalesce(f.reasons, '{}')::text[] AS reasons,
           (SELECT count(*) FROM tasks ft WHERE ft.report_id = r.id AND ft.kind = 'followup')::int AS followups,
           (SELECT count(*) FROM shares s WHERE s.report_id = r.id)::int AS shares,
           coalesce((SELECT sum(s.view_count) FROM shares s WHERE s.report_id = r.id), 0)::int AS share_views
    FROM reports r
    JOIN users u ON u.id = r.user_id
    LEFT JOIN tasks t ON t.id = r.task_id
    LEFT JOIN report_feedback f ON f.report_id = r.id
    WHERE (sqlc.narg(id)::uuid IS NULL OR r.id = sqlc.narg(id))
      AND (sqlc.narg(user_id)::uuid IS NULL OR r.user_id = sqlc.narg(user_id))
      AND (sqlc.narg(q)::text IS NULL OR r.title ILIKE '%' || sqlc.narg(q) || '%' OR r.topic ILIKE '%' || sqlc.narg(q) || '%')
      AND (sqlc.narg(status)::text IS NULL
           OR (sqlc.narg(status) = 'hidden' AND r.hidden_at IS NOT NULL)
           OR (sqlc.narg(status) <> 'hidden' AND r.status = sqlc.narg(status)))
      AND (sqlc.narg(depth)::text IS NULL OR r.depth = sqlc.narg(depth))
      AND (sqlc.narg(created_from)::timestamptz IS NULL OR r.created_at >= sqlc.narg(created_from))
      AND (sqlc.narg(created_to)::timestamptz IS NULL OR r.created_at < sqlc.narg(created_to))
      AND (sqlc.narg(rating)::text IS NULL
           OR (sqlc.narg(rating) = 'none' AND f.rating IS NULL)
           OR f.rating = sqlc.narg(rating))
      AND (sqlc.narg(origin)::text IS NULL
           OR (sqlc.narg(origin) = 'briefing' AND r.source_briefing_item_id IS NOT NULL)
           OR (sqlc.narg(origin) = 'direct' AND r.source_briefing_item_id IS NULL))
      AND (sqlc.narg(prompt_version)::text IS NULL OR t.prompt_version = sqlc.narg(prompt_version))
)
SELECT * FROM x
WHERE (sqlc.narg(min_cost_micros)::bigint IS NULL OR x.cost_micros >= sqlc.narg(min_cost_micros))
  AND (sqlc.narg(shared)::bool IS NULL OR (x.shares > 0) = sqlc.narg(shared))
ORDER BY x.created_at DESC
LIMIT $1 OFFSET $2;

-- name: AdminListReportTasks :many
-- The report's own task and its follow-ups, oldest first, with what each cost.
SELECT t.*, coalesce((SELECT sum(m.cost_micros) FROM model_usage m WHERE m.task_id = t.id), 0)::bigint AS cost_micros,
       coalesce((SELECT sum(m.billed_tokens) FROM model_usage m WHERE m.task_id = t.id), 0)::bigint AS billed_tokens
FROM tasks t WHERE t.report_id = $1 ORDER BY t.created_at;

-- name: AdminGetReportFeedback :one
SELECT * FROM report_feedback WHERE report_id = $1;

-- name: AdminListSharesFor :many
SELECT * FROM shares WHERE (report_id = sqlc.narg(report_id) OR briefing_id = sqlc.narg(briefing_id))
ORDER BY created_at DESC;

-- name: AdminRevokeSharesFor :execrows
UPDATE shares SET revoked_at = now()
WHERE revoked_at IS NULL AND (report_id = sqlc.narg(report_id) OR briefing_id = sqlc.narg(briefing_id));

-- name: AdminReportRegenerations :many
SELECT id, status, created_at FROM reports WHERE regenerated_from = $1 ORDER BY created_at DESC;

-- name: AdminSetReportHidden :one
UPDATE reports SET hidden_at = sqlc.narg(hidden_at), hidden_reason = sqlc.arg(reason), hidden_by = sqlc.narg(hidden_by)
WHERE id = sqlc.arg(id) RETURNING *;

-- name: AdminCreateRegeneratedReport :one
INSERT INTO reports (user_id, task_id, title, topic, depth, focuses, source_briefing_item_id, language, regenerated_from)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: SetTaskAdmin :exec
UPDATE tasks SET admin_id = $2 WHERE id = $1;

-- ------------------------------------------------------------------ subscriptions

-- name: AdminListSubscriptions :many
WITH x AS (
    SELECT s.*, u.nickname, u.phone, coalesce(tp.name, '')::text AS template_name,
           coalesce(lb.status, '')::text AS last_status, coalesce(lb.date::text, '')::text AS last_date,
           coalesce(lb.item_count, 0)::int AS last_item_count,
           (SELECT count(*) FROM briefings b WHERE b.subscription_id = s.id)::int AS issues,
           (SELECT count(*) FROM (SELECT 1 FROM briefings b WHERE b.subscription_id = s.id ORDER BY b.date DESC LIMIT 7) r)::int AS recent,
           (SELECT count(*) FROM (SELECT b.status FROM briefings b WHERE b.subscription_id = s.id ORDER BY b.date DESC LIMIT 7) r
            WHERE r.status = 'ready')::int AS recent_ready,
           (SELECT count(*) FROM (SELECT b.read_at FROM briefings b WHERE b.subscription_id = s.id AND b.status = 'ready'
            ORDER BY b.date DESC LIMIT 7) r WHERE r.read_at IS NOT NULL)::int AS recent_read,
           (SELECT count(*) FROM (SELECT b.status FROM briefings b WHERE b.subscription_id = s.id ORDER BY b.date DESC LIMIT 3) r
            WHERE r.status = 'failed')::int AS recent_failed
    FROM briefing_subscriptions s
    JOIN users u ON u.id = s.user_id
    LEFT JOIN templates tp ON tp.id = s.template_id
    LEFT JOIN LATERAL (SELECT b.status, b.date, b.item_count FROM briefings b WHERE b.subscription_id = s.id
                       ORDER BY b.date DESC LIMIT 1) lb ON true
    WHERE s.status <> 'deleted' AND (sqlc.narg(id)::uuid IS NULL OR s.id = sqlc.narg(id))
      AND (sqlc.narg(user_id)::uuid IS NULL OR s.user_id = sqlc.narg(user_id))
      AND (sqlc.narg(template_id)::uuid IS NULL OR s.template_id = sqlc.narg(template_id))
      AND (sqlc.narg(status)::text IS NULL OR s.status = sqlc.narg(status))
      AND (sqlc.narg(q)::text IS NULL OR s.name ILIKE '%' || sqlc.narg(q) || '%')
)
SELECT * FROM x
WHERE (sqlc.narg(last_status)::text IS NULL
       OR (sqlc.narg(last_status) = 'empty' AND x.last_status = 'ready' AND x.last_item_count = 0)
       OR (sqlc.narg(last_status) <> 'empty' AND x.last_status = sqlc.narg(last_status)))
ORDER BY x.created_at DESC
LIMIT $1 OFFSET $2;

-- name: AdminGetSubscription :one
SELECT * FROM briefing_subscriptions WHERE id = $1 AND status <> 'deleted';

-- name: AdminSetSubscriptionStatus :one
UPDATE briefing_subscriptions SET status = $2, paused_reason = $3, next_run_at = $4, updated_at = now()
WHERE id = $1 AND status <> 'deleted' RETURNING *;

-- ------------------------------------------------------------------ briefings

-- name: AdminListBriefings :many
WITH x AS (
    SELECT b.id, b.user_id, b.subscription_id, b.task_id, b.title, b.date, b.status, b.headline, b.item_count,
           b.source_count, b.read_at, b.generated_at, b.created_at, b.hidden_at,
           u.nickname, u.phone, s.template_id, coalesce(tp.name, '')::text AS template_name,
           t.started_at, t.finished_at, t.admin_id,
           coalesce((SELECT sum(m.cost_micros) FROM model_usage m WHERE m.task_id = b.task_id), 0)::bigint AS cost_micros,
           (SELECT count(*) FROM briefing_feedback f WHERE f.briefing_id = b.id AND f.rating = 'useful')::int AS useful,
           (SELECT count(*) FROM briefing_feedback f WHERE f.briefing_id = b.id AND f.rating = 'meh')::int AS meh
    FROM briefings b
    JOIN users u ON u.id = b.user_id
    JOIN briefing_subscriptions s ON s.id = b.subscription_id
    LEFT JOIN templates tp ON tp.id = s.template_id
    LEFT JOIN tasks t ON t.id = b.task_id
    WHERE (sqlc.narg(id)::uuid IS NULL OR b.id = sqlc.narg(id))
      AND (sqlc.narg(user_id)::uuid IS NULL OR b.user_id = sqlc.narg(user_id))
      AND (sqlc.narg(subscription_id)::uuid IS NULL OR b.subscription_id = sqlc.narg(subscription_id))
      AND (sqlc.narg(template_id)::uuid IS NULL OR s.template_id = sqlc.narg(template_id))
      AND (sqlc.narg(q)::text IS NULL OR b.title ILIKE '%' || sqlc.narg(q) || '%' OR b.headline ILIKE '%' || sqlc.narg(q) || '%')
      AND (sqlc.narg(status)::text IS NULL
           OR (sqlc.narg(status) = 'hidden' AND b.hidden_at IS NOT NULL)
           OR (sqlc.narg(status) = 'empty' AND b.status = 'ready' AND b.item_count = 0)
           OR (sqlc.narg(status) NOT IN ('hidden', 'empty') AND b.status = sqlc.narg(status)))
      AND (sqlc.narg(date_from)::date IS NULL OR b.date >= sqlc.narg(date_from))
      AND (sqlc.narg(date_to)::date IS NULL OR b.date <= sqlc.narg(date_to))
      AND (sqlc.narg(read)::bool IS NULL OR (b.read_at IS NOT NULL) = sqlc.narg(read))
)
SELECT * FROM x
WHERE (NOT sqlc.arg(disliked)::bool OR x.meh > 0)
ORDER BY x.date DESC, x.created_at DESC
LIMIT $1 OFFSET $2;

-- name: AdminGetBriefingOnDate :one
SELECT * FROM briefings WHERE subscription_id = $1 AND date = $2;

-- name: AdminGetBriefing :one
SELECT * FROM briefings WHERE id = $1;

-- name: AdminListBriefingFeedback :many
SELECT * FROM briefing_feedback WHERE briefing_id = $1 ORDER BY created_at DESC;

-- name: AdminSetBriefingHidden :one
UPDATE briefings SET hidden_at = sqlc.narg(hidden_at), hidden_reason = sqlc.arg(reason), hidden_by = sqlc.narg(hidden_by)
WHERE id = sqlc.arg(id) RETURNING *;

-- name: AdminSetBriefingSections :exec
UPDATE briefings SET sections = $2, item_count = $3 WHERE id = $1;

-- name: InsertBriefingHiddenItem :exec
INSERT INTO briefing_hidden_items (briefing_id, item_id, section_key, position, item, reason, hidden_by)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: ListBriefingHiddenItems :many
SELECT * FROM briefing_hidden_items WHERE briefing_id = $1 ORDER BY created_at;

-- name: DeleteBriefingHiddenItem :one
DELETE FROM briefing_hidden_items WHERE briefing_id = $1 AND item_id = $2 RETURNING *;

-- name: DeleteBriefingHiddenItems :exec
-- A re-run replaces the sections, so items hidden from the old version no longer apply.
DELETE FROM briefing_hidden_items WHERE briefing_id = $1;

-- name: InsertBriefingRevision :exec
INSERT INTO briefing_revisions (briefing_id, task_id, status, headline, sections, item_count, source_count, generated_at, created_by)
SELECT b.id, b.task_id, b.status, b.headline, b.sections, b.item_count, b.source_count, b.generated_at, $2
FROM briefings b WHERE b.id = $1;

-- name: ListBriefingRevisions :many
SELECT * FROM briefing_revisions WHERE briefing_id = $1 ORDER BY created_at DESC LIMIT 10;

-- ------------------------------------------------------------------ notes, refunds, issues

-- name: ListContentNotes :many
SELECT n.*, coalesce(a.name, '')::text AS admin_name FROM content_notes n
LEFT JOIN admin_users a ON a.id = n.admin_id
WHERE n.target_type = $1 AND n.target_id = $2 ORDER BY n.created_at DESC;

-- name: CreateContentNote :one
INSERT INTO content_notes (target_type, target_id, admin_id, body) VALUES ($1, $2, $3, $4) RETURNING *;

-- name: InsertQuotaGrantRef :one
-- A refund: ON CONFLICT means this content was already refunded.
INSERT INTO quota_grants (user_id, tokens, reason, granted_by, ref) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (ref) WHERE ref <> '' DO NOTHING
RETURNING *;

-- name: GetQuotaGrantByRef :one
SELECT * FROM quota_grants WHERE ref = $1;

-- name: ChargedTaskTokens :one
-- Tokens these tasks counted against the user's quota (same rule as SumBilledTokensSince).
SELECT coalesce(sum(m.billed_tokens), 0)::bigint FROM model_usage m JOIN tasks t ON t.id = m.task_id
WHERE t.id = ANY(sqlc.arg(task_ids)::uuid[]) AND t.status <> 'failed' AND t.admin_id IS NULL;

-- name: AdminContentIssues :many
-- The exception queue: content that needs a look, newest first. kind is one of failed, empty,
-- few_sources, unsatisfied, disliked, costly. A row stays until resolved.
WITH issues AS (
    SELECT 'report'::text AS target_type, r.id AS target_id, 'failed'::text AS kind, r.title, r.user_id, r.created_at, ''::text AS detail
    FROM reports r WHERE r.status = 'failed' AND r.created_at >= sqlc.arg(since)
    UNION ALL
    SELECT 'report', r.id, 'few_sources', r.title, r.user_id, r.created_at, r.source_count::text
    FROM reports r WHERE r.status = 'done' AND r.source_count < 3 AND r.created_at >= sqlc.arg(since)
    UNION ALL
    SELECT 'report', r.id, 'unsatisfied', r.title, r.user_id, f.updated_at, array_to_string(f.reasons, ',')
    FROM reports r JOIN report_feedback f ON f.report_id = r.id
    WHERE f.rating = 'unsatisfied' AND f.updated_at >= sqlc.arg(since)
    UNION ALL
    SELECT 'report', r.id, 'costly', r.title, r.user_id, r.created_at, c.cost::text
    FROM reports r JOIN LATERAL (SELECT coalesce(sum(m.cost_micros), 0) AS cost FROM model_usage m WHERE m.task_id = r.task_id) c ON true
    WHERE r.created_at >= sqlc.arg(since) AND c.cost > sqlc.arg(costly_micros)::bigint
    UNION ALL
    SELECT 'briefing', b.id, 'failed', b.title, b.user_id, b.created_at, ''
    FROM briefings b WHERE b.status = 'failed' AND b.created_at >= sqlc.arg(since)
    UNION ALL
    SELECT 'briefing', b.id, 'empty', b.title, b.user_id, b.created_at, ''
    FROM briefings b WHERE b.status = 'ready' AND b.item_count = 0 AND b.created_at >= sqlc.arg(since)
    UNION ALL
    SELECT 'briefing', b.id, 'few_sources', b.title, b.user_id, b.created_at, b.source_count::text
    FROM briefings b WHERE b.status = 'ready' AND b.item_count > 0 AND b.source_count < 3 AND b.created_at >= sqlc.arg(since)
    UNION ALL
    SELECT 'briefing', b.id, 'disliked', b.title, b.user_id, max(f.created_at), count(*)::text
    FROM briefings b JOIN briefing_feedback f ON f.briefing_id = b.id
    WHERE f.rating = 'meh' AND f.created_at >= sqlc.arg(since)
    GROUP BY b.id
)
SELECT i.target_type, i.target_id, i.kind, i.title, i.user_id, i.created_at, i.detail, u.nickname, u.phone,
       cr.resolved_at, cr.note AS resolution_note, coalesce(a.name, '')::text AS resolved_by_name
FROM issues i
JOIN users u ON u.id = i.user_id
LEFT JOIN content_issue_resolutions cr ON cr.target_type = i.target_type AND cr.target_id = i.target_id AND cr.kind = i.kind
LEFT JOIN admin_users a ON a.id = cr.resolved_by
WHERE (sqlc.narg(kind)::text IS NULL OR i.kind = sqlc.narg(kind))
  AND (sqlc.narg(target_type)::text IS NULL OR i.target_type = sqlc.narg(target_type))
  AND (sqlc.arg(resolved)::bool = (cr.resolved_at IS NOT NULL))
ORDER BY i.created_at DESC
LIMIT $1 OFFSET $2;

-- name: AdminContentIssueCounts :many
-- Open issues per kind, for the tabs on the 内容质量 page.
WITH issues AS (
    SELECT 'report'::text AS target_type, r.id AS target_id, 'failed'::text AS kind
    FROM reports r WHERE r.status = 'failed' AND r.created_at >= sqlc.arg(since)
    UNION ALL
    SELECT 'report', r.id, 'few_sources' FROM reports r WHERE r.status = 'done' AND r.source_count < 3 AND r.created_at >= sqlc.arg(since)
    UNION ALL
    SELECT 'report', r.id, 'unsatisfied' FROM reports r JOIN report_feedback f ON f.report_id = r.id
    WHERE f.rating = 'unsatisfied' AND f.updated_at >= sqlc.arg(since)
    UNION ALL
    SELECT 'report', r.id, 'costly' FROM reports r
    WHERE r.created_at >= sqlc.arg(since)
      AND (SELECT coalesce(sum(m.cost_micros), 0) FROM model_usage m WHERE m.task_id = r.task_id) > sqlc.arg(costly_micros)::bigint
    UNION ALL
    SELECT 'briefing', b.id, 'failed' FROM briefings b WHERE b.status = 'failed' AND b.created_at >= sqlc.arg(since)
    UNION ALL
    SELECT 'briefing', b.id, 'empty' FROM briefings b WHERE b.status = 'ready' AND b.item_count = 0 AND b.created_at >= sqlc.arg(since)
    UNION ALL
    SELECT 'briefing', b.id, 'few_sources' FROM briefings b
    WHERE b.status = 'ready' AND b.item_count > 0 AND b.source_count < 3 AND b.created_at >= sqlc.arg(since)
    UNION ALL
    SELECT DISTINCT 'briefing', b.id, 'disliked' FROM briefings b JOIN briefing_feedback f ON f.briefing_id = b.id
    WHERE f.rating = 'meh' AND f.created_at >= sqlc.arg(since)
)
SELECT i.kind, count(*)::int AS open
FROM issues i
LEFT JOIN content_issue_resolutions cr ON cr.target_type = i.target_type AND cr.target_id = i.target_id AND cr.kind = i.kind
WHERE cr.resolved_at IS NULL
GROUP BY i.kind;

-- name: ResolveContentIssue :exec
INSERT INTO content_issue_resolutions (target_type, target_id, kind, note, resolved_by) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (target_type, target_id, kind) DO UPDATE SET note = excluded.note, resolved_by = excluded.resolved_by, resolved_at = now();

-- name: ReopenContentIssue :execrows
DELETE FROM content_issue_resolutions WHERE target_type = $1 AND target_id = $2 AND kind = $3;

-- ------------------------------------------------------------------ report follow-ups

-- name: AdminReportFollowups :many
SELECT * FROM report_followups
WHERE report_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: AdminListFollowups :many
SELECT f.id, f.report_id, r.title AS report_title, (r.hidden_at IS NOT NULL)::boolean AS report_hidden,
       f.user_id, f.question, f.answer, f.attached_at, f.created_at
FROM report_followups f
JOIN reports r ON r.id = f.report_id
WHERE (sqlc.narg(attached)::boolean IS NULL OR (f.attached_at IS NOT NULL) = sqlc.narg(attached))
  AND (sqlc.narg(user_id)::uuid IS NULL OR f.user_id = sqlc.narg(user_id))
  AND (sqlc.narg(q)::text IS NULL OR f.question ILIKE sqlc.narg(q) ESCAPE '\' OR f.answer ILIKE sqlc.narg(q) ESCAPE '\')
ORDER BY f.created_at DESC
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);
