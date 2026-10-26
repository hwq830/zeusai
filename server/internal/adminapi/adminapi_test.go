package adminapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/adminapi"
	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/auth"
	"github.com/hwq830/zeusai/server/internal/blob"
	"github.com/hwq830/zeusai/server/internal/connector"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/moderation"
	"github.com/hwq830/zeusai/server/internal/notify"
	"github.com/hwq830/zeusai/server/internal/orch"
	"github.com/hwq830/zeusai/server/internal/platform/testdb"
	"github.com/hwq830/zeusai/server/internal/push"
	"github.com/hwq830/zeusai/server/internal/quota"
	"github.com/hwq830/zeusai/server/internal/remoteconfig"
	"github.com/hwq830/zeusai/server/internal/schedule"
	"github.com/hwq830/zeusai/server/internal/template"
)

// fakeOrch records the calls the admin API makes to Temporal.
type fakeOrch struct {
	orch.Client
	mu        sync.Mutex
	schedules map[uuid.UUID]bool // subscription → paused
	cancelled []uuid.UUID
	campaigns []orch.CampaignRun
	stopped   []uuid.UUID
}

func (f *fakeOrch) StartCampaign(_ context.Context, run orch.CampaignRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.campaigns = append(f.campaigns, run)
	return nil
}

func (f *fakeOrch) CancelCampaign(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, id)
	return nil
}

func (f *fakeOrch) UpsertBriefingSchedule(_ context.Context, _, sub uuid.UUID, _ schedule.Schedule, paused bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.schedules[sub] = paused
	return nil
}

func (f *fakeOrch) CancelTask(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, id)
	return nil
}

type harness struct {
	t      *testing.T
	q      *db.Queries
	orch   *fakeOrch
	url    string
	token  string
	router routers.Router
	blob   *blob.Local
	pushes *push.Log
	config *remoteconfig.Cache
}

func setup(t *testing.T) *harness {
	pool := testdb.New(t)
	q := db.New(pool)
	ctx := context.Background()
	if _, err := (&template.Service{Q: q}).SeedOfficial(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := (&connector.Service{Q: q}).SeedCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	fo := &fakeOrch{schedules: map[uuid.UUID]bool{}}
	files := &blob.Local{Dir: t.TempDir(), BaseURL: "https://api.zeus.test", Secret: []byte("files")}
	pushes := &push.Log{}
	rc := &remoteconfig.Cache{Q: q, Region: "china"}
	srv := &adminapi.Server{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Q: q, Tokens: adminapi.NewTokens("test", time.Hour),
		Quota: &quota.Service{Q: q, Config: rc}, Orch: fo, LLMProvider: "gateway", ModelGWConfig: "../../../deploy/modelgw.example.yaml",
		PublicBaseURL: "https://zeus.test", Blob: files, Push: pushes, Notify: &notify.Service{Q: q, Sender: pushes}, Region: "china",
		Releases: &remoteconfig.Releases{Pool: pool, RequireApproval: true},
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	spec, err := gen.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	spec.Servers = nil
	router, err := gorillamux.NewRouter(spec)
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, q: q, orch: fo, url: hs.URL, router: router, blob: files, pushes: pushes, config: rc}
}

func (h *harness) do(method, path string, body any, wantStatus int, out any) {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.url+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		h.t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, wantStatus, raw)
	}
	// Every response must match api/admin.yaml, which the console's types are generated from.
	route, params, err := h.router.FindRoute(req)
	if err != nil {
		h.t.Fatalf("%s %s: not in contract: %v", method, path, err)
	}
	in := &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route,
		Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc}}
	if err := openapi3filter.ValidateResponse(context.Background(), &openapi3filter.ResponseValidationInput{
		RequestValidationInput: in, Status: resp.StatusCode, Header: resp.Header, Body: io.NopCloser(bytes.NewReader(raw)),
		Options: &openapi3filter.Options{IncludeResponseStatus: true},
	}); err != nil {
		h.t.Fatalf("%s %s: response violates contract: %v\n%s", method, path, err, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			h.t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
}

func (h *harness) login(email, password string) gen.LoginResponse {
	h.t.Helper()
	h.token = ""
	var lr gen.LoginResponse
	h.do("POST", "/admin/v1/auth/login", map[string]string{"email": email, "password": password}, 200, &lr)
	h.token = lr.Token
	return lr
}

func (h *harness) createOwner(email, password string) {
	h.t.Helper()
	hash, err := adminapi.HashPassword(password)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.q.CreateAdminUser(context.Background(), db.CreateAdminUserParams{Email: email, PasswordHash: hash, Role: "owner"}); err != nil {
		h.t.Fatal(err)
	}
}

func TestAdminConsole(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	h.createOwner("owner@zeus.test", "correct-horse-battery")

	// ---- login
	h.do("POST", "/admin/v1/auth/login", map[string]string{"email": "owner@zeus.test", "password": "wrong-password"}, 401, nil)
	h.do("GET", "/admin/v1/me", nil, 401, nil)
	lr := h.login("owner@zeus.test", "correct-horse-battery")
	if lr.Admin.Role != "owner" || len(lr.Admin.Permissions) != 12 {
		t.Fatalf("login admin = %+v", lr.Admin)
	}

	// ---- fixtures: a user with a subscription, a finished report task with model usage, a share
	phone := "+8613800001234"
	u, err := h.q.CreateUser(ctx, db.CreateUserParams{Nickname: "小王", Phone: &phone, Timezone: "Asia/Shanghai"})
	if err != nil {
		t.Fatal(err)
	}
	sched := schedule.Schedule{Freq: "daily", Time: "08:00", TZ: "Asia/Shanghai"}
	sc, _ := json.Marshal(sched)
	next := time.Now().Add(time.Hour)
	sub, err := h.q.CreateSubscription(ctx, db.CreateSubscriptionParams{UserID: u.ID, Name: "科技早报", FieldValues: []byte(`{}`),
		Topics: []string{}, Schedule: sc, Length: "standard", PushEnabled: true, NextRunAt: &next})
	if err != nil {
		t.Fatal(err)
	}
	tk, err := h.q.CreateTask(ctx, db.CreateTaskParams{UserID: u.ID, Kind: "report", Title: "新能源调研", Input: []byte(`{"topic":"新能源"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.q.UpdateTaskProgress(ctx, db.UpdateTaskProgressParams{ID: tk.ID, UserID: u.ID, Status: "done"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := h.q.RecordModelUsage(ctx, db.RecordModelUsageParams{UserID: &u.ID, TaskID: &tk.ID, Provider: "qwen", Model: "qwen-max",
			Tier: "strong", InputTokens: 1000, OutputTokens: 500, CostMicros: 1_250_000, BilledTokens: 1500}); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := h.q.CreateReport(ctx, db.CreateReportParams{UserID: u.ID, TaskID: &tk.ID, Title: "新能源调研", Topic: "新能源", Depth: "standard", Focuses: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	share, err := h.q.CreateShare(ctx, db.CreateShareParams{UserID: u.ID, ReportID: &rep.ID, Token: "tok123", Scope: "full", Channel: "wechat_session"})
	if err != nil {
		t.Fatal(err)
	}
	running, err := h.q.CreateTask(ctx, db.CreateTaskParams{UserID: u.ID, Kind: "briefing", Title: "科技早报", Input: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}

	// ---- dashboard
	var dash gen.Dashboard
	h.do("GET", "/admin/v1/dashboard", nil, 200, &dash)
	if dash.Users.Total != 1 || dash.Users.NewToday != 1 || dash.ActiveUsers.D1 != 1 || len(dash.Daily) != 14 {
		t.Fatalf("dashboard = %+v", dash)
	}
	if dash.Cost.AvgReportCostCny == nil || *dash.Cost.AvgReportCostCny != 2.5 || dash.Cost.TodayCny != 2.5 {
		t.Fatalf("dashboard cost = %+v", dash.Cost)
	}
	if st := dash.Shares; st == nil || st.CreatedToday != 1 || st.Created7d != 1 || len(st.Channels7d) != 1 || st.Channels7d[0].Channel != "wechat_session" {
		t.Fatalf("dashboard shares = %+v", dash.Shares)
	}

	// ---- users
	var up gen.UserPage
	h.do("GET", "/admin/v1/users?q=0001234", nil, 200, &up)
	if len(up.Items) != 1 || up.Items[0].PhoneMasked != "138****1234" {
		t.Fatalf("user search = %+v", up.Items)
	}
	var ud gen.UserDetail
	h.do("GET", "/admin/v1/users/"+u.ID.String(), nil, 200, &ud)
	if ud.Phone == nil || *ud.Phone != phone || ud.Quota.Tokens.Limit != 7_500_000 || ud.Quota.Tokens.Used != 3000 || ud.Cost30dCny != 2.5 || len(ud.Subscriptions) != 1 {
		t.Fatalf("user detail = %+v", ud)
	}
	h.do("PUT", "/admin/v1/users/"+u.ID.String()+"/quota", map[string]any{"tokensPerMonth": 1_000_000, "note": "压测账号"}, 200, &ud)
	if ud.Quota.Tokens.Limit != 1_000_000 || ud.Quota.Tokens.PlanLimit != 7_500_000 || ud.Quota.Subscriptions.Limit != 2 || ud.Quota.Override == nil {
		t.Fatalf("quota override = %+v", ud.Quota)
	}
	qs := &quota.Service{Q: h.q}
	if err := h.q.RecordModelUsage(ctx, db.RecordModelUsageParams{UserID: &u.ID, Provider: "qwen", Model: "qwen-max", Tier: "strong", BilledTokens: 600_000}); err != nil {
		t.Fatal(err)
	}
	if err := qs.CheckReport(ctx, u); err == nil { // 603k of 1M used: under the 500k a report needs
		t.Fatal("override not applied")
	}
	h.do("POST", "/admin/v1/users/"+u.ID.String()+"/token-grants", map[string]any{"tokens": 200_000, "reason": "补偿"}, 200, &ud)
	if ud.Quota.Tokens.Limit != 1_200_000 || ud.Quota.Tokens.Granted != 200_000 || len(ud.Quota.Grants) != 1 || ud.Quota.Grants[0].Reason != "补偿" {
		t.Fatalf("grant = %+v", ud.Quota)
	}
	if err := qs.CheckReport(ctx, u); err != nil {
		t.Fatalf("grant not applied: %v", err)
	}
	h.do("POST", "/admin/v1/users/"+u.ID.String()+"/token-grants", map[string]any{"tokens": 0, "reason": "x"}, 400, nil)
	ud = gen.UserDetail{}
	h.do("DELETE", "/admin/v1/users/"+u.ID.String()+"/quota", nil, 200, &ud)
	if ud.Quota.Tokens.Limit != 7_700_000 || ud.Quota.Override != nil {
		t.Fatalf("quota cleared = %+v", ud.Quota)
	}

	// disable: refresh tokens revoked, subscriptions and schedules paused, sign-in refused
	if err := h.q.CreateRefreshToken(ctx, db.CreateRefreshTokenParams{UserID: u.ID, TokenHash: auth.HashRefreshToken("rt"), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	h.do("POST", "/admin/v1/users/"+u.ID.String()+"/disable", map[string]string{"reason": "违规内容"}, 200, &ud)
	if !ud.User.Disabled || ud.Subscriptions[0].Status != "paused" || !h.orch.schedules[sub.ID] {
		t.Fatalf("disable = %+v, schedules %v", ud, h.orch.schedules)
	}
	as := &auth.Service{Q: h.q, Tokens: auth.NewTokens("x", time.Minute), RefreshTTL: time.Hour}
	if _, err := as.Refresh(ctx, "rt"); err == nil || !strings.Contains(err.Error(), "登录已过期") {
		t.Fatalf("refresh after disable: %v", err)
	}
	h.do("POST", "/admin/v1/users/"+u.ID.String()+"/enable", nil, 200, &ud)
	if ud.User.Disabled || ud.Subscriptions[0].Status != "active" || ud.Subscriptions[0].NextRunAt == nil || h.orch.schedules[sub.ID] {
		t.Fatalf("enable = %+v", ud)
	}

	// ---- templates: console edits survive a re-seed
	var tp gen.TemplatePage
	h.do("GET", "/admin/v1/templates?scope=official", nil, 200, &tp)
	if len(tp.Items) == 0 {
		t.Fatal("no official templates")
	}
	first := tp.Items[0]
	in := gen.AdminTemplateInput{Name: "改过的名字", Fields: first.Fields, Sections: first.Sections, Sources: first.Sources,
		DefaultSchedule: first.DefaultSchedule, Published: false, SortOrder: first.SortOrder}
	var tpl gen.AdminTemplate
	h.do("PUT", "/admin/v1/templates/"+first.Id, in, 200, &tpl)
	if !tpl.AdminManaged || tpl.Published || tpl.Version != first.Version {
		t.Fatalf("updated template = %+v", tpl)
	}
	if _, err := (&template.Service{Q: h.q}).SeedOfficial(ctx); err != nil {
		t.Fatal(err)
	}
	h.do("GET", "/admin/v1/templates/"+first.Id, nil, 200, &tpl)
	if tpl.Name != "改过的名字" {
		t.Fatalf("seed overwrote console edit: %q", tpl.Name)
	}
	in.Name, in.Published = "新模板", true
	in.Slug = ptr("console-new")
	h.do("POST", "/admin/v1/templates", in, 201, &tpl)
	h.do("POST", "/admin/v1/templates", in, 409, nil)
	h.do("DELETE", "/admin/v1/templates/"+tpl.Id, nil, 204, nil)

	// ---- connectors
	var cs []gen.AdminConnector
	h.do("GET", "/admin/v1/connectors", nil, 200, &cs)
	if len(cs) == 0 {
		t.Fatal("no connectors")
	}
	var c gen.AdminConnector
	h.do("PATCH", "/admin/v1/connectors/"+cs[0].Id, map[string]any{"available": false}, 200, &c)
	if c.Available || !c.AdminManaged {
		t.Fatalf("connector = %+v", c)
	}
	if _, err := (&connector.Service{Q: h.q}).SeedCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	h.do("GET", "/admin/v1/connectors", nil, 200, &cs)
	if cs[0].Available {
		t.Fatal("seed re-enabled a connector taken down in the console")
	}

	// ---- moderation: a console term blocks text and the block is logged
	var term gen.ModerationTerm
	h.do("POST", "/admin/v1/moderation/terms", map[string]string{"term": "违禁词", "note": "测试"}, 201, &term)
	h.do("POST", "/admin/v1/moderation/terms", map[string]string{"term": "违禁词"}, 409, nil)
	checker := moderation.Recorder{Checker: &moderation.Terms{Q: h.q}, Q: h.q}
	v, err := checker.Check(moderation.WithUser(ctx, u.ID), moderation.Input, "这里有个 违-禁-词")
	if err != nil || !v.Blocked {
		t.Fatalf("term not applied: %+v %v", v, err)
	}
	var evs gen.ModerationEventPage
	h.do("GET", "/admin/v1/moderation/events", nil, 200, &evs)
	if len(evs.Items) != 1 || deref(evs.Items[0].UserId) != u.ID.String() {
		t.Fatalf("events = %+v", evs.Items)
	}
	h.do("DELETE", fmt.Sprintf("/admin/v1/moderation/terms/%d", term.Id), nil, 204, nil)

	var sp gen.SharePage
	h.do("GET", "/admin/v1/shares", nil, 200, &sp)
	if len(sp.Items) != 1 || deref(sp.Items[0].Url) != "https://zeus.test/s/tok123" || sp.Items[0].TargetType != gen.AdminShareTargetTypeReport ||
		sp.Items[0].Title != "新能源调研" || sp.Items[0].TargetId != rep.ID.String() {
		t.Fatalf("shares = %+v", sp.Items)
	}
	h.do("GET", "/admin/v1/shares?type=briefing", nil, 200, &sp)
	if len(sp.Items) != 0 {
		t.Fatalf("type filter: %+v", sp.Items)
	}
	h.do("POST", "/admin/v1/shares/"+share.ID.String()+"/revoke", nil, 204, nil)
	h.do("GET", "/admin/v1/shares", nil, 200, &sp)
	if len(sp.Items) != 0 {
		t.Fatal("revoked share still listed as active")
	}
	h.do("GET", "/admin/v1/reports/"+rep.ID.String(), nil, 200, nil)

	// ---- tasks
	var tasks gen.TaskPage
	h.do("GET", "/admin/v1/tasks?kind=report", nil, 200, &tasks)
	if len(tasks.Items) != 1 || tasks.Items[0].CostCny != 2.5 {
		t.Fatalf("tasks = %+v", tasks.Items)
	}
	var td gen.TaskDetail
	h.do("GET", "/admin/v1/tasks/"+tk.ID.String(), nil, 200, &td)
	if len(td.Calls) != 2 {
		t.Fatalf("task detail = %+v", td)
	}
	h.do("POST", "/admin/v1/tasks/"+tk.ID.String()+"/cancel", nil, 409, nil) // already done
	h.do("POST", "/admin/v1/tasks/"+running.ID.String()+"/cancel", nil, 202, nil)
	if len(h.orch.cancelled) != 1 || h.orch.cancelled[0] != running.ID {
		t.Fatalf("cancelled = %v", h.orch.cancelled)
	}

	// ---- costs and gateway
	var cr gen.CostReport
	h.do("GET", "/admin/v1/costs?days=7", nil, 200, &cr)
	if cr.TotalCny != 2.5 || len(cr.Daily) != 7 || len(cr.ByModel) != 1 || len(cr.TopUsers) != 1 {
		t.Fatalf("costs = %+v", cr)
	}
	var gw gen.ModelGateway
	h.do("GET", "/admin/v1/modelgw", nil, 200, &gw)
	if !gw.Configured || len(gw.Providers) != 2 || len(gw.Routes["strong"]) != 2 || len(gw.Usage24h) != 1 {
		t.Fatalf("modelgw = %+v", gw)
	}
	h.do("GET", "/admin/v1/plans", nil, 200, nil)

	// ---- roles: a viewer can read but not write
	var created gen.AdminWithPassword
	h.do("POST", "/admin/v1/admins", map[string]string{"email": "Viewer@zeus.test", "role": "viewer"}, 201, &created)
	h.do("POST", "/admin/v1/admins", map[string]string{"email": "viewer@zeus.test", "role": "viewer"}, 409, nil)
	ownerToken := h.token
	h.login("viewer@zeus.test", created.Password)
	h.do("GET", "/admin/v1/users", nil, 200, nil)
	h.do("PATCH", "/admin/v1/connectors/"+cs[0].Id, map[string]any{"available": true}, 403, nil)
	h.do("GET", "/admin/v1/admins", nil, 403, nil)

	// disabling an operator ends their session immediately
	viewerToken := h.token
	h.token = ownerToken
	h.do("PATCH", "/admin/v1/admins/"+created.Admin.Id, map[string]any{"disabled": true}, 200, nil)
	h.do("PATCH", "/admin/v1/admins/"+lr.Admin.Id, map[string]any{"role": "viewer"}, 400, nil) // not yourself
	h.token = viewerToken
	h.do("GET", "/admin/v1/me", nil, 401, nil)

	h.token = ownerToken
	var audit gen.AuditPage
	h.do("GET", "/admin/v1/audit-log?targetType=user&targetId="+u.ID.String(), nil, 200, &audit)
	if len(audit.Items) != 5 || audit.Items[0].Action != "user.enable" || audit.Items[0].AdminEmail != "owner@zeus.test" {
		t.Fatalf("audit = %+v", audit.Items)
	}
}

func TestLoginRateLimit(t *testing.T) {
	h := setup(t)
	h.createOwner("owner@zeus.test", "correct-horse-battery")
	for i := 0; i < 5; i++ {
		h.do("POST", "/admin/v1/auth/login", map[string]string{"email": "owner@zeus.test", "password": "nope"}, 401, nil)
	}
	h.do("POST", "/admin/v1/auth/login", map[string]string{"email": "owner@zeus.test", "password": "correct-horse-battery"}, 429, nil)
}

func ptr[T any](v T) *T { return &v }

func deref[T any](p *T) T {
	var z T
	if p == nil {
		return z
	}
	return *p
}
