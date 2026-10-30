-- Operations analytics for the admin console. Days are Beijing days; "active" means the
-- App called the api that day (user_daily_activity). platform (ios | android) filters by
-- each user's most recent platform (view user_platform); NULL means all users.

-- name: AnalyticsDaily :many
WITH days AS (
    SELECT d::date AS date FROM generate_series(@from_date::date, @to_date::date, interval '1 day') d
), pu AS (
    SELECT u.id, (u.created_at AT TIME ZONE 'Asia/Shanghai')::date AS signup_date,
        CASE WHEN u.phone IS NOT NULL THEN 'phone' WHEN u.apple_sub IS NOT NULL THEN 'apple'
             WHEN u.wechat_unionid IS NOT NULL THEN 'wechat' ELSE 'other' END AS method
    FROM users u LEFT JOIN user_platform p ON p.user_id = u.id
    WHERE sqlc.narg(platform)::text IS NULL OR p.platform = sqlc.narg(platform)
), act AS (
    SELECT a.date, count(*) AS n FROM user_daily_activity a JOIN pu ON pu.id = a.user_id
    WHERE a.date BETWEEN @from_date::date AND @to_date::date GROUP BY a.date
), sig AS (
    SELECT signup_date AS date, count(*) AS n,
        count(*) FILTER (WHERE method = 'phone') AS phone, count(*) FILTER (WHERE method = 'apple') AS apple,
        count(*) FILTER (WHERE method = 'wechat') AS wechat, count(*) FILTER (WHERE method = 'other') AS other
    FROM pu GROUP BY signup_date
)
SELECT days.date,
    coalesce(act.n, 0)::int AS active_users,
    coalesce(sig.n, 0)::int AS new_users,
    coalesce(sig.phone, 0)::int AS new_phone,
    coalesce(sig.apple, 0)::int AS new_apple,
    coalesce(sig.wechat, 0)::int AS new_wechat,
    coalesce(sig.other, 0)::int AS new_other,
    (SELECT count(*) FROM pu WHERE pu.signup_date <= days.date)::int AS total_users
FROM days LEFT JOIN act ON act.date = days.date LEFT JOIN sig ON sig.date = days.date
ORDER BY days.date;

-- name: AnalyticsActiveUsers :one
SELECT count(DISTINCT a.user_id)::int
FROM user_daily_activity a LEFT JOIN user_platform p ON p.user_id = a.user_id
WHERE a.date BETWEEN @from_date::date AND @to_date::date
  AND (sqlc.narg(platform)::text IS NULL OR p.platform = sqlc.narg(platform));

-- name: AnalyticsNewUsers :one
SELECT count(*)::int
FROM users u LEFT JOIN user_platform p ON p.user_id = u.id
WHERE (u.created_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN @from_date::date AND @to_date::date
  AND (sqlc.narg(platform)::text IS NULL OR p.platform = sqlc.narg(platform));

-- name: AnalyticsBreakdown :many
-- Active users in the range by platform, App version and interface language.
WITH a AS (
    SELECT DISTINCT ON (a.user_id) a.user_id, a.platform, a.app_version
    FROM user_daily_activity a
    WHERE a.date BETWEEN @from_date::date AND @to_date::date
    ORDER BY a.user_id, a.date DESC
), f AS (
    SELECT a.*, coalesce(p.platform, '') AS last_platform, u.ui_language
    FROM a JOIN users u ON u.id = a.user_id LEFT JOIN user_platform p ON p.user_id = a.user_id
    WHERE sqlc.narg(platform)::text IS NULL OR p.platform = sqlc.narg(platform)
)
SELECT 'platform'::text AS dim, coalesce(nullif(last_platform, ''), 'unknown')::text AS key, count(*)::int AS users FROM f GROUP BY 2
UNION ALL
SELECT 'version'::text, coalesce(nullif(app_version, ''), 'unknown')::text, count(*)::int FROM f GROUP BY 2
UNION ALL
SELECT 'language'::text, coalesce(nullif(ui_language, ''), 'unknown')::text, count(*)::int FROM f GROUP BY 2;

-- name: AnalyticsFunnel :one
-- Activation of users who signed up in the range. Week 2 = opened the App on days 7–13 after
-- sign-up; only users whose day 13 is over are eligible.
WITH c AS (
    SELECT u.id, (u.created_at AT TIME ZONE 'Asia/Shanghai')::date AS signup_date
    FROM users u LEFT JOIN user_platform p ON p.user_id = u.id
    WHERE (u.created_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN @from_date::date AND @to_date::date
      AND (sqlc.narg(platform)::text IS NULL OR p.platform = sqlc.narg(platform))
)
SELECT
    count(*)::int AS registered,
    count(*) FILTER (WHERE EXISTS (SELECT 1 FROM briefing_subscriptions s WHERE s.user_id = c.id))::int AS subscribed,
    count(*) FILTER (WHERE EXISTS (SELECT 1 FROM briefings b WHERE b.user_id = c.id AND b.read_at IS NOT NULL))::int AS read_briefing,
    count(*) FILTER (WHERE EXISTS (SELECT 1 FROM reports r WHERE r.user_id = c.id))::int AS started_report,
    count(*) FILTER (WHERE c.signup_date + 13 < @today::date)::int AS week2_eligible,
    count(*) FILTER (WHERE c.signup_date + 13 < @today::date AND EXISTS (
        SELECT 1 FROM user_daily_activity a WHERE a.user_id = c.id AND a.date BETWEEN c.signup_date + 7 AND c.signup_date + 13))::int AS week2_active
FROM c;

-- name: AnalyticsRetention :many
-- Weekly sign-up cohorts (weeks start Monday). dN = opened the App exactly N days after
-- sign-up; d30 = on any of days 29–31 (gate B). *_eligible counts users whose day is over.
WITH c AS (
    SELECT u.id, (u.created_at AT TIME ZONE 'Asia/Shanghai')::date AS signup_date
    FROM users u LEFT JOIN user_platform p ON p.user_id = u.id
    WHERE (u.created_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN @from_date::date AND @to_date::date
      AND (sqlc.narg(platform)::text IS NULL OR p.platform = sqlc.narg(platform))
), x AS (
    SELECT c.id, c.signup_date, date_trunc('week', c.signup_date)::date AS week,
        array(SELECT a.date - c.signup_date FROM user_daily_activity a
              WHERE a.user_id = c.id AND a.date BETWEEN c.signup_date + 1 AND c.signup_date + 31)::int[] AS days
    FROM c
)
SELECT week,
    count(*)::int AS users,
    count(*) FILTER (WHERE signup_date + 1 < @today::date)::int AS d1_eligible,
    count(*) FILTER (WHERE signup_date + 1 < @today::date AND 1 = ANY(days))::int AS d1,
    count(*) FILTER (WHERE signup_date + 3 < @today::date)::int AS d3_eligible,
    count(*) FILTER (WHERE signup_date + 3 < @today::date AND 3 = ANY(days))::int AS d3,
    count(*) FILTER (WHERE signup_date + 7 < @today::date)::int AS d7_eligible,
    count(*) FILTER (WHERE signup_date + 7 < @today::date AND 7 = ANY(days))::int AS d7,
    count(*) FILTER (WHERE signup_date + 14 < @today::date)::int AS d14_eligible,
    count(*) FILTER (WHERE signup_date + 14 < @today::date AND 14 = ANY(days))::int AS d14,
    count(*) FILTER (WHERE signup_date + 31 < @today::date)::int AS d30_eligible,
    count(*) FILTER (WHERE signup_date + 31 < @today::date AND days && ARRAY[29, 30, 31])::int AS d30
FROM x GROUP BY week ORDER BY week;

-- name: AnalyticsAtRisk :one
-- Users whose scheduled briefings keep running although they have not opened the App since
-- since_date: what they cost over the same days.
WITH r AS (
    SELECT DISTINCT s.user_id
    FROM briefing_subscriptions s JOIN users u ON u.id = s.user_id LEFT JOIN user_platform p ON p.user_id = s.user_id
    WHERE s.status = 'active' AND u.disabled_at IS NULL
      AND (sqlc.narg(platform)::text IS NULL OR p.platform = sqlc.narg(platform))
      AND NOT EXISTS (SELECT 1 FROM user_daily_activity a WHERE a.user_id = s.user_id AND a.date >= @since_date::date)
)
SELECT
    (SELECT count(*) FROM r)::int AS users,
    (SELECT count(*) FROM briefings b WHERE b.user_id IN (SELECT user_id FROM r) AND b.created_at >= @since)::int AS briefings,
    (SELECT coalesce(sum(m.cost_micros), 0) FROM model_usage m WHERE m.user_id IN (SELECT user_id FROM r) AND m.created_at >= @since)::bigint AS cost_micros;

-- name: AnalyticsBriefings :one
WITH pu AS (
    SELECT u.id FROM users u LEFT JOIN user_platform p ON p.user_id = u.id
    WHERE sqlc.narg(platform)::text IS NULL OR p.platform = sqlc.narg(platform)
), b AS (
    SELECT b.* FROM briefings b
    WHERE b.user_id IN (SELECT id FROM pu)
      AND (b.created_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN @from_date::date AND @to_date::date
)
SELECT
    (SELECT count(*) FROM briefing_subscriptions s WHERE s.status = 'active' AND s.user_id IN (SELECT id FROM pu))::int AS active_subscriptions,
    (SELECT count(*) FROM briefing_subscriptions s WHERE s.status = 'paused' AND s.user_id IN (SELECT id FROM pu))::int AS paused_subscriptions,
    (SELECT count(*) FROM briefing_subscriptions s WHERE s.user_id IN (SELECT id FROM pu)
        AND (s.created_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN @from_date::date AND @to_date::date)::int AS new_subscriptions,
    (SELECT count(*) FROM b)::int AS generated,
    (SELECT count(*) FROM b WHERE status = 'ready')::int AS ready,
    (SELECT count(*) FROM b WHERE status = 'failed')::int AS failed,
    (SELECT count(*) FROM b WHERE read_at IS NOT NULL)::int AS read,
    (SELECT coalesce(percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM read_at - generated_at) / 60), 0)
        FROM b WHERE read_at IS NOT NULL AND generated_at IS NOT NULL AND read_at >= generated_at)::float8 AS median_read_delay_minutes,
    (SELECT count(*) FROM briefing_feedback f WHERE f.item_id = '' AND f.rating = 'useful' AND f.briefing_id IN (SELECT id FROM b))::int AS useful,
    (SELECT count(*) FROM briefing_feedback f WHERE f.item_id = '' AND f.rating = 'meh' AND f.briefing_id IN (SELECT id FROM b))::int AS meh,
    (SELECT count(*) FROM shares s WHERE s.briefing_id IN (SELECT id FROM b))::int AS shared;

-- name: AnalyticsBriefingsDaily :many
SELECT (b.created_at AT TIME ZONE 'Asia/Shanghai')::date AS date,
    count(*)::int AS generated, count(*) FILTER (WHERE b.read_at IS NOT NULL)::int AS read
FROM briefings b LEFT JOIN user_platform p ON p.user_id = b.user_id
WHERE (b.created_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN @from_date::date AND @to_date::date
  AND (sqlc.narg(platform)::text IS NULL OR p.platform = sqlc.narg(platform))
GROUP BY 1 ORDER BY 1;

-- name: AnalyticsTemplates :many
-- Official templates by active subscriptions, with how their briefings in the range fared.
WITH pu AS (
    SELECT u.id FROM users u LEFT JOIN user_platform p ON p.user_id = u.id
    WHERE sqlc.narg(platform)::text IS NULL OR p.platform = sqlc.narg(platform)
), s AS (
    SELECT s.* FROM briefing_subscriptions s WHERE s.user_id IN (SELECT id FROM pu)
), b AS (
    SELECT b.id, b.subscription_id, b.read_at FROM briefings b
    WHERE b.subscription_id IN (SELECT id FROM s)
      AND (b.created_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN @from_date::date AND @to_date::date
)
SELECT t.id, t.name,
    (SELECT count(*) FROM s WHERE s.template_id = t.id AND s.status = 'active')::int AS active_subscriptions,
    (SELECT count(*) FROM s WHERE s.template_id = t.id
        AND (s.created_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN @from_date::date AND @to_date::date)::int AS new_subscriptions,
    (SELECT count(*) FROM b JOIN s ON s.id = b.subscription_id WHERE s.template_id = t.id)::int AS briefings,
    (SELECT count(*) FROM b JOIN s ON s.id = b.subscription_id WHERE s.template_id = t.id AND b.read_at IS NOT NULL)::int AS read,
    (SELECT count(*) FROM briefing_feedback f JOIN b ON b.id = f.briefing_id JOIN s ON s.id = b.subscription_id
        WHERE s.template_id = t.id AND f.item_id = '' AND f.rating = 'useful')::int AS useful,
    (SELECT count(*) FROM briefing_feedback f JOIN b ON b.id = f.briefing_id JOIN s ON s.id = b.subscription_id
        WHERE s.template_id = t.id AND f.item_id = '' AND f.rating = 'meh')::int AS meh
FROM templates t
WHERE t.owner_id IS NULL
ORDER BY active_subscriptions DESC, t.sort_order, t.name;

-- name: AnalyticsReports :one
WITH r AS (
    SELECT r.* FROM reports r LEFT JOIN user_platform p ON p.user_id = r.user_id
    WHERE (r.created_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN @from_date::date AND @to_date::date
      AND (sqlc.narg(platform)::text IS NULL OR p.platform = sqlc.narg(platform))
)
SELECT
    (SELECT count(*) FROM r)::int AS started,
    (SELECT count(*) FROM r WHERE status = 'done')::int AS completed,
    (SELECT count(*) FROM r WHERE status = 'failed')::int AS failed,
    (SELECT count(*) FROM r WHERE status = 'cancelled')::int AS cancelled,
    (SELECT coalesce(avg(extract(epoch FROM completed_at - created_at)), 0) FROM r WHERE status = 'done' AND completed_at IS NOT NULL)::float8 AS avg_duration_seconds,
    (SELECT coalesce(avg(source_count), 0) FROM r WHERE status = 'done')::float8 AS avg_sources,
    (SELECT count(*) FROM tasks t WHERE t.kind = 'followup' AND t.report_id IN (SELECT id FROM r))::int AS followups,
    (SELECT count(DISTINCT t.report_id) FROM tasks t WHERE t.kind = 'followup' AND t.report_id IN (SELECT id FROM r))::int AS reports_with_followup,
    (SELECT count(*) FROM r WHERE pdf_status = 'ready')::int AS pdf_exports,
    (SELECT count(DISTINCT s.report_id) FROM shares s WHERE s.report_id IN (SELECT id FROM r))::int AS shared,
    (SELECT count(*) FROM report_feedback f WHERE f.report_id IN (SELECT id FROM r) AND f.rating = 'satisfied')::int AS satisfied,
    (SELECT count(*) FROM report_feedback f WHERE f.report_id IN (SELECT id FROM r) AND f.rating = 'unsatisfied')::int AS unsatisfied;

-- name: AnalyticsReportGroups :many
-- Depth mix and unsatisfied reasons of reports created in the range.
WITH r AS (
    SELECT r.* FROM reports r LEFT JOIN user_platform p ON p.user_id = r.user_id
    WHERE (r.created_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN @from_date::date AND @to_date::date
      AND (sqlc.narg(platform)::text IS NULL OR p.platform = sqlc.narg(platform))
)
SELECT 'depth'::text AS dim, depth::text AS key, count(*)::int AS n FROM r GROUP BY depth
UNION ALL
SELECT 'reason'::text, reason::text, count(*)::int
FROM report_feedback f JOIN r ON r.id = f.report_id, unnest(f.reasons) AS reason
WHERE f.rating = 'unsatisfied' GROUP BY reason;

-- name: AnalyticsReportsDaily :many
SELECT (r.created_at AT TIME ZONE 'Asia/Shanghai')::date AS date,
    count(*)::int AS started, count(*) FILTER (WHERE r.status = 'done')::int AS completed
FROM reports r LEFT JOIN user_platform p ON p.user_id = r.user_id
WHERE (r.created_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN @from_date::date AND @to_date::date
  AND (sqlc.narg(platform)::text IS NULL OR p.platform = sqlc.narg(platform))
GROUP BY 1 ORDER BY 1;

-- name: AnalyticsPush :one
-- Reach: users with a push device and briefing notifications on, among users active in the
-- range; notifications created in the range and how many were pushed and opened.
WITH act AS (
    SELECT DISTINCT a.user_id FROM user_daily_activity a LEFT JOIN user_platform p ON p.user_id = a.user_id
    WHERE a.date BETWEEN @from_date::date AND @to_date::date
      AND (sqlc.narg(platform)::text IS NULL OR p.platform = sqlc.narg(platform))
), m AS (
    SELECT m.* FROM messages m
    WHERE m.user_id IN (SELECT user_id FROM act)
      AND (m.created_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN @from_date::date AND @to_date::date
)
SELECT
    (SELECT count(*) FROM act)::int AS active_users,
    (SELECT count(*) FROM act WHERE EXISTS (SELECT 1 FROM devices d WHERE d.user_id = act.user_id))::int AS with_device,
    (SELECT count(*) FROM act WHERE EXISTS (SELECT 1 FROM devices d WHERE d.user_id = act.user_id)
        AND coalesce((SELECT np.briefing FROM notification_prefs np WHERE np.user_id = act.user_id), true))::int AS briefing_push_on,
    (SELECT count(*) FROM act WHERE coalesce((SELECT np.marketing FROM notification_prefs np WHERE np.user_id = act.user_id), false))::int AS marketing_opt_in,
    (SELECT count(*) FROM m)::int AS messages,
    (SELECT count(*) FROM m WHERE push_status = 'sent')::int AS pushed,
    (SELECT count(*) FROM m WHERE opened_at IS NOT NULL)::int AS opened;

-- name: AnalyticsPushByCategory :many
SELECT m.category, count(*)::int AS messages,
    count(*) FILTER (WHERE m.push_status = 'sent')::int AS pushed,
    count(*) FILTER (WHERE m.opened_at IS NOT NULL)::int AS opened,
    count(*) FILTER (WHERE m.read_at IS NOT NULL)::int AS read
FROM messages m LEFT JOIN user_platform p ON p.user_id = m.user_id
WHERE (m.created_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN @from_date::date AND @to_date::date
  AND (sqlc.narg(platform)::text IS NULL OR p.platform = sqlc.narg(platform))
GROUP BY m.category ORDER BY messages DESC;

-- name: AnalyticsUnitCosts :many
-- Model cost per finished briefing / report task (task kinds briefing, report, followup).
WITH t AS (
    SELECT t.id, t.kind, coalesce(sum(m.cost_micros), 0)::bigint AS cost
    FROM tasks t LEFT JOIN model_usage m ON m.task_id = t.id LEFT JOIN user_platform p ON p.user_id = t.user_id
    WHERE t.status = 'done'
      AND (t.created_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN @from_date::date AND @to_date::date
      AND (sqlc.narg(platform)::text IS NULL OR p.platform = sqlc.narg(platform))
    GROUP BY t.id, t.kind
)
SELECT kind, count(*)::int AS tasks, coalesce(sum(cost), 0)::bigint AS cost_micros,
    coalesce(percentile_cont(0.5) WITHIN GROUP (ORDER BY cost), 0)::float8 AS median_micros,
    coalesce(percentile_cont(0.9) WITHIN GROUP (ORDER BY cost), 0)::float8 AS p90_micros
FROM t GROUP BY kind ORDER BY kind;

-- name: AnalyticsCostTotal :one
SELECT coalesce(sum(m.cost_micros), 0)::bigint AS cost_micros, coalesce(sum(m.billed_tokens), 0)::bigint AS billed_tokens
FROM model_usage m LEFT JOIN user_platform p ON p.user_id = m.user_id
WHERE (m.created_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN @from_date::date AND @to_date::date
  AND (sqlc.narg(platform)::text IS NULL OR p.platform = sqlc.narg(platform));

-- name: AnalyticsQuotaUse :one
-- This quota month (Beijing calendar month): how far users with any usage got into their
-- allowance (override or default_limit, plus grants this month).
WITH u AS (
    SELECT m.user_id, sum(m.billed_tokens) AS used
    FROM model_usage m LEFT JOIN user_platform p ON p.user_id = m.user_id
    WHERE m.created_at >= @month_start AND m.user_id IS NOT NULL
      AND (sqlc.narg(platform)::text IS NULL OR p.platform = sqlc.narg(platform))
    GROUP BY m.user_id
), l AS (
    SELECT u.user_id, u.used,
        coalesce((SELECT o.tokens_per_month FROM user_quota_overrides o WHERE o.user_id = u.user_id), @default_limit::bigint)
        + coalesce((SELECT sum(g.tokens) FROM quota_grants g WHERE g.user_id = u.user_id AND g.created_at >= @month_start), 0) AS lim
    FROM u
)
SELECT
    count(*)::int AS users,
    count(*) FILTER (WHERE used >= lim * 0.5)::int AS over50,
    count(*) FILTER (WHERE used >= lim * 0.8)::int AS over80,
    count(*) FILTER (WHERE used >= lim)::int AS exhausted,
    coalesce(percentile_cont(0.5) WITHIN GROUP (ORDER BY used), 0)::float8 AS median_used,
    (SELECT coalesce(sum(g.tokens), 0) FROM quota_grants g WHERE g.created_at >= @month_start)::bigint AS granted_tokens,
    (SELECT count(DISTINCT g.user_id) FROM quota_grants g WHERE g.created_at >= @month_start)::int AS granted_users
FROM l;

-- name: AnalyticsTaskQuality :many
SELECT kind, count(*)::int AS total,
    count(*) FILTER (WHERE status = 'done')::int AS done,
    count(*) FILTER (WHERE status = 'failed')::int AS failed,
    count(*) FILTER (WHERE status = 'cancelled')::int AS cancelled,
    coalesce(percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM finished_at - started_at))
        FILTER (WHERE status = 'done' AND finished_at IS NOT NULL AND started_at IS NOT NULL), 0)::float8 AS p50_seconds,
    coalesce(percentile_cont(0.9) WITHIN GROUP (ORDER BY extract(epoch FROM finished_at - started_at))
        FILTER (WHERE status = 'done' AND finished_at IS NOT NULL AND started_at IS NOT NULL), 0)::float8 AS p90_seconds
FROM tasks
WHERE (created_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN @from_date::date AND @to_date::date
GROUP BY kind ORDER BY total DESC;

-- name: AnalyticsTaskErrors :many
SELECT kind, coalesce(error->>'code', '')::text AS code, coalesce(error->>'message', '')::text AS message, count(*)::int AS n
FROM tasks
WHERE status = 'failed'
  AND (created_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN @from_date::date AND @to_date::date
GROUP BY 1, 2, 3 ORDER BY n DESC LIMIT 10;

-- name: AnalyticsTodo :one
SELECT
    (SELECT count(*) FROM feedback fb WHERE fb.status IN ('new', 'in_progress'))::int AS open_feedback,
    (SELECT count(*) FROM campaigns c WHERE c.status = 'pending_approval')::int AS pending_campaigns,
    (SELECT count(*) FROM config_releases cr WHERE cr.status = 'pending')::int AS pending_releases,
    (SELECT count(*) FROM tasks t WHERE t.status = 'failed' AND t.created_at >= sqlc.arg(since)::timestamptz)::int AS failed_tasks;
