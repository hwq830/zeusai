package adminapi

import (
	"context"
	"sort"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/modelgw"
	"github.com/hwq830/zeusai/server/internal/quota"
	"github.com/hwq830/zeusai/server/internal/task"
)

// reportingTZ is the zone operators count days in.
const reportingTZ = "Asia/Shanghai"

func (s *Server) dayStart(daysAgo int) time.Time {
	loc, err := time.LoadLocation(reportingTZ)
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	n := s.now().In(loc)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -daysAgo)
}

func dateKey(t time.Time) string { return t.Format("2006-01-02") }

func (s *Server) GetDashboard(ctx context.Context, _ gen.GetDashboardRequestObject) (gen.GetDashboardResponseObject, error) {
	var d gen.Dashboard
	today, week, month30 := s.dayStart(0), s.dayStart(6), s.dayStart(29)
	uc, err := s.Q.DashboardUserCounts(ctx, db.DashboardUserCountsParams{DayStart: today, WeekStart: week})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	d.Users.Total, d.Users.NewToday, d.Users.New7d, d.Users.Disabled = int(uc.Total), int(uc.NewToday), int(uc.New7d), int(uc.Disabled)

	for _, w := range []struct {
		since time.Time
		dst   *int
	}{{today, &d.ActiveUsers.D1}, {week, &d.ActiveUsers.D7}, {month30, &d.ActiveUsers.D30}} {
		n, err := s.Q.CountActiveUsersSince(ctx, w.since)
		if err != nil {
			return nil, apperr.Internal(err)
		}
		*w.dst = int(n)
	}

	counts, err := s.Q.TaskStatusCountsSince(ctx, today)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	d.TasksToday = make([]gen.TaskCount, 0, len(counts))
	for _, c := range counts {
		d.TasksToday = append(d.TasksToday, gen.TaskCount{Kind: c.Kind, Status: c.Status, Count: int(c.N)})
	}

	loc := today.Location()
	n := s.now().In(loc)
	monthStart := time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, loc)
	rm, err := s.Q.CountReportsCreatedSince(ctx, monthStart)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	d.ReportsThisMonth = int(rm)

	for _, w := range []struct {
		since time.Time
		dst   *float64
	}{{today, &d.Cost.TodayCny}, {week, &d.Cost.Last7dCny}, {month30, &d.Cost.Last30dCny}} {
		c, err := s.Q.SumCostSince(ctx, w.since)
		if err != nil {
			return nil, apperr.Internal(err)
		}
		*w.dst = cny(c)
	}
	kinds, err := s.Q.CostByTaskKind(ctx, month30)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	for _, k := range kinds {
		if k.Kind == "report" && k.Tasks > 0 {
			avg := cny(k.CostMicros) / float64(k.Tasks)
			d.Cost.AvgReportCostCny = &avg
		}
	}

	d.Daily, err = s.daily(ctx, 14)
	if err != nil {
		return nil, err
	}
	d.Satisfaction, err = s.satisfaction(ctx)
	if err != nil {
		return nil, err
	}
	d.Shares, err = s.shareStats(ctx, today, week)
	if err != nil {
		return nil, err
	}
	return gen.GetDashboard200JSONResponse(d), nil
}

// shareStats covers links created in the last 7 days and what they have collected so far.
func (s *Server) shareStats(ctx context.Context, today, week time.Time) (*gen.ShareStats, error) {
	st := &gen.ShareStats{}
	t, err := s.Q.AdminShareTotals(ctx, today)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	st.CreatedToday = int(t.Created)
	w, err := s.Q.AdminShareTotals(ctx, week)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	st.Created7d, st.Views7d, st.WechatViews7d, st.CtaClicks7d = int(w.Created), int(w.Views), int(w.WechatViews), int(w.CtaClicks)
	ready, err := s.Q.CountBriefingsReadySince(ctx, &week)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if ready > 0 {
		r := float64(w.BriefingsShared) / float64(ready)
		st.BriefingShareRate7d = &r
	}
	chans, err := s.Q.AdminShareChannels(ctx, week)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	st.Channels7d = make([]struct {
		Channel string `json:"channel"`
		Shares  int    `json:"shares"`
	}, 0, len(chans))
	for _, c := range chans {
		st.Channels7d = append(st.Channels7d, struct {
			Channel string `json:"channel"`
			Shares  int    `json:"shares"`
		}{c.Channel, int(c.Shares)})
	}
	return st, nil
}

// daily returns one point per day for the last n days, oldest first, with empty days filled.
func (s *Server) daily(ctx context.Context, n int) ([]gen.DailyPoint, error) {
	since := s.dayStart(n - 1)
	users, err := s.Q.DailyNewUsers(ctx, db.DailyNewUsersParams{Tz: reportingTZ, Since: since})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	tasks, err := s.Q.DailyTasks(ctx, db.DailyTasksParams{Tz: reportingTZ, Since: since})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	costs, err := s.Q.DailyCost(ctx, db.DailyCostParams{Tz: reportingTZ, Since: since})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	points := make([]gen.DailyPoint, n)
	idx := map[string]int{}
	for i := range points {
		day := since.AddDate(0, 0, i)
		points[i].Date = openapi_types.Date{Time: time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)}
		idx[dateKey(day)] = i
	}
	for _, u := range users {
		if i, ok := idx[dateKey(u.Day)]; ok {
			points[i].NewUsers = int(u.N)
		}
	}
	for _, t := range tasks {
		if i, ok := idx[dateKey(t.Day)]; ok {
			points[i].Tasks, points[i].ActiveUsers = int(t.N), int(t.Users)
		}
	}
	for _, c := range costs {
		if i, ok := idx[dateKey(c.Day)]; ok {
			points[i].CostCny = cny(c.CostMicros)
		}
	}
	return points, nil
}

// ---------------------------------------------------------------- tasks

func (s *Server) ListTasks(ctx context.Context, req gen.ListTasksRequestObject) (gen.ListTasksResponseObject, error) {
	p := req.Params
	lim, off := page(p.Cursor, p.Limit)
	params := db.AdminListTasksParams{Status: strPtr(p.Status), Kind: strPtr(p.Kind), Limit: int32(lim), Offset: int32(off)}
	if p.UserId != nil && *p.UserId != "" {
		id, err := parseID(*p.UserId, "用户")
		if err != nil {
			return nil, err
		}
		params.UserID = &id
	}
	rows, err := s.Q.AdminListTasks(ctx, params)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	items := make([]gen.AdminTask, 0, len(rows))
	for _, r := range rows {
		t := toAdminTask(db.Task{
			ID: r.ID, UserID: r.UserID, Kind: r.Kind, Status: r.Status, Title: r.Title, Subtitle: r.Subtitle,
			ReportID: r.ReportID, BriefingID: r.BriefingID, Error: r.Error, PromptVersion: r.PromptVersion,
			CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
		}, r.CostMicros)
		t.Nickname, t.PhoneMasked = r.Nickname, maskPhone(r.Phone)
		items = append(items, t)
	}
	return gen.ListTasks200JSONResponse{Items: items, NextCursor: nextCursor(len(rows), lim, off)}, nil
}

func toAdminTask(t db.Task, costMicros int64) gen.AdminTask {
	out := gen.AdminTask{
		Id: t.ID.String(), UserId: t.UserID.String(), Kind: t.Kind, Status: t.Status, Title: t.Title,
		Subtitle: &t.Subtitle, PromptVersion: &t.PromptVersion, ReportId: idStr(t.ReportID), BriefingId: idStr(t.BriefingID),
		CostCny: cny(costMicros), CreatedAt: t.CreatedAt, StartedAt: t.StartedAt, FinishedAt: t.FinishedAt,
	}
	if m := jsonObject(t.Error); m != nil {
		out.Error = &m
	}
	return out
}

func (s *Server) loadTask(ctx context.Context, id string) (db.Task, error) {
	tid, err := parseID(id, "任务")
	if err != nil {
		return db.Task{}, err
	}
	t, err := s.Q.AdminGetTask(ctx, tid)
	if err != nil {
		return t, notFound(err, "任务")
	}
	return t, nil
}

func (s *Server) GetTask(ctx context.Context, req gen.GetTaskRequestObject) (gen.GetTaskResponseObject, error) {
	t, err := s.loadTask(ctx, req.TaskId)
	if err != nil {
		return nil, err
	}
	usage, err := s.Q.ListTaskUsage(ctx, &t.ID)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	var total int64
	calls := make([]gen.ModelCall, 0, len(usage))
	for _, u := range usage {
		total += u.CostMicros
		calls = append(calls, gen.ModelCall{
			Provider: u.Provider, Model: u.Model, Tier: u.Tier, InputTokens: int(u.InputTokens), OutputTokens: int(u.OutputTokens),
			CachedTokens: int(u.CachedTokens), CostCny: cny(u.CostMicros), CreatedAt: u.CreatedAt,
		})
	}
	u, err := s.Q.GetUser(ctx, t.UserID)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	at := toAdminTask(t, total)
	at.Nickname, at.PhoneMasked = u.Nickname, maskPhone(u.Phone)
	d := gen.TaskDetail{Task: at, Input: orEmptyMap(jsonObject(t.Input)), Calls: calls}
	if m := jsonObject(t.Plan); m != nil {
		d.Plan = &m
	}
	return gen.GetTask200JSONResponse(d), nil
}

func (s *Server) CancelTask(ctx context.Context, req gen.CancelTaskRequestObject) (gen.CancelTaskResponseObject, error) {
	if _, err := require(ctx, PermTasks); err != nil {
		return nil, err
	}
	t, err := s.loadTask(ctx, req.TaskId)
	if err != nil {
		return nil, err
	}
	if task.Terminal(t.Status) {
		return nil, apperr.Conflict("任务已结束")
	}
	if err := s.Orch.CancelTask(ctx, t.ID); err != nil {
		return nil, apperr.Unavailable("取消失败，请稍后再试").WithCause(err)
	}
	s.audit(ctx, "task.cancel", "task", t.ID.String(), map[string]any{"userId": t.UserID.String(), "kind": t.Kind})
	return gen.CancelTask202Response{}, nil
}

// ---------------------------------------------------------------- costs

type costDay struct {
	Date         string  `json:"date"`
	CostCny      float64 `json:"costCny"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	Calls        int     `json:"calls"`
}

type costModel struct {
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	Tier         string  `json:"tier"`
	Calls        int     `json:"calls"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	CostCny      float64 `json:"costCny"`
	BilledTokens int64   `json:"billedTokens"`
	// CnyPerMillionBilled shows whether a model's weight covers its cost.
	CnyPerMillionBilled *float64 `json:"cnyPerMillionBilled"`
}

type quotaBucket struct {
	Label string `json:"label"`
	Users int    `json:"users"`
}

type tokenQuotaStats struct {
	PeriodStart  string        `json:"periodStart"`
	PlanLimit    int64         `json:"planLimit"`
	ActiveUsers  int           `json:"activeUsers"`
	AvgTokens    int64         `json:"avgTokens"`
	AtLimitUsers int           `json:"atLimitUsers"`
	Buckets      []quotaBucket `json:"buckets"`
}

// tokenQuotaStats spreads this month's users over fifths of the free plan's allowance.
func (s *Server) tokenQuotaStats(ctx context.Context) (tokenQuotaStats, error) {
	start, _ := quota.MonthWindow(s.now(), reportingTZ)
	limit := s.Quota.PlanLimits(db.User{PlanCode: "free"}).TokensPerMonth
	out := tokenQuotaStats{PeriodStart: dateKey(start), PlanLimit: limit, Buckets: []quotaBucket{
		{Label: "0–20%"}, {Label: "20–40%"}, {Label: "40–60%"}, {Label: "60–80%"}, {Label: "80–100%"}, {Label: "用满"},
	}}
	rows, err := s.Q.BilledTokensByUserSince(ctx, start)
	if err != nil {
		return out, apperr.Internal(err)
	}
	var total int64
	for _, r := range rows {
		total += r.Tokens
		i := len(out.Buckets) - 1
		if limit > 0 && r.Tokens < limit {
			i = int(r.Tokens * 5 / limit)
		} else {
			out.AtLimitUsers++
		}
		out.Buckets[i].Users++
	}
	out.ActiveUsers = len(rows)
	if len(rows) > 0 {
		out.AvgTokens = total / int64(len(rows))
	}
	return out, nil
}

type costKind struct {
	Kind       string  `json:"kind"`
	Tasks      int     `json:"tasks"`
	CostCny    float64 `json:"costCny"`
	AvgCostCny float64 `json:"avgCostCny"`
}

type costUser struct {
	UserID      string  `json:"userId"`
	Nickname    string  `json:"nickname"`
	PhoneMasked string  `json:"phoneMasked"`
	Tasks       int     `json:"tasks"`
	CostCny     float64 `json:"costCny"`
}

func (s *Server) GetCosts(ctx context.Context, req gen.GetCostsRequestObject) (gen.GetCostsResponseObject, error) {
	days := 30
	if req.Params.Days != nil && *req.Params.Days >= 1 && *req.Params.Days <= 90 {
		days = *req.Params.Days
	}
	since := s.dayStart(days - 1)
	daily, err := s.Q.DailyCost(ctx, db.DailyCostParams{Tz: reportingTZ, Since: since})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	byDay := map[string]db.DailyCostRow{}
	for _, d := range daily {
		byDay[dateKey(d.Day)] = d
	}
	out := struct {
		Days     int             `json:"days"`
		TotalCny float64         `json:"totalCny"`
		Daily    []costDay       `json:"daily"`
		ByModel  []costModel     `json:"byModel"`
		ByKind   []costKind      `json:"byKind"`
		TopUsers []costUser      `json:"topUsers"`
		Quota    tokenQuotaStats `json:"tokenQuota"`
	}{Days: days, Daily: []costDay{}, ByModel: []costModel{}, ByKind: []costKind{}, TopUsers: []costUser{}}
	for i := 0; i < days; i++ {
		k := dateKey(since.AddDate(0, 0, i))
		d := byDay[k]
		out.Daily = append(out.Daily, costDay{Date: k, CostCny: cny(d.CostMicros), InputTokens: d.InputTokens, OutputTokens: d.OutputTokens, Calls: int(d.Calls)})
		out.TotalCny += cny(d.CostMicros)
	}
	models, err := s.Q.CostByModel(ctx, since)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	for _, m := range models {
		cm := costModel{Provider: m.Provider, Model: m.Model, Tier: m.Tier, Calls: int(m.Calls),
			InputTokens: m.InputTokens, OutputTokens: m.OutputTokens, CostCny: cny(m.CostMicros), BilledTokens: m.BilledTokens}
		if m.BilledTokens > 0 {
			v := cm.CostCny * 1e6 / float64(m.BilledTokens)
			cm.CnyPerMillionBilled = &v
		}
		out.ByModel = append(out.ByModel, cm)
	}
	kinds, err := s.Q.CostByTaskKind(ctx, since)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	for _, k := range kinds {
		ck := costKind{Kind: k.Kind, Tasks: int(k.Tasks), CostCny: cny(k.CostMicros)}
		if k.Tasks > 0 {
			ck.AvgCostCny = ck.CostCny / float64(k.Tasks)
		}
		out.ByKind = append(out.ByKind, ck)
	}
	top, err := s.Q.CostTopUsers(ctx, db.CostTopUsersParams{CreatedAt: since, Limit: 20})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	for _, u := range top {
		out.TopUsers = append(out.TopUsers, costUser{UserID: deref(idStr(u.UserID)), Nickname: u.Nickname,
			PhoneMasked: maskPhone(u.Phone), Tasks: int(u.Tasks), CostCny: cny(u.CostMicros)})
	}
	if out.Quota, err = s.tokenQuotaStats(ctx); err != nil {
		return nil, err
	}
	return gen.GetCosts200JSONResponse(recode[gen.CostReport](out)), nil
}

// ---------------------------------------------------------------- model gateway

type gwPrice struct {
	Model  string  `json:"model"`
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
	Cached float64 `json:"cached"`
}

type gwProvider struct {
	Name       string            `json:"name"`
	BaseURL    string            `json:"baseUrl"`
	APIKeyEnv  string            `json:"apiKeyEnv"`
	KeyPresent bool              `json:"keyPresent"`
	KeySource  string            `json:"keySource"`
	Timeout    string            `json:"timeout,omitempty"`
	Models     map[string]string `json:"models"`
	Prices     []gwPrice         `json:"prices"`
}

type gwUsage struct {
	Provider   string     `json:"provider"`
	Calls      int        `json:"calls"`
	CostCny    float64    `json:"costCny"`
	LastCallAt *time.Time `json:"lastCallAt"`
}

// GetModelGateway shows the routing file the api and orchestrator load. The admin process
// should be given the same ZEUS_MODELGW_CONFIG (and, to show keyPresent, the same key
// variables).
func (s *Server) GetModelGateway(ctx context.Context, _ gen.GetModelGatewayRequestObject) (gen.GetModelGatewayResponseObject, error) {
	out := struct {
		LLMProvider     string              `json:"llmProvider"`
		Configured      bool                `json:"configured"`
		ConfigError     string              `json:"configError,omitempty"`
		BreakerFailures int                 `json:"breakerFailures,omitempty"`
		BreakerCooldown string              `json:"breakerCooldown,omitempty"`
		Routes          map[string][]string `json:"routes"`
		Providers       []gwProvider        `json:"providers"`
		Usage24h        []gwUsage           `json:"usage24h"`
	}{LLMProvider: s.LLMProvider, Routes: map[string][]string{}, Providers: []gwProvider{}, Usage24h: []gwUsage{}}
	if out.LLMProvider == "" {
		out.LLMProvider = "mock"
	}
	if s.ModelGWConfig != "" {
		cfg, err := modelgw.LoadConfig(s.ModelGWConfig)
		if err != nil {
			out.ConfigError = err.Error()
		} else {
			out.Configured = true
			out.BreakerFailures = cfg.BreakerFailures
			if cfg.BreakerCooldown > 0 {
				out.BreakerCooldown = cfg.BreakerCooldown.String()
			}
			for tier, names := range cfg.Routes {
				out.Routes[string(tier)] = names
			}
			for _, p := range cfg.Providers {
				gp := gwProvider{Name: p.Name, BaseURL: p.BaseURL, APIKeyEnv: p.APIKeyEnv, Models: map[string]string{}, Prices: []gwPrice{}}
				gp.KeyPresent, gp.KeySource = s.keySourceFor(ctx, p.Name, p.APIKeyEnv)
				if p.Timeout > 0 {
					gp.Timeout = p.Timeout.String()
				}
				for tier, m := range p.Models {
					gp.Models[string(tier)] = m
				}
				for m, pr := range p.Prices {
					gp.Prices = append(gp.Prices, gwPrice{Model: m, Input: pr.Input, Output: pr.Output, Cached: pr.Cached})
				}
				sort.Slice(gp.Prices, func(i, j int) bool { return gp.Prices[i].Model < gp.Prices[j].Model })
				out.Providers = append(out.Providers, gp)
			}
		}
	}
	usage, err := s.Q.UsageByProviderSince(ctx, s.now().Add(-24*time.Hour))
	if err != nil {
		return nil, apperr.Internal(err)
	}
	for _, u := range usage {
		last := u.LastCallAt
		out.Usage24h = append(out.Usage24h, gwUsage{Provider: u.Provider, Calls: int(u.Calls), CostCny: cny(u.CostMicros), LastCallAt: &last})
	}
	return gen.GetModelGateway200JSONResponse(recode[gen.ModelGateway](out)), nil
}
