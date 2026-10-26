-- Admin console queries. Unlike the App queries these read across users on purpose; they are
-- only reachable through the admin process (server/cmd/admin), which checks the operator's role.

-- ------------------------------------------------------------------ operators

-- name: CreateAdminUser :one
INSERT INTO admin_users (email, name, password_hash, role) VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetAdminUser :one
SELECT * FROM admin_users WHERE id = $1;

-- name: GetAdminUserByEmail :one
SELECT * FROM admin_users WHERE email = $1;

-- name: ListAdminUsers :many
SELECT * FROM admin_users ORDER BY created_at;

-- name: UpdateAdminUser :one
UPDATE admin_users SET
    name = coalesce(sqlc.narg(name), name),
    role = coalesce(sqlc.narg(role), role),
    disabled_at = CASE WHEN sqlc.narg(disabled)::bool IS NULL THEN disabled_at
                       WHEN sqlc.narg(disabled)::bool THEN coalesce(disabled_at, now())
                       ELSE NULL END,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetAdminPassword :exec
UPDATE admin_users SET password_hash = $2, updated_at = now() WHERE id = $1;

-- name: TouchAdminLogin :exec
UPDATE admin_users SET last_login_at = now() WHERE id = $1;

-- name: CountActiveOwners :one
SELECT count(*)::int FROM admin_users WHERE role = 'owner' AND disabled_at IS NULL;

-- name: InsertAdminAudit :exec
INSERT INTO admin_audit_log (admin_id, action, target_type, target_id, detail) VALUES ($1, $2, $3, $4, $5);

-- name: ListAdminAudit :many
SELECT l.*, coalesce(a.email, '')::text AS admin_email
FROM admin_audit_log l LEFT JOIN admin_users a ON a.id = l.admin_id
WHERE (sqlc.narg(target_type)::text IS NULL OR l.target_type = sqlc.narg(target_type))
  AND (sqlc.narg(target_id)::text IS NULL OR l.target_id = sqlc.narg(target_id))
ORDER BY l.id DESC
LIMIT $1 OFFSET $2;

-- ------------------------------------------------------------------ dashboard

-- name: DashboardUserCounts :one
SELECT count(*)::int                                                  AS total,
       count(*) FILTER (WHERE created_at >= sqlc.arg(day_start))::int  AS new_today,
       count(*) FILTER (WHERE created_at >= sqlc.arg(week_start))::int AS new_7d,
       count(*) FILTER (WHERE disabled_at IS NOT NULL)::int             AS disabled
FROM users;

-- name: CountActiveUsersSince :one
-- A user is active when they started a task (briefings included) in the window.
SELECT count(DISTINCT user_id)::int FROM tasks WHERE created_at >= $1;

-- name: TaskStatusCountsSince :many
SELECT kind, status, count(*)::int AS n FROM tasks WHERE created_at >= $1 GROUP BY kind, status;

-- name: CountReportsCreatedSince :one
SELECT count(*)::int FROM reports WHERE created_at >= $1;

-- name: SumCostSince :one
SELECT coalesce(sum(cost_micros), 0)::bigint FROM model_usage WHERE created_at >= $1;

-- name: DailyNewUsers :many
SELECT (created_at AT TIME ZONE sqlc.arg(tz)::text)::date AS day, count(*)::int AS n
FROM users WHERE created_at >= sqlc.arg(since)
GROUP BY day ORDER BY day;

-- name: DailyTasks :many
SELECT (created_at AT TIME ZONE sqlc.arg(tz)::text)::date AS day, count(*)::int AS n,
       count(DISTINCT user_id)::int AS users
FROM tasks WHERE created_at >= sqlc.arg(since)
GROUP BY day ORDER BY day;

-- name: DailyCost :many
SELECT (created_at AT TIME ZONE sqlc.arg(tz)::text)::date AS day,
       coalesce(sum(cost_micros), 0)::bigint   AS cost_micros,
       coalesce(sum(input_tokens), 0)::bigint  AS input_tokens,
       coalesce(sum(output_tokens), 0)::bigint AS output_tokens,
       count(*)::int                           AS calls
FROM model_usage WHERE created_at >= sqlc.arg(since)
GROUP BY day ORDER BY day;

-- ------------------------------------------------------------------ users

-- name: AdminListUsers :many
SELECT * FROM users
WHERE (sqlc.narg(q)::text IS NULL
       OR id::text = sqlc.narg(q) OR phone LIKE '%' || sqlc.narg(q) || '%' OR nickname ILIKE '%' || sqlc.narg(q) || '%')
  AND (sqlc.narg(plan)::text IS NULL OR plan_code = sqlc.narg(plan))
  AND (sqlc.narg(ui_language)::text IS NULL OR ui_language = sqlc.narg(ui_language))
  AND (sqlc.narg(status)::text IS NULL
       OR (sqlc.narg(status) = 'disabled' AND disabled_at IS NOT NULL)
       OR (sqlc.narg(status) = 'active' AND disabled_at IS NULL))
ORDER BY created_at DESC
LIMIT $1 OFFSET $2;

-- name: AdminSetUserPlan :one
UPDATE users SET plan_code = $2, plan_expires_at = $3, updated_at = now() WHERE id = $1
RETURNING *;

-- name: AdminSetUserDisabled :one
UPDATE users SET
    disabled_at = CASE WHEN sqlc.arg(disabled)::bool THEN coalesce(disabled_at, now()) ELSE NULL END,
    disabled_reason = sqlc.arg(reason),
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: UpsertQuotaOverride :one
INSERT INTO user_quota_overrides (user_id, subscriptions, tokens_per_month, note, updated_by)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (user_id) DO UPDATE SET
    subscriptions = excluded.subscriptions, tokens_per_month = excluded.tokens_per_month,
    note = excluded.note, updated_by = excluded.updated_by, updated_at = now()
RETURNING *;

-- name: DeleteQuotaOverride :exec
DELETE FROM user_quota_overrides WHERE user_id = $1;

-- name: UserCostSince :one
SELECT coalesce(sum(cost_micros), 0)::bigint AS cost_micros, count(DISTINCT task_id)::int AS tasks
FROM model_usage WHERE user_id = $1 AND created_at >= $2;

-- name: SetSubscriptionsPausedFor :many
-- Pauses a suspended user's active subscriptions; returns them so their schedules can be paused too.
UPDATE briefing_subscriptions SET status = 'paused', paused_reason = 'account_disabled', next_run_at = NULL, updated_at = now()
WHERE user_id = $1 AND status = 'active'
RETURNING *;

-- name: ResumeSubscriptionsFor :many
-- Undoes SetSubscriptionsPausedFor; the caller sets next_run_at and resumes schedules.
UPDATE briefing_subscriptions SET status = 'active', paused_reason = NULL, updated_at = now()
WHERE user_id = $1 AND status = 'paused' AND paused_reason = 'account_disabled'
RETURNING *;

-- name: SetSubscriptionNextRun :exec
UPDATE briefing_subscriptions SET next_run_at = $2 WHERE id = $1;

-- ------------------------------------------------------------------ templates

-- name: AdminListTemplates :many
SELECT t.*, (SELECT count(*) FROM briefing_subscriptions s WHERE s.template_id = t.id)::int AS subscription_count
FROM templates t
WHERE ((sqlc.arg(scope)::text = 'official' AND t.owner_id IS NULL) OR (sqlc.arg(scope)::text = 'custom' AND t.owner_id IS NOT NULL))
  AND (sqlc.narg(q)::text IS NULL OR t.name ILIKE '%' || sqlc.narg(q) || '%' OR t.description ILIKE '%' || sqlc.narg(q) || '%')
ORDER BY CASE WHEN sqlc.arg(scope)::text = 'official' THEN t.sort_order END, t.updated_at DESC
LIMIT $1 OFFSET $2;

-- name: AdminGetTemplate :one
SELECT * FROM templates WHERE id = $1;

-- name: AdminCreateOfficialTemplate :one
INSERT INTO templates (slug, name, description, category, icon, fields, sections, sources,
                       default_schedule, default_length, required_connectors, published, sort_order, translations, admin_managed)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, true)
RETURNING *;

-- name: AdminUpdateTemplate :one
UPDATE templates SET
    name = $2, description = $3, category = $4, icon = $5, fields = $6, sections = $7, sources = $8,
    default_schedule = $9, default_length = $10, required_connectors = $11, published = $12, sort_order = $13,
    translations = $14,
    version = CASE WHEN (fields, sections, sources) IS DISTINCT FROM ($6::jsonb, $7::jsonb, $8::jsonb)
                   THEN version + 1 ELSE version END,
    admin_managed = true, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: AdminDeleteTemplate :execrows
DELETE FROM templates WHERE id = $1;

-- ------------------------------------------------------------------ connectors

-- name: AdminListConnectors :many
SELECT c.*,
       (SELECT count(*) FROM user_connectors uc WHERE uc.connector_id = c.id AND uc.enabled)::int      AS enabled_users,
       (SELECT count(*) FROM connector_credentials cc WHERE cc.connector_id = c.id)::int              AS authorized_users
FROM connectors c
ORDER BY c.sort_order, c.id;

-- name: AdminUpdateConnector :one
UPDATE connectors SET
    name = coalesce(sqlc.narg(name), name),
    description = coalesce(sqlc.narg(description), description),
    category = coalesce(sqlc.narg(category), category),
    available = coalesce(sqlc.narg(available), available),
    default_on = coalesce(sqlc.narg(default_on), default_on),
    sort_order = coalesce(sqlc.narg(sort_order), sort_order),
    admin_managed = true
WHERE id = $1
RETURNING *;

-- ------------------------------------------------------------------ moderation

-- name: AdminListModerationTerms :many
SELECT * FROM moderation_terms ORDER BY id DESC;

-- name: CreateModerationTerm :one
INSERT INTO moderation_terms (term, note, created_by) VALUES ($1, $2, $3)
RETURNING *;

-- name: DeleteModerationTerm :execrows
DELETE FROM moderation_terms WHERE id = $1;

-- name: ListModerationEvents :many
SELECT e.*, u.nickname, u.phone
FROM moderation_events e LEFT JOIN users u ON u.id = e.user_id
WHERE (sqlc.narg(kind)::text IS NULL OR e.kind = sqlc.narg(kind))
  AND (sqlc.narg(user_id)::uuid IS NULL OR e.user_id = sqlc.narg(user_id))
ORDER BY e.id DESC
LIMIT $1 OFFSET $2;

-- name: AdminListShares :many
SELECT s.*, coalesce(r.title, b.title, '')::text AS target_title, u.nickname, u.phone
FROM shares s
LEFT JOIN reports r ON r.id = s.report_id
LEFT JOIN briefings b ON b.id = s.briefing_id
JOIN users u ON u.id = s.user_id
WHERE (NOT sqlc.arg(active_only)::bool OR (s.revoked_at IS NULL AND (s.expires_at IS NULL OR s.expires_at > now())))
  AND (sqlc.narg(target_type)::text IS NULL
       OR (sqlc.narg(target_type) = 'report' AND s.report_id IS NOT NULL)
       OR (sqlc.narg(target_type) = 'briefing' AND s.briefing_id IS NOT NULL))
ORDER BY s.created_at DESC
LIMIT $1 OFFSET $2;

-- name: AdminShareTotals :one
-- Links created in the window and what they have collected so far.
SELECT count(*)::int AS created,
       count(*) FILTER (WHERE briefing_id IS NOT NULL)::int AS briefing_shares,
       count(DISTINCT briefing_id)::int AS briefings_shared,
       coalesce(sum(view_count), 0)::int AS views,
       coalesce(sum(wechat_view_count), 0)::int AS wechat_views,
       coalesce(sum(cta_clicks), 0)::int AS cta_clicks
FROM shares WHERE created_at >= $1;

-- name: AdminShareChannels :many
SELECT channel, count(*)::int AS shares FROM shares WHERE created_at >= $1
GROUP BY channel ORDER BY shares DESC;

-- name: CountBriefingsReadySince :one
SELECT count(*)::int FROM briefings WHERE status = 'ready' AND generated_at >= $1;

-- name: AdminRevokeShare :execrows
UPDATE shares SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL;

-- name: AdminGetReport :one
SELECT * FROM reports WHERE id = $1;

-- ------------------------------------------------------------------ tasks & cost

-- name: AdminListTasks :many
SELECT t.*, u.nickname, u.phone,
       coalesce((SELECT sum(cost_micros) FROM model_usage m WHERE m.task_id = t.id), 0)::bigint AS cost_micros
FROM tasks t JOIN users u ON u.id = t.user_id
WHERE (sqlc.narg(status)::text IS NULL OR t.status = sqlc.narg(status))
  AND (sqlc.narg(kind)::text IS NULL OR t.kind = sqlc.narg(kind))
  AND (sqlc.narg(user_id)::uuid IS NULL OR t.user_id = sqlc.narg(user_id))
ORDER BY t.created_at DESC
LIMIT $1 OFFSET $2;

-- name: AdminGetTask :one
SELECT * FROM tasks WHERE id = $1;

-- name: ListTaskUsage :many
SELECT * FROM model_usage WHERE task_id = $1 ORDER BY id;

-- name: CostByModel :many
SELECT provider, model, tier, count(*)::int AS calls,
       coalesce(sum(input_tokens), 0)::bigint AS input_tokens, coalesce(sum(output_tokens), 0)::bigint AS output_tokens,
       coalesce(sum(cost_micros), 0)::bigint AS cost_micros, coalesce(sum(billed_tokens), 0)::bigint AS billed_tokens
FROM model_usage WHERE created_at >= $1
GROUP BY provider, model, tier ORDER BY cost_micros DESC;

-- name: CostByTaskKind :many
-- Average cost per finished task of each kind (the gate-B target: report ≤ ¥3).
SELECT t.kind, count(DISTINCT t.id)::int AS tasks, coalesce(sum(m.cost_micros), 0)::bigint AS cost_micros
FROM tasks t LEFT JOIN model_usage m ON m.task_id = t.id
WHERE t.created_at >= $1 AND t.status = 'done'
GROUP BY t.kind ORDER BY t.kind;

-- name: CostTopUsers :many
SELECT m.user_id, u.nickname, u.phone, coalesce(sum(m.cost_micros), 0)::bigint AS cost_micros,
       count(DISTINCT m.task_id)::int AS tasks
FROM model_usage m JOIN users u ON u.id = m.user_id
WHERE m.created_at >= $1
GROUP BY m.user_id, u.nickname, u.phone ORDER BY cost_micros DESC
LIMIT $2;

-- name: UsageByProviderSince :many
SELECT provider, count(*)::int AS calls, coalesce(sum(cost_micros), 0)::bigint AS cost_micros, max(created_at)::timestamptz AS last_call_at
FROM model_usage WHERE created_at >= $1
GROUP BY provider ORDER BY provider;

-- name: InsertQuotaGrant :one
INSERT INTO quota_grants (user_id, tokens, reason, granted_by) VALUES ($1, $2, $3, $4) RETURNING *;

-- name: ListQuotaGrantsSince :many
SELECT * FROM quota_grants WHERE user_id = $1 AND created_at >= $2 ORDER BY created_at DESC;

-- name: BilledTokensByUserSince :many
-- Each user's counted billed tokens since the start of the quota month (failed tasks excluded).
SELECT m.user_id, coalesce(sum(m.billed_tokens), 0)::bigint AS tokens
FROM model_usage m LEFT JOIN tasks t ON t.id = m.task_id
WHERE m.created_at >= $1 AND m.user_id IS NOT NULL AND (t.status IS NULL OR t.status <> 'failed')
GROUP BY m.user_id;
