package adminapi_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
)

func TestAnalytics(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	h.createOwner("owner@zeus.test", "correct-horse-battery")
	h.login("owner@zeus.test", "correct-horse-battery")
	cst := time.FixedZone("CST", 8*3600)
	now := time.Now().In(cst)
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, cst)
	day := func(n int) time.Time { return today.AddDate(0, 0, -n) }
	ds := func(n int) string { return day(n).Format("2006-01-02") }
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := h.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	user := func(phone string, signup time.Time) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := h.pool.QueryRow(ctx, `INSERT INTO users (nickname, phone, created_at) VALUES ('u', $1, $2) RETURNING id`, phone, signup).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	active := func(id uuid.UUID, n int, platform string) {
		exec(`INSERT INTO user_daily_activity (user_id, date, platform) VALUES ($1, $2::date, $3) ON CONFLICT DO NOTHING`, id, ds(n), platform)
	}

	// A: signed up 40 days ago on iOS, came back on day 30, has a briefing and a rated report.
	a := user("+8613800000001", day(40))
	active(a, 40, "ios")
	active(a, 10, "ios")
	// B: signed up 40 days ago on Android, never came back.
	b := user("+8613800000002", day(40))
	active(b, 40, "android")
	// C: signed up today on iOS.
	c := user("+8613800000003", day(0))
	active(c, 0, "ios")

	var tplID uuid.UUID
	if err := h.pool.QueryRow(ctx, `SELECT id FROM templates WHERE owner_id IS NULL ORDER BY sort_order LIMIT 1`).Scan(&tplID); err != nil {
		t.Fatal(err)
	}
	var subID, briefingID, taskID, reportID uuid.UUID
	if err := h.pool.QueryRow(ctx, `INSERT INTO briefing_subscriptions (user_id, template_id, name, schedule, status)
		VALUES ($1, $2, '早报', '{}', 'active') RETURNING id`, a, tplID).Scan(&subID); err != nil {
		t.Fatal(err)
	}
	if err := h.pool.QueryRow(ctx, `INSERT INTO briefings (user_id, subscription_id, title, date, status, generated_at, read_at, created_at)
		VALUES ($1, $2, '早报', $3::date, 'ready', $4, $5, $4) RETURNING id`, a, subID, ds(1), day(1), day(1).Add(30*time.Minute)).Scan(&briefingID); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO briefing_feedback (briefing_id, user_id, rating) VALUES ($1, $2, 'useful')`, briefingID, a)
	if err := h.pool.QueryRow(ctx, `INSERT INTO tasks (user_id, kind, status, created_at, started_at, finished_at)
		VALUES ($1, 'report', 'done', $2, $2, $3) RETURNING id`, a, day(2), day(2).Add(5*time.Minute)).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if err := h.pool.QueryRow(ctx, `INSERT INTO reports (user_id, task_id, title, topic, status, source_count, created_at, completed_at)
		VALUES ($1, $2, '新能源', '新能源', 'done', 12, $3, $4) RETURNING id`, a, taskID, day(2), day(2).Add(5*time.Minute)).Scan(&reportID); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE tasks SET report_id = $1 WHERE id = $2`, reportID, taskID)
	exec(`INSERT INTO model_usage (user_id, task_id, provider, model, tier, input_tokens, output_tokens, cost_micros, billed_tokens, created_at)
		VALUES ($1, $2, 'qwen', 'qwen-max', 'strong', 1000, 500, 2500000, 1500, $3)`, a, taskID, day(2))
	exec(`INSERT INTO report_feedback (report_id, user_id, rating) VALUES ($1, $2, 'satisfied')`, reportID, a)
	exec(`INSERT INTO tasks (user_id, kind, status, report_id, created_at) VALUES ($1, 'followup', 'done', $2, $3)`, a, reportID, day(2))
	exec(`INSERT INTO tasks (user_id, kind, status, error, created_at) VALUES ($1, 'report', 'failed', '{"code":"upstream","message":"模型超时"}', $2)`, c, day(0))

	// ---- overview
	var ov gen.AnalyticsOverview
	h.do("GET", "/admin/v1/analytics/overview", nil, 200, &ov)
	d30 := ov.GateB.D30Retention
	if d30.Eligible != 2 || d30.Retained != 1 || d30.Rate == nil || *d30.Rate != 0.5 {
		t.Fatalf("d30 = %+v", d30)
	}
	if ov.Kpis.Dau.Value != 1 || ov.Kpis.NewUsers.Value != 1 || ov.Kpis.Mau.Value != 2 {
		t.Fatalf("kpis = %+v", ov.Kpis)
	}
	if ov.GateB.ReportSatisfaction.Satisfied != 1 || ov.GateB.ReportCost.AvgCny == nil || *ov.GateB.ReportCost.AvgCny != 2.5 {
		t.Fatalf("gate B = %+v", ov.GateB)
	}
	if ov.AtRisk.Users != 1 || ov.AtRisk.Briefings != 1 || ov.Todo.FailedTasks24h != 1 || len(ov.Daily) != 30 {
		t.Fatalf("atRisk %+v todo %+v daily %d", ov.AtRisk, ov.Todo, len(ov.Daily))
	}
	var ovAndroid gen.AnalyticsOverview
	h.do("GET", "/admin/v1/analytics/overview?platform=android", nil, 200, &ovAndroid)
	if ovAndroid.GateB.D30Retention.Eligible != 1 || ovAndroid.GateB.D30Retention.Retained != 0 || ovAndroid.Kpis.Dau.Value != 0 {
		t.Fatalf("android overview = %+v", ovAndroid.GateB.D30Retention)
	}

	// ---- growth
	var g gen.AnalyticsGrowth
	h.do("GET", fmt.Sprintf("/admin/v1/analytics/growth?from=%s&to=%s", ds(45), ds(0)), nil, 200, &g)
	if g.Current.NewUsers != 3 || g.Current.ActiveUsers != 3 || len(g.Daily) != 46 || g.Daily[len(g.Daily)-1].TotalUsers != 3 ||
		g.Daily[5].NewPhone != 2 {
		t.Fatalf("growth = %+v", g.Current)
	}
	if g.Funnel.Registered != 3 || g.Funnel.Subscribed != 1 || g.Funnel.ReadBriefing != 1 || g.Funnel.StartedReport != 1 || g.Funnel.Week2Eligible != 2 {
		t.Fatalf("funnel = %+v", g.Funnel)
	}
	if len(g.Platforms) != 2 || g.Platforms[0].Key != "ios" || g.Platforms[0].Count != 2 {
		t.Fatalf("platforms = %+v", g.Platforms)
	}
	h.do("GET", "/admin/v1/analytics/growth?from=2026-01-02&to=2026-01-01", nil, 400, nil)

	// ---- retention
	var rt gen.AnalyticsRetention
	h.do("GET", fmt.Sprintf("/admin/v1/analytics/retention?from=%s", ds(45)), nil, 200, &rt)
	total, d30r := 0, 0
	for _, c := range rt.Cohorts {
		total += c.Users
		d30r += c.D30.Retained
	}
	if total != 3 || d30r != 1 {
		t.Fatalf("retention = %+v", rt.Cohorts)
	}

	// ---- engagement
	var e gen.AnalyticsEngagement
	h.do("GET", "/admin/v1/analytics/engagement", nil, 200, &e)
	if e.Briefings.Generated != 1 || e.Briefings.Read != 1 || e.Briefings.Useful != 1 || e.Briefings.ActiveSubscriptions != 1 ||
		e.Briefings.MedianReadDelayMinutes != 30 {
		t.Fatalf("briefings = %+v", e.Briefings)
	}
	if e.Reports.Started != 1 || e.Reports.Completed != 1 || e.Reports.Followups != 1 || e.Reports.Satisfied != 1 || e.Reports.AvgDurationSeconds != 300 {
		t.Fatalf("reports = %+v", e.Reports)
	}
	if len(e.Templates) == 0 || e.Templates[0].ActiveSubscriptions != 1 || e.Templates[0].Read != 1 || len(e.BriefingsDaily) != 30 {
		t.Fatalf("templates = %+v", e.Templates[:1])
	}

	// ---- costs
	var co gen.AnalyticsCosts
	h.do("GET", "/admin/v1/analytics/costs", nil, 200, &co)
	if co.Current.CostCny != 2.5 || co.Current.ActiveUsers != 2 || co.Current.CostPerActiveUserCny == nil || *co.Current.CostPerActiveUserCny != 1.25 {
		t.Fatalf("costs = %+v", co.Current)
	}
	if len(co.UnitCosts) == 0 || co.Quota.DefaultLimit != 7_500_000 {
		t.Fatalf("units = %+v quota = %+v", co.UnitCosts, co.Quota)
	}

	// ---- quality
	var qu gen.AnalyticsQuality
	h.do("GET", "/admin/v1/analytics/quality", nil, 200, &qu)
	if len(qu.Errors) != 1 || qu.Errors[0].Code != "upstream" || len(qu.Kinds) == 0 {
		t.Fatalf("quality = %+v", qu)
	}
}
