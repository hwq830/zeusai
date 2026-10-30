package adminapi

import (
	"context"
	"sort"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
)

// Operations analytics (the console's 数据 pages). Every number comes from tables the
// server already writes; "active" is user_daily_activity (the App called the api that day).

const maxAnalyticsDays = 366

type period struct{ from, to time.Time }

func (p period) days() int { return int(p.to.Sub(p.from).Hours()/24) + 1 }

// previous is the period of the same length just before p.
func (p period) previous() period {
	n := p.days()
	return period{p.from.AddDate(0, 0, -n), p.from.AddDate(0, 0, -1)}
}

// period resolves from/to (Beijing days, inclusive); default is the last defaultDays days.
func (s *Server) period(from, to *openapi_types.Date, defaultDays int) (period, error) {
	today := s.dayStart(0)
	loc := today.Location()
	p := period{to: today}
	if to != nil {
		p.to = time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, loc)
	}
	p.from = p.to.AddDate(0, 0, -(defaultDays - 1))
	if from != nil {
		p.from = time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
	}
	if p.from.After(p.to) {
		return p, apperr.Invalid("起始日不能晚于截止日")
	}
	if p.days() > maxAnalyticsDays {
		return p, apperr.Invalid("时间范围最长 366 天")
	}
	return p, nil
}

func platformArg[T ~string](p *T) *string {
	if p == nil || *p == "" {
		return nil
	}
	v := string(*p)
	return &v
}

func ratio(n, d int) *float64 {
	if d <= 0 {
		return nil
	}
	v := float64(n) / float64(d)
	return &v
}

func kpi(v float64, prev *float64) map[string]any {
	return map[string]any{"value": v, "previous": prev}
}

func f64(v float64) *float64 { return &v }

// weekStart is the Monday of t's week.
func weekStart(t time.Time) time.Time {
	wd := (int(t.Weekday()) + 6) % 7
	return t.AddDate(0, 0, -wd)
}

func keyCounts(rows map[string]int) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for k, n := range rows {
		out = append(out, map[string]any{"key": k, "count": n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i]["count"].(int) != out[j]["count"].(int) {
			return out[i]["count"].(int) > out[j]["count"].(int)
		}
		return out[i]["key"].(string) < out[j]["key"].(string)
	})
	return out
}

func (s *Server) activeUsers(ctx context.Context, p period, platform *string) (int, error) {
	n, err := s.Q.AnalyticsActiveUsers(ctx, db.AnalyticsActiveUsersParams{FromDate: p.from, ToDate: p.to, Platform: platform})
	if err != nil {
		return 0, apperr.Internal(err)
	}
	return int(n), nil
}

func (s *Server) atRisk(ctx context.Context, platform *string) (map[string]any, error) {
	since := s.dayStart(6)
	r, err := s.Q.AnalyticsAtRisk(ctx, db.AnalyticsAtRiskParams{Since: since, SinceDate: since, Platform: platform})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return map[string]any{"users": r.Users, "briefings": r.Briefings, "costCny": cny(r.CostMicros)}, nil
}

// ---------------------------------------------------------------- overview

func (s *Server) GetAnalyticsOverview(ctx context.Context, req gen.GetAnalyticsOverviewRequestObject) (gen.GetAnalyticsOverviewResponseObject, error) {
	pf := platformArg(req.Params.Platform)
	today := s.dayStart(0)
	day := func(n int) period { d := today.AddDate(0, 0, -n); return period{d, d} }
	last := func(n, skip int) period {
		return period{today.AddDate(0, 0, -(skip + n - 1)), today.AddDate(0, 0, -skip)}
	}

	counts := map[string]int{}
	for k, p := range map[string]period{"dau": day(0), "dau_prev": day(1), "wau": last(7, 0), "wau_prev": last(7, 7),
		"mau": last(30, 0), "mau_prev": last(30, 30)} {
		n, err := s.activeUsers(ctx, p, pf)
		if err != nil {
			return nil, err
		}
		counts[k] = n
	}
	newToday, err := s.Q.AnalyticsNewUsers(ctx, db.AnalyticsNewUsersParams{FromDate: today, ToDate: today, Platform: pf})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	newYesterday, err := s.Q.AnalyticsNewUsers(ctx, db.AnalyticsNewUsersParams{FromDate: day(1).from, ToDate: day(1).to, Platform: pf})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	bToday, err := s.Q.AnalyticsBriefings(ctx, db.AnalyticsBriefingsParams{FromDate: today, ToDate: today, Platform: pf})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	bYesterday, err := s.Q.AnalyticsBriefings(ctx, db.AnalyticsBriefingsParams{FromDate: day(1).from, ToDate: day(1).to, Platform: pf})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	rToday, err := s.Q.AnalyticsReports(ctx, db.AnalyticsReportsParams{FromDate: today, ToDate: today, Platform: pf})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	rYesterday, err := s.Q.AnalyticsReports(ctx, db.AnalyticsReportsParams{FromDate: day(1).from, ToDate: day(1).to, Platform: pf})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	stick := func(d, m int) *float64 { return ratio(d, m) }
	stickNow := 0.0
	if v := stick(counts["dau"], counts["mau"]); v != nil {
		stickNow = *v
	}
	kpis := map[string]any{
		"dau":           kpi(float64(counts["dau"]), f64(float64(counts["dau_prev"]))),
		"wau":           kpi(float64(counts["wau"]), f64(float64(counts["wau_prev"]))),
		"mau":           kpi(float64(counts["mau"]), f64(float64(counts["mau_prev"]))),
		"stickiness":    kpi(stickNow, stick(counts["dau_prev"], counts["mau"])),
		"newUsers":      kpi(float64(newToday), f64(float64(newYesterday))),
		"briefingsRead": kpi(float64(bToday.Read), f64(float64(bYesterday.Read))),
		"reports":       kpi(float64(rToday.Started), f64(float64(rYesterday.Started))),
	}

	gateB, err := s.gateB(ctx, today, pf)
	if err != nil {
		return nil, err
	}

	todo, err := s.Q.AnalyticsTodo(ctx, s.now().Add(-24*time.Hour))
	if err != nil {
		return nil, apperr.Internal(err)
	}
	rows, err := s.Q.AnalyticsDaily(ctx, db.AnalyticsDailyParams{FromDate: last(30, 0).from, ToDate: today, Platform: pf})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	daily := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		daily = append(daily, map[string]any{"date": dateKey(r.Date), "activeUsers": r.ActiveUsers, "newUsers": r.NewUsers})
	}
	risk, err := s.atRisk(ctx, pf)
	if err != nil {
		return nil, err
	}
	return gen.GetAnalyticsOverview200JSONResponse(recode[gen.AnalyticsOverview](map[string]any{
		"gateB": gateB, "kpis": kpis, "daily": daily, "atRisk": risk,
		"todo": map[string]any{"openFeedback": todo.OpenFeedback, "pendingCampaigns": todo.PendingCampaigns,
			"pendingReleases": todo.PendingReleases, "failedTasks24h": todo.FailedTasks},
	})), nil
}

func (s *Server) gateB(ctx context.Context, today time.Time, pf *string) (map[string]any, error) {
	// D30: users who signed up 31–60 days ago (their day 31 is over), and per sign-up week.
	cohortFrom, cohortTo := today.AddDate(0, 0, -60), today.AddDate(0, 0, -32)
	ret, err := s.Q.AnalyticsRetention(ctx, db.AnalyticsRetentionParams{Today: today, FromDate: cohortFrom, ToDate: cohortTo, Platform: pf})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	eligible, retained := 0, 0
	for _, r := range ret {
		eligible += int(r.D30Eligible)
		retained += int(r.D30)
	}
	weeklyFrom := weekStart(today.AddDate(0, 0, -32)).AddDate(0, 0, -7*7)
	wret, err := s.Q.AnalyticsRetention(ctx, db.AnalyticsRetentionParams{Today: today, FromDate: weeklyFrom, ToDate: cohortTo, Platform: pf})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	d30Weekly := []map[string]any{}
	for _, r := range wret {
		if r.D30Eligible == 0 {
			continue
		}
		d30Weekly = append(d30Weekly, map[string]any{"week": dateKey(r.Week), "rate": ratio(int(r.D30), int(r.D30Eligible)),
			"eligible": r.D30Eligible, "retained": r.D30})
	}

	// Satisfaction and report cost: last 30 days, and the last 8 weeks.
	month := period{today.AddDate(0, 0, -29), today}
	rep, err := s.Q.AnalyticsReports(ctx, db.AnalyticsReportsParams{FromDate: month.from, ToDate: month.to, Platform: pf})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	rated := int(rep.Satisfied + rep.Unsatisfied)
	cost, err := s.reportUnitCost(ctx, month, pf)
	if err != nil {
		return nil, err
	}
	satWeekly, costWeekly := []map[string]any{}, []map[string]any{}
	for w := weekStart(today).AddDate(0, 0, -7*7); !w.After(today); w = w.AddDate(0, 0, 7) {
		p := period{w, w.AddDate(0, 0, 6)}
		if p.to.After(today) {
			p.to = today
		}
		r, err := s.Q.AnalyticsReports(ctx, db.AnalyticsReportsParams{FromDate: p.from, ToDate: p.to, Platform: pf})
		if err != nil {
			return nil, apperr.Internal(err)
		}
		satWeekly = append(satWeekly, map[string]any{"week": dateKey(w), "rate": ratio(int(r.Satisfied), int(r.Satisfied+r.Unsatisfied)),
			"satisfied": r.Satisfied, "unsatisfied": r.Unsatisfied, "completed": r.Completed})
		c, err := s.reportUnitCost(ctx, p, pf)
		if err != nil {
			return nil, err
		}
		costWeekly = append(costWeekly, map[string]any{"week": dateKey(w), "avgCny": c["avgCny"], "reports": c["reports"]})
	}
	return map[string]any{
		"d30Retention": map[string]any{"rate": ratio(retained, eligible), "eligible": eligible, "retained": retained,
			"cohortFrom": dateKey(cohortFrom), "cohortTo": dateKey(cohortTo), "weekly": d30Weekly},
		"reportSatisfaction": map[string]any{"rate": ratio(int(rep.Satisfied), rated), "ratingRate": ratio(rated, int(rep.Completed)),
			"satisfied": rep.Satisfied, "unsatisfied": rep.Unsatisfied, "completed": rep.Completed, "weekly": satWeekly},
		"reportCost": map[string]any{"avgCny": cost["avgCny"], "p90Cny": cost["p90Cny"], "reports": cost["reports"], "weekly": costWeekly},
	}, nil
}

// reportUnitCost is the model cost of finished report tasks created in p.
func (s *Server) reportUnitCost(ctx context.Context, p period, pf *string) (map[string]any, error) {
	rows, err := s.Q.AnalyticsUnitCosts(ctx, db.AnalyticsUnitCostsParams{FromDate: p.from, ToDate: p.to, Platform: pf})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	out := map[string]any{"avgCny": nil, "p90Cny": nil, "reports": 0}
	for _, r := range rows {
		if r.Kind == "report" && r.Tasks > 0 {
			out["avgCny"] = cny(r.CostMicros) / float64(r.Tasks)
			out["p90Cny"] = r.P90Micros / 1e6
			out["reports"] = r.Tasks
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- growth

func (s *Server) growthSummary(ctx context.Context, p period, pf *string) (map[string]any, []db.AnalyticsDailyRow, error) {
	rows, err := s.Q.AnalyticsDaily(ctx, db.AnalyticsDailyParams{FromDate: p.from, ToDate: p.to, Platform: pf})
	if err != nil {
		return nil, nil, apperr.Internal(err)
	}
	active, err := s.activeUsers(ctx, p, pf)
	if err != nil {
		return nil, nil, err
	}
	newUsers, dauSum := 0, 0
	for _, r := range rows {
		newUsers += int(r.NewUsers)
		dauSum += int(r.ActiveUsers)
	}
	return map[string]any{"newUsers": newUsers, "activeUsers": active, "avgDailyActive": float64(dauSum) / float64(p.days())}, rows, nil
}

func (s *Server) GetAnalyticsGrowth(ctx context.Context, req gen.GetAnalyticsGrowthRequestObject) (gen.GetAnalyticsGrowthResponseObject, error) {
	p, err := s.period(req.Params.From, req.Params.To, 30)
	if err != nil {
		return nil, err
	}
	pf := platformArg(req.Params.Platform)
	cur, rows, err := s.growthSummary(ctx, p, pf)
	if err != nil {
		return nil, err
	}
	prev, _, err := s.growthSummary(ctx, p.previous(), pf)
	if err != nil {
		return nil, err
	}
	daily := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		daily = append(daily, map[string]any{"date": dateKey(r.Date), "activeUsers": r.ActiveUsers, "newUsers": r.NewUsers,
			"newPhone": r.NewPhone, "newApple": r.NewApple, "newWechat": r.NewWechat, "newOther": r.NewOther, "totalUsers": r.TotalUsers})
	}
	br, err := s.Q.AnalyticsBreakdown(ctx, db.AnalyticsBreakdownParams{FromDate: p.from, ToDate: p.to, Platform: pf})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	dims := map[string]map[string]int{"platform": {}, "version": {}, "language": {}}
	for _, r := range br {
		dims[r.Dim][r.Key] += int(r.Users)
	}
	f, err := s.Q.AnalyticsFunnel(ctx, db.AnalyticsFunnelParams{Today: s.dayStart(0), FromDate: p.from, ToDate: p.to, Platform: pf})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return gen.GetAnalyticsGrowth200JSONResponse(recode[gen.AnalyticsGrowth](map[string]any{
		"from": dateKey(p.from), "to": dateKey(p.to), "current": cur, "previous": prev, "daily": daily,
		"platforms": keyCounts(dims["platform"]), "versions": keyCounts(dims["version"]), "languages": keyCounts(dims["language"]),
		"funnel": map[string]any{"registered": f.Registered, "subscribed": f.Subscribed, "readBriefing": f.ReadBriefing,
			"startedReport": f.StartedReport, "week2Eligible": f.Week2Eligible, "week2Active": f.Week2Active},
	})), nil
}

// ---------------------------------------------------------------- retention

func (s *Server) GetAnalyticsRetention(ctx context.Context, req gen.GetAnalyticsRetentionRequestObject) (gen.GetAnalyticsRetentionResponseObject, error) {
	today := s.dayStart(0)
	p, err := s.period(req.Params.From, req.Params.To, 12*7)
	if err != nil {
		return nil, err
	}
	if req.Params.From == nil {
		p.from = weekStart(p.from)
	}
	pf := platformArg(req.Params.Platform)
	rows, err := s.Q.AnalyticsRetention(ctx, db.AnalyticsRetentionParams{Today: today, FromDate: p.from, ToDate: p.to, Platform: pf})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	cell := func(e, r int32) map[string]any { return map[string]any{"eligible": e, "retained": r} }
	cohorts := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		cohorts = append(cohorts, map[string]any{"week": dateKey(r.Week), "users": r.Users,
			"d1": cell(r.D1Eligible, r.D1), "d3": cell(r.D3Eligible, r.D3), "d7": cell(r.D7Eligible, r.D7),
			"d14": cell(r.D14Eligible, r.D14), "d30": cell(r.D30Eligible, r.D30)})
	}
	risk, err := s.atRisk(ctx, pf)
	if err != nil {
		return nil, err
	}
	return gen.GetAnalyticsRetention200JSONResponse(recode[gen.AnalyticsRetention](map[string]any{
		"from": dateKey(p.from), "to": dateKey(p.to), "cohorts": cohorts, "atRisk": risk,
	})), nil
}

// ---------------------------------------------------------------- engagement

func (s *Server) briefingStats(ctx context.Context, p period, pf *string) (db.AnalyticsBriefingsRow, error) {
	b, err := s.Q.AnalyticsBriefings(ctx, db.AnalyticsBriefingsParams{FromDate: p.from, ToDate: p.to, Platform: pf})
	if err != nil {
		return b, apperr.Internal(err)
	}
	return b, nil
}

func (s *Server) reportStats(ctx context.Context, p period, pf *string) (db.AnalyticsReportsRow, error) {
	r, err := s.Q.AnalyticsReports(ctx, db.AnalyticsReportsParams{FromDate: p.from, ToDate: p.to, Platform: pf})
	if err != nil {
		return r, apperr.Internal(err)
	}
	return r, nil
}

func (s *Server) GetAnalyticsEngagement(ctx context.Context, req gen.GetAnalyticsEngagementRequestObject) (gen.GetAnalyticsEngagementResponseObject, error) {
	p, err := s.period(req.Params.From, req.Params.To, 30)
	if err != nil {
		return nil, err
	}
	pf := platformArg(req.Params.Platform)
	b, err := s.briefingStats(ctx, p, pf)
	if err != nil {
		return nil, err
	}
	bPrev, err := s.briefingStats(ctx, p.previous(), pf)
	if err != nil {
		return nil, err
	}
	r, err := s.reportStats(ctx, p, pf)
	if err != nil {
		return nil, err
	}
	rPrev, err := s.reportStats(ctx, p.previous(), pf)
	if err != nil {
		return nil, err
	}
	bDaily, err := s.Q.AnalyticsBriefingsDaily(ctx, db.AnalyticsBriefingsDailyParams{FromDate: p.from, ToDate: p.to, Platform: pf})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	rDaily, err := s.Q.AnalyticsReportsDaily(ctx, db.AnalyticsReportsDailyParams{FromDate: p.from, ToDate: p.to, Platform: pf})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	tpls, err := s.Q.AnalyticsTemplates(ctx, db.AnalyticsTemplatesParams{FromDate: p.from, ToDate: p.to, Platform: pf})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	groups, err := s.Q.AnalyticsReportGroups(ctx, db.AnalyticsReportGroupsParams{FromDate: p.from, ToDate: p.to, Platform: pf})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	push, err := s.Q.AnalyticsPush(ctx, db.AnalyticsPushParams{FromDate: p.from, ToDate: p.to, Platform: pf})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	cats, err := s.Q.AnalyticsPushByCategory(ctx, db.AnalyticsPushByCategoryParams{FromDate: p.from, ToDate: p.to, Platform: pf})
	if err != nil {
		return nil, apperr.Internal(err)
	}

	// Dates are filled in so charts have one point per day.
	byDate := func(n int) []time.Time {
		out := make([]time.Time, 0, n)
		for d := p.from; !d.After(p.to); d = d.AddDate(0, 0, 1) {
			out = append(out, d)
		}
		return out
	}(p.days())
	bd := map[string]db.AnalyticsBriefingsDailyRow{}
	for _, x := range bDaily {
		bd[dateKey(x.Date)] = x
	}
	rd := map[string]db.AnalyticsReportsDailyRow{}
	for _, x := range rDaily {
		rd[dateKey(x.Date)] = x
	}
	briefingsDaily, reportsDaily := []map[string]any{}, []map[string]any{}
	for _, d := range byDate {
		k := dateKey(d)
		briefingsDaily = append(briefingsDaily, map[string]any{"date": k, "generated": bd[k].Generated, "read": bd[k].Read})
		reportsDaily = append(reportsDaily, map[string]any{"date": k, "started": rd[k].Started, "completed": rd[k].Completed})
	}
	templates := make([]map[string]any, 0, len(tpls))
	for _, t := range tpls {
		templates = append(templates, map[string]any{"id": t.ID.String(), "name": t.Name, "activeSubscriptions": t.ActiveSubscriptions,
			"newSubscriptions": t.NewSubscriptions, "briefings": t.Briefings, "read": t.Read, "useful": t.Useful, "meh": t.Meh})
	}
	depths, reasons := map[string]int{}, map[string]int{}
	for _, g := range groups {
		if g.Dim == "depth" {
			depths[g.Key] += int(g.N)
		} else {
			reasons[g.Key] += int(g.N)
		}
	}
	categories := make([]map[string]any, 0, len(cats))
	for _, c := range cats {
		categories = append(categories, map[string]any{"category": c.Category, "messages": c.Messages, "pushed": c.Pushed, "opened": c.Opened, "read": c.Read})
	}
	return gen.GetAnalyticsEngagement200JSONResponse(recode[gen.AnalyticsEngagement](map[string]any{
		"from": dateKey(p.from), "to": dateKey(p.to),
		"briefings": b, "previousBriefings": bPrev, "briefingsDaily": briefingsDaily, "templates": templates,
		"reports": r, "previousReports": rPrev, "reportsDaily": reportsDaily,
		"depths": keyCounts(depths), "unsatisfiedReasons": keyCounts(reasons),
		"push": map[string]any{"activeUsers": push.ActiveUsers, "withDevice": push.WithDevice, "briefingPushOn": push.BriefingPushOn,
			"marketingOptIn": push.MarketingOptIn, "messages": push.Messages, "pushed": push.Pushed, "opened": push.Opened,
			"categories": categories},
	})), nil
}

// ---------------------------------------------------------------- costs

func (s *Server) costSummary(ctx context.Context, p period, pf *string) (map[string]any, error) {
	c, err := s.Q.AnalyticsCostTotal(ctx, db.AnalyticsCostTotalParams{FromDate: p.from, ToDate: p.to, Platform: pf})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	active, err := s.activeUsers(ctx, p, pf)
	if err != nil {
		return nil, err
	}
	var per *float64
	if active > 0 {
		per = f64(cny(c.CostMicros) / float64(active))
	}
	return map[string]any{"costCny": cny(c.CostMicros), "billedTokens": c.BilledTokens, "activeUsers": active, "costPerActiveUserCny": per}, nil
}

func (s *Server) GetAnalyticsCosts(ctx context.Context, req gen.GetAnalyticsCostsRequestObject) (gen.GetAnalyticsCostsResponseObject, error) {
	p, err := s.period(req.Params.From, req.Params.To, 30)
	if err != nil {
		return nil, err
	}
	pf := platformArg(req.Params.Platform)
	cur, err := s.costSummary(ctx, p, pf)
	if err != nil {
		return nil, err
	}
	prev, err := s.costSummary(ctx, p.previous(), pf)
	if err != nil {
		return nil, err
	}
	units, err := s.Q.AnalyticsUnitCosts(ctx, db.AnalyticsUnitCostsParams{FromDate: p.from, ToDate: p.to, Platform: pf})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	unitCosts := make([]map[string]any, 0, len(units))
	for _, u := range units {
		unitCosts = append(unitCosts, map[string]any{"kind": u.Kind, "tasks": u.Tasks, "costCny": cny(u.CostMicros),
			"medianCny": u.MedianMicros / 1e6, "p90Cny": u.P90Micros / 1e6})
	}
	today := s.dayStart(0)
	monthStart := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, today.Location())
	var limit int64
	if s.Quota != nil {
		limit = s.Quota.PlanLimits(db.User{PlanCode: "free"}).TokensPerMonth
	}
	q, err := s.Q.AnalyticsQuotaUse(ctx, db.AnalyticsQuotaUseParams{MonthStart: monthStart, DefaultLimit: limit, Platform: pf})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return gen.GetAnalyticsCosts200JSONResponse(recode[gen.AnalyticsCosts](map[string]any{
		"from": dateKey(p.from), "to": dateKey(p.to), "current": cur, "previous": prev, "unitCosts": unitCosts,
		"quota": map[string]any{"monthStart": dateKey(monthStart), "defaultLimit": limit, "users": q.Users, "over50": q.Over50,
			"over80": q.Over80, "exhausted": q.Exhausted, "medianUsed": q.MedianUsed, "grantedTokens": q.GrantedTokens,
			"grantedUsers": q.GrantedUsers},
	})), nil
}

// ---------------------------------------------------------------- quality

func (s *Server) GetAnalyticsQuality(ctx context.Context, req gen.GetAnalyticsQualityRequestObject) (gen.GetAnalyticsQualityResponseObject, error) {
	p, err := s.period(req.Params.From, req.Params.To, 7)
	if err != nil {
		return nil, err
	}
	kinds, err := s.Q.AnalyticsTaskQuality(ctx, db.AnalyticsTaskQualityParams{FromDate: p.from, ToDate: p.to})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	errs, err := s.Q.AnalyticsTaskErrors(ctx, db.AnalyticsTaskErrorsParams{FromDate: p.from, ToDate: p.to})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	ks := make([]map[string]any, 0, len(kinds))
	for _, k := range kinds {
		ks = append(ks, map[string]any{"kind": k.Kind, "total": k.Total, "done": k.Done, "failed": k.Failed, "cancelled": k.Cancelled,
			"p50Seconds": k.P50Seconds, "p90Seconds": k.P90Seconds})
	}
	es := make([]map[string]any, 0, len(errs))
	for _, e := range errs {
		es = append(es, map[string]any{"kind": e.Kind, "code": e.Code, "message": e.Message, "count": e.N})
	}
	return gen.GetAnalyticsQuality200JSONResponse(recode[gen.AnalyticsQuality](map[string]any{
		"from": dateKey(p.from), "to": dateKey(p.to), "kinds": ks, "errors": es,
	})), nil
}
