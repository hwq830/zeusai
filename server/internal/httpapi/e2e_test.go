package httpapi_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/hwq830/zeusai/server/internal/blob"
	"github.com/hwq830/zeusai/server/internal/pdf"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
	"github.com/google/uuid"
	"go.temporal.io/sdk/worker"

	"github.com/hwq830/zeusai/server/internal/connector"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/events"
	"github.com/hwq830/zeusai/server/internal/httpapi"
	"github.com/hwq830/zeusai/server/internal/httpapi/gen"
	"github.com/hwq830/zeusai/server/internal/llm"
	"github.com/hwq830/zeusai/server/internal/moderation"
	"github.com/hwq830/zeusai/server/internal/notify"
	"github.com/hwq830/zeusai/server/internal/orch"
	"github.com/hwq830/zeusai/server/internal/platform/config"
	"github.com/hwq830/zeusai/server/internal/platform/testdb"
	"github.com/hwq830/zeusai/server/internal/platform/testenv"
	"github.com/hwq830/zeusai/server/internal/push"
	"github.com/hwq830/zeusai/server/internal/quota"
	"github.com/hwq830/zeusai/server/internal/search"
	"github.com/hwq830/zeusai/server/internal/template"
	"github.com/hwq830/zeusai/server/internal/workflows"
)

const smsCode = "246810"

type harness struct {
	t      *testing.T
	router routers.Router
	url    string
	token  string
	orch   *workflows.Client
	pushes *push.Log
	q      *db.Queries
	// lang is sent as Accept-Language when set.
	lang string
}

func setup(t *testing.T) *harness {
	pool := testdb.New(t)
	rdb := testenv.Redis(t)
	tc := testenv.Temporal(t)
	ctx := context.Background()
	q := db.New(pool)
	if _, err := (&template.Service{Q: q}).SeedOfficial(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := (&connector.Service{Q: q}).SeedCatalog(ctx); err != nil {
		t.Fatal(err)
	}

	queue := "test-" + uuid.NewString()
	pushes := &push.Log{}
	w := worker.New(tc, queue, worker.Options{})
	amap := fakeMCP(t)
	tools := &connector.Toolbox{Q: q, Endpoint: func(id string) string {
		if id == "amap" {
			return amap.URL
		}
		return ""
	}}
	files := &blob.Local{Dir: t.TempDir(), Secret: []byte("files")}
	var printer pdf.Renderer = fakePDF{}
	if p := os.Getenv("ZEUS_TEST_CHROMIUM"); p != "" {
		printer = pdf.Chromium{Path: p, NoSandbox: true}
	}
	// Pushes go through the message center like in production. Its clock is pinned to noon in
	// Beijing so quiet hours never defer a push mid-test.
	y, m, d := time.Now().Date()
	noon := time.Date(y, m, d, 4, 0, 0, 0, time.UTC)
	notifier := &notify.Service{Q: q, Sender: pushes, Now: func() time.Time { return noon }}
	workflows.Register(w, &workflows.Activities{Q: q, Bus: &events.Bus{R: rdb}, LLM: llm.Metered{Provider: llm.Mock{}, Q: q}, Search: search.Mock{},
		Push: notifier, Tools: tools, PDF: printer, Blob: files, Notify: notifier, Quota: &quota.Service{Q: q}})
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Stop)

	// General API tests use morning schedules and generate manually. Keep their
	// schedule saves outside the catchup window regardless of CI's wall-clock time.
	oc := &workflows.Client{C: tc, TaskQueue: queue, Now: func() time.Time { return noon }}
	cfg := config.Config{Env: "dev", JWTSecret: "test", AccessTTL: time.Minute, RefreshTTL: time.Hour, DevSMSCode: smsCode}
	mod, err := moderation.NewKeywords(strings.NewReader(blockedTerm))
	if err != nil {
		t.Fatal(err)
	}
	srv := httpapi.New(httpapi.Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Pool: pool, Redis: rdb, Orch: oc, LLM: llm.Mock{}, Mod: mod, Blob: files, Config: cfg})
	srv.SSEPing = 200 * time.Millisecond
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	files.BaseURL = hs.URL
	spec, err := gen.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	spec.Servers = nil // match any host
	router, err := gorillamux.NewRouter(spec)
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, router: router, url: hs.URL, orch: oc, pushes: pushes, q: q}
}

// do sends a JSON request and decodes the JSON response into out (if non-nil).
func (h *harness) do(method, path string, body any, wantStatus int, out any) {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.url+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if h.lang != "" {
		req.Header.Set("Accept-Language", h.lang)
	}
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
	h.conform(req, resp, raw)
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			h.t.Fatalf("%s %s: decode: %v: %s", method, path, err, raw)
		}
	}
}

// conform checks the response body against api/openapi.yaml, so the iOS client generated
// from the contract can decode everything the server sends.
func (h *harness) conform(req *http.Request, resp *http.Response, body []byte) {
	h.t.Helper()
	route, params, err := h.router.FindRoute(req)
	if err != nil {
		h.t.Fatalf("%s %s: not in contract: %v", req.Method, req.URL.Path, err)
	}
	in := &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route,
		Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc}}
	if err := openapi3filter.ValidateResponse(context.Background(), &openapi3filter.ResponseValidationInput{
		RequestValidationInput: in, Status: resp.StatusCode, Header: resp.Header, Body: io.NopCloser(bytes.NewReader(body)),
		Options: &openapi3filter.Options{IncludeResponseStatus: true},
	}); err != nil {
		h.t.Fatalf("%s %s: response violates contract: %v\n%s", req.Method, req.URL.Path, err, body)
	}
}

type session struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	IsNewUser    bool   `json:"isNewUser"`
	Me           struct {
		User struct {
			ID          string `json:"id"`
			PhoneMasked string `json:"phoneMasked"`
		} `json:"user"`
		Quota struct {
			BriefingSubscriptions struct{ Used, Limit int } `json:"briefingSubscriptions"`
			Tokens                struct {
				Used, Limit, Remaining int64
			} `json:"tokens"`
		} `json:"quota"`
	} `json:"me"`
}

// randomPhone avoids the per-phone resend limit across test runs sharing one Redis.
func randomPhone() string {
	return fmt.Sprintf("+86138%08d", uuid.New().ID()%100000000)
}

func (h *harness) login(phone string) session {
	h.do("POST", "/v1/auth/sms/send", map[string]string{"phone": phone}, 204, nil)
	var s session
	h.do("POST", "/v1/auth/sms/verify", map[string]string{"phone": phone, "code": smsCode}, 200, &s)
	h.token = s.AccessToken
	return s
}

// streamEvents reads the SSE stream until the connection closes.
func (h *harness) streamEvents(taskID string, lastEventID int64) []events.Event {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", h.url+"/v1/tasks/"+taskID+"/events", nil)
	req.Header.Set("Authorization", "Bearer "+h.token)
	if lastEventID > 0 {
		req.Header.Set("Last-Event-ID", fmt.Sprint(lastEventID))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		h.t.Fatalf("content type %q", ct)
	}
	var out []events.Event
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var id string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "id: "):
			id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "data: "):
			var ev events.Event
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
				h.t.Fatal(err)
			}
			if fmt.Sprint(ev.Seq) != id {
				h.t.Fatalf("SSE id %s does not match seq %d", id, ev.Seq)
			}
			out = append(out, ev)
		}
	}
	return out
}

type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	Details   map[string]any
}

func TestBriefingEndToEnd(t *testing.T) {
	h := setup(t)
	ctx := context.Background()

	var e apiError
	h.do("GET", "/v1/me", nil, 401, &e)
	if e.Code != "unauthenticated" {
		t.Fatalf("got %+v", e)
	}

	s := h.login(randomPhone())
	if !s.IsNewUser || !strings.HasPrefix(s.Me.User.PhoneMasked, "138****") || s.Me.Quota.BriefingSubscriptions.Limit != 2 || s.Me.Quota.Tokens.Limit != 7_500_000 {
		t.Fatalf("unexpected session: %+v", s)
	}

	var tl struct {
		Items []struct {
			ID     string                 `json:"id"`
			Name   string                 `json:"name"`
			Kind   string                 `json:"kind"`
			Fields []struct{ Key string } `json:"fields"`
		} `json:"items"`
		Categories []string `json:"categories"`
	}
	h.do("GET", "/v1/templates", nil, 200, &tl)
	if len(tl.Items) != 5 || len(tl.Categories) == 0 || tl.Items[0].Kind != "official" {
		t.Fatalf("templates: %+v", tl)
	}
	var companyWatch string
	for _, it := range tl.Items {
		if it.Name == "公司追踪" {
			companyWatch = it.ID
		}
	}

	sched := map[string]any{"freq": "daily", "weekdays": []int{1, 2, 3, 4, 5}, "time": "08:00", "tz": "Asia/Shanghai"}
	h.do("POST", "/v1/briefing-subscriptions", map[string]any{"templateId": companyWatch, "name": "盯公司", "schedule": sched}, 400, &e)
	if !strings.Contains(e.Message, "要追踪的公司") {
		t.Fatalf("required field not enforced: %+v", e)
	}

	type sub struct {
		ID        string     `json:"id"`
		Status    string     `json:"status"`
		NextRunAt *time.Time `json:"nextRunAt"`
	}
	var s1, s2 sub
	h.do("POST", "/v1/briefing-subscriptions", map[string]any{
		"templateId": companyWatch, "name": "盯公司", "schedule": sched,
		"fieldValues": map[string]any{"companies": []string{"宁德时代", "比亚迪"}, "aspects": []string{"财报与业绩"}},
	}, 201, &s1)
	if s1.Status != "active" || s1.NextRunAt == nil {
		t.Fatalf("subscription: %+v", s1)
	}
	desc, err := h.orch.C.ScheduleClient().GetHandle(ctx, orch.ScheduleID(uuid.MustParse(s1.ID))).Describe(ctx)
	if err != nil {
		t.Fatalf("temporal schedule missing: %v", err)
	}
	if cal := desc.Schedule.Spec.Calendars[0]; cal.Hour[0].Start != 7 || cal.Minute[0].Start != 30 || desc.Schedule.Spec.TimeZoneName != "Asia/Shanghai" {
		t.Fatalf("schedule spec: %+v", desc.Schedule.Spec)
	}

	h.do("POST", "/v1/briefing-subscriptions", map[string]any{"name": "AI 早报", "topics": []string{"大模型", "AI 芯片"}, "schedule": map[string]any{"freq": "daily", "time": "07:30", "tz": "Asia/Shanghai"}}, 201, &s2)
	h.do("POST", "/v1/briefing-subscriptions", map[string]any{"name": "第三个", "topics": []string{"x"}, "schedule": sched}, 403, &e)
	if e.Code != "quota_exceeded" {
		t.Fatalf("quota: %+v", e)
	}

	// Run now and follow the SSE stream to completion.
	var tk struct {
		ID          string `json:"id"`
		Kind        string `json:"kind"`
		Status      string `json:"status"`
		Subtitle    string `json:"subtitle"`
		LastEventID int64  `json:"lastEventId"`
		BriefingID  string `json:"briefingId"`
		Plan        []events.Step
	}
	h.do("POST", "/v1/briefing-subscriptions/"+s1.ID+"/run", nil, 202, &tk)
	if tk.Kind != "briefing" || tk.Subtitle != "立即生成" {
		t.Fatalf("task: %+v", tk)
	}
	evs := h.streamEvents(tk.ID, 0)
	types := map[string]int{}
	for i, ev := range evs {
		types[ev.Type]++
		if ev.Seq != int64(i+1) {
			t.Fatalf("seq gap at %d: %+v", i, ev)
		}
	}
	last := evs[len(evs)-1]
	if last.Type != events.TypeCompleted || last.BriefingID == "" {
		t.Fatalf("last event: %+v (all: %v)", last, types)
	}
	if types[events.TypePlan] != 1 || types[events.TypeStepStarted] != 3 || types[events.TypeStepDone] != 3 || types[events.TypeSourceReading] == 0 {
		t.Fatalf("event mix: %v", types)
	}

	// lastEventId is bumped right after each publish, so it can trail the stream briefly.
	for i := 0; i < 20; i++ {
		h.do("GET", "/v1/tasks/"+tk.ID, nil, 200, &tk)
		if tk.LastEventID == last.Seq {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if tk.Status != "done" || tk.LastEventID != last.Seq || tk.BriefingID != last.BriefingID || len(tk.Plan) != 3 {
		t.Fatalf("task after run: %+v", tk)
	}
	if again := h.streamEvents(tk.ID, tk.LastEventID); len(again) != 0 {
		t.Fatalf("resume after end should send nothing, got %d", len(again))
	}
	if replay := h.streamEvents(tk.ID, 2); len(replay) != len(evs)-2 {
		t.Fatalf("resume from 2: got %d events, want %d", len(replay), len(evs)-2)
	}

	var today struct {
		Date  string `json:"date"`
		Items []struct {
			ID       string `json:"id"`
			Status   string `json:"status"`
			Headline string `json:"headline"`
			Sections []struct {
				Title string `json:"title"`
				Items []struct {
					ID          string `json:"id"`
					Sources     []events.Source
					DeepDivable bool `json:"deepDivable"`
				} `json:"items"`
			} `json:"sections"`
		} `json:"items"`
		Upcoming []sub `json:"upcoming"`
	}
	h.do("GET", "/v1/briefings/today", nil, 200, &today)
	if len(today.Items) != 1 || today.Items[0].Status != "ready" || today.Items[0].Headline == "" || len(today.Items[0].Sections) == 0 {
		t.Fatalf("today: %+v", today)
	}
	it := today.Items[0].Sections[0].Items[0]
	if len(it.Sources) == 0 || !strings.HasPrefix(it.Sources[0].URL, "https://") || !it.DeepDivable {
		t.Fatalf("item: %+v", it)
	}
	if len(h.pushes.Notifications()) != 0 {
		t.Fatal("manual preview must not push")
	}

	h.do("PUT", "/v1/briefings/"+today.Items[0].ID+"/feedback", map[string]any{"rating": "useful"}, 204, nil)
	var b struct {
		Feedback string     `json:"feedback"`
		ReadAt   *time.Time `json:"readAt"`
	}
	h.do("GET", "/v1/briefings/"+today.Items[0].ID, nil, 200, &b)
	h.do("GET", "/v1/briefings/"+today.Items[0].ID, nil, 200, &b)
	if b.Feedback != "useful" || b.ReadAt == nil {
		t.Fatalf("briefing: %+v", b)
	}
	var list struct {
		Items []struct{ ItemCount int } `json:"items"`
	}
	h.do("GET", "/v1/briefings?subscriptionId="+s1.ID, nil, 200, &list)
	if len(list.Items) != 1 || list.Items[0].ItemCount == 0 {
		t.Fatalf("history: %+v", list)
	}

	// Pause, then delete: the Temporal schedule follows.
	h.do("PATCH", "/v1/briefing-subscriptions/"+s1.ID, map[string]any{"paused": true}, 200, &s1)
	desc, _ = h.orch.C.ScheduleClient().GetHandle(ctx, orch.ScheduleID(uuid.MustParse(s1.ID))).Describe(ctx)
	if s1.Status != "paused" || !desc.Schedule.State.Paused {
		t.Fatalf("pause: %+v %+v", s1, desc.Schedule.State)
	}
	h.do("DELETE", "/v1/briefing-subscriptions/"+s1.ID, nil, 204, nil)
	if _, err := h.orch.C.ScheduleClient().GetHandle(ctx, orch.ScheduleID(uuid.MustParse(s1.ID))).Describe(ctx); err == nil {
		t.Fatal("schedule should be deleted")
	}

	// Removing a subscription frees its quota but preserves all generated issues.
	h.do("GET", "/v1/briefings?subscriptionId="+s1.ID, nil, 200, &list)
	if len(list.Items) != 1 || list.Items[0].ItemCount == 0 {
		t.Fatalf("deleted subscription lost history: %+v", list)
	}
	h.do("GET", "/v1/briefings/"+today.Items[0].ID, nil, 200, nil)
	h.do("PATCH", "/v1/briefing-subscriptions/"+s1.ID, map[string]any{"paused": false}, 404, nil)
	var options struct {
		Items []struct {
			ID      string
			Deleted bool
		}
	}
	h.do("GET", "/v1/briefings/subscriptions", nil, 200, &options)
	if len(options.Items) != 1 || options.Items[0].ID != s1.ID || !options.Items[0].Deleted {
		t.Fatalf("history filter lost deleted subscription: %+v", options)
	}
	h.do("GET", "/v1/briefings?unreadOnly=true", nil, 200, &list)
	if len(list.Items) != 0 {
		t.Fatalf("read issue in unread list: %+v", list)
	}
	h.do("GET", "/v1/briefings?dateFrom=2099-01-01", nil, 200, &list)
	if len(list.Items) != 0 {
		t.Fatalf("date filter ignored: %+v", list)
	}
	h.do("GET", "/v1/briefings?dateFrom=2026-11-02&dateTo=2026-11-01", nil, 400, nil)
	// Deletion is retryable when removing the external schedule previously failed.
	h.do("DELETE", "/v1/briefing-subscriptions/"+s1.ID, nil, 204, nil)
	var afterDelete struct {
		User  struct{ ID string }
		Quota struct{ BriefingSubscriptions struct{ Used int } }
	}
	h.do("GET", "/v1/me", nil, 200, &afterDelete)
	if afterDelete.Quota.BriefingSubscriptions.Used != 1 {
		t.Fatalf("deleted subscription still consumes quota: %+v", afterDelete)
	}

	// Another user sees none of it.
	other := *h
	other.login(randomPhone())
	other.do("GET", "/v1/briefings/"+today.Items[0].ID, nil, 404, nil)
	other.do("GET", "/v1/tasks/"+tk.ID, nil, 404, nil)
	other.do("GET", "/v1/briefings/subscriptions", nil, 200, &options)
	if len(options.Items) != 0 {
		t.Fatalf("other user sees history subscriptions: %+v", options)
	}

	// Refresh rotates the token; the old one stops working.
	var s3 session
	h.do("POST", "/v1/auth/refresh", map[string]string{"refreshToken": s.RefreshToken}, 200, &s3)
	h.do("POST", "/v1/auth/refresh", map[string]string{"refreshToken": s.RefreshToken}, 401, nil)
	if s3.IsNewUser || s3.Me.User.ID != s.Me.User.ID {
		t.Fatalf("refresh: %+v", s3)
	}

	var cs struct {
		Items []struct {
			ID       string `json:"id"`
			Enabled  bool   `json:"enabled"`
			Requires bool   `json:"requiresAuth"`
		} `json:"items"`
	}
	h.do("GET", "/v1/connectors", nil, 200, &cs)
	if len(cs.Items) < 3 || !cs.Items[0].Enabled {
		t.Fatalf("connectors: %+v", cs)
	}
	h.do("PATCH", "/v1/connectors/github", map[string]any{"enabled": true}, 409, &e)
	if e.Code != "auth_required" {
		t.Fatalf("github without auth: %+v", e)
	}

	h.do("DELETE", "/v1/me", nil, 204, nil)
	h.do("GET", "/v1/me", nil, 401, nil)
	if _, err := h.orch.C.ScheduleClient().GetHandle(ctx, orch.ScheduleID(uuid.MustParse(s2.ID))).Describe(ctx); err == nil {
		t.Fatal("account deletion must remove schedules")
	}
}

func TestDraftSubscription(t *testing.T) {
	h := setup(t)
	h.login(randomPhone())
	var in struct {
		Name     string   `json:"name"`
		Topics   []string `json:"topics"`
		Schedule struct {
			Freq string `json:"freq"`
			Time string `json:"time"`
			TZ   string `json:"tz"`
		} `json:"schedule"`
	}
	h.do("POST", "/v1/briefing-subscriptions/draft", map[string]string{"prompt": "每天早上 8 点给我简报：科技新闻 + 我关注的公司"}, 200, &in)
	if in.Schedule.Time != "08:00" || in.Schedule.TZ != "Asia/Shanghai" || len(in.Topics) != 2 {
		t.Fatalf("draft: %+v", in)
	}
}

func (h *harness) raw(path string, auth bool) (int, string) {
	h.t.Helper()
	req, _ := http.NewRequest("GET", h.url+path, nil)
	if auth {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestReportEndToEnd(t *testing.T) {
	h := setup(t)
	h.login(randomPhone())

	type taskT struct {
		ID       string `json:"id"`
		Kind     string `json:"kind"`
		Subtitle string `json:"subtitle"`
		ReportID string `json:"reportId"`
		Status   string `json:"status"`
	}
	var tk taskT
	var e apiError
	h.do("POST", "/v1/tasks", map[string]any{"kind": "report", "topic": ""}, 400, &e)
	h.do("POST", "/v1/tasks", map[string]any{"kind": "report", "topic": "2026 年中国新能源车出海", "depth": "quick", "focuses": []string{"市场规模", "竞品对比"}}, 202, &tk)
	if tk.Kind != "report" || tk.ReportID == "" || tk.Subtitle != "快速报告 · 侧重市场规模、竞品对比" {
		t.Fatalf("task: %+v", tk)
	}
	evs := h.streamEvents(tk.ID, 0)
	var plan []events.Step
	var summary strings.Builder
	for _, ev := range evs {
		if ev.Type == events.TypePlan {
			plan = ev.Steps
		}
		if ev.Type == events.TypeTextDelta {
			summary.WriteString(ev.Delta)
		}
	}
	if last := evs[len(evs)-1]; last.Type != events.TypeCompleted || last.ReportID != tk.ReportID {
		t.Fatalf("last: %+v", last)
	}
	if len(plan) != 4 || plan[3].Title != "撰写报告" || summary.Len() == 0 {
		t.Fatalf("plan %+v summary %q", plan, summary.String())
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if ns := h.pushes.Notifications(); len(ns) == 1 && ns[0].DeepLink == "zeus://report/"+tk.ReportID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("report push: %+v", h.pushes.Notifications())
		}
	}
	// The push opens a message-center entry.
	var inbox struct {
		Items []struct {
			ID       string `json:"id"`
			Category string `json:"category"`
			Deeplink string `json:"deeplink"`
		} `json:"items"`
		UnreadCount int `json:"unreadCount"`
	}
	h.do("GET", "/v1/messages", nil, 200, &inbox)
	if len(inbox.Items) != 1 || inbox.Items[0].Category != "report" || inbox.Items[0].ID != h.pushes.Notifications()[0].MessageID || inbox.UnreadCount != 1 {
		t.Fatalf("inbox = %+v", inbox)
	}

	var rep struct {
		ID          string `json:"id"`
		Status      string `json:"status"`
		Title       string `json:"title"`
		Summary     string `json:"summary"`
		SourceCount int    `json:"sourceCount"`
		HTMLURL     string `json:"htmlUrl"`
	}
	h.do("GET", "/v1/reports/"+tk.ReportID, nil, 200, &rep)
	if rep.Status != "done" || rep.SourceCount == 0 || rep.HTMLURL != "/v1/reports/"+rep.ID+"/html" || rep.Summary != summary.String() {
		t.Fatalf("report: %+v", rep)
	}
	code, page := h.raw(rep.HTMLURL, true)
	if code != 200 || !strings.Contains(page, "<h1>"+rep.Title+"</h1>") || !strings.Contains(page, `href="#src-1"`) || !strings.Contains(page, "<table>") {
		t.Fatalf("html %d: %.400s", code, page)
	}
	if code, _ := h.raw(rep.HTMLURL, false); code != 401 {
		t.Fatalf("html without token: %d", code)
	}
	var list struct {
		Items []struct{ ID string } `json:"items"`
	}
	h.do("GET", "/v1/reports?status=done", nil, 200, &list)
	if len(list.Items) != 1 {
		t.Fatalf("list: %+v", list)
	}

	// Follow-up streams an answer and appends it to the report.
	h.do("POST", "/v1/reports/"+rep.ID+"/followups", map[string]string{"question": "欧洲市场的关税影响多大？"}, 202, &tk)
	answer := ""
	for _, ev := range h.streamEvents(tk.ID, 0) {
		answer += ev.Delta
	}
	if answer == "" {
		t.Fatal("no streamed answer")
	}
	if _, page = h.raw(rep.HTMLURL, true); !strings.Contains(page, "追问：欧洲市场的关税影响多大？") {
		t.Fatal("follow-up not appended")
	}

	// Shares: full and summary-only, public, revocable.
	var full, sum struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	h.do("POST", "/v1/reports/"+rep.ID+"/shares", map[string]string{"scope": "full"}, 201, &full)
	h.do("POST", "/v1/reports/"+rep.ID+"/shares", map[string]string{"scope": "summary"}, 201, &sum)
	path := func(u string) string { return u[strings.Index(u, "/s/"):] }
	if code, page := h.raw(path(full.URL), false); code != 200 || !strings.Contains(page, "来源") || !strings.Contains(page, "og:title") {
		t.Fatalf("full share %d", code)
	}
	if code, page := h.raw(path(sum.URL), false); code != 200 || strings.Contains(page, "<article>") || !strings.Contains(page, rep.Summary) {
		t.Fatalf("summary share %d: %.300s", code, page)
	}
	h.do("DELETE", "/v1/shares/"+full.ID, nil, 204, nil)
	if code, _ := h.raw(path(full.URL), false); code != 404 {
		t.Fatalf("revoked share: %d", code)
	}

	h.do("DELETE", "/v1/reports/"+rep.ID, nil, 204, nil)
	h.do("GET", "/v1/reports/"+rep.ID, nil, 404, nil)
}

func TestReportQuota(t *testing.T) {
	h := setup(t)
	h.login(randomPhone())
	var tk struct{ ID, ReportID string }
	h.do("POST", "/v1/tasks", map[string]any{"kind": "report", "topic": "题目", "depth": "quick"}, 202, &tk)
	h.streamEvents(tk.ID, 0)
	var me session
	h.do("GET", "/v1/me", nil, 200, &me.Me)
	used := me.Me.Quota.Tokens.Used
	if used <= 0 || me.Me.Quota.Tokens.Limit != 7_500_000 {
		t.Fatalf("quota after one report: %+v", me.Me.Quota)
	}
	var rep struct{ TokensUsed int64 }
	h.do("GET", "/v1/reports/"+tk.ReportID, nil, 200, &rep)
	if rep.TokensUsed != used {
		t.Fatalf("report tokens = %d, quota used = %d", rep.TokensUsed, used)
	}

	// Leave less than one report's worth (500k) of the 7.5M allowance.
	uid := uuid.MustParse(me.Me.User.ID)
	if err := h.q.RecordModelUsage(context.Background(), db.RecordModelUsageParams{UserID: &uid, Provider: "p", Model: "m",
		Tier: "strong", BilledTokens: 7_100_000 - used}); err != nil {
		t.Fatal(err)
	}
	var e apiError
	h.do("POST", "/v1/tasks", map[string]any{"kind": "report", "topic": "第二份"}, 403, &e)
	if e.Code != "quota_exceeded" || e.Details["remaining"] != float64(400_000) {
		t.Fatalf("got %+v", e)
	}
	var usage struct {
		Quota struct{ Used, Remaining int64 }
		Daily []struct{ Tokens int64 }
		Tasks []struct {
			TaskID  string
			Tokens  int64
			Counted bool
		}
	}
	h.do("GET", "/v1/me/usage", nil, 200, &usage)
	if usage.Quota.Used != 7_100_000 || len(usage.Daily) != 1 || len(usage.Tasks) != 1 || usage.Tasks[0].TaskID != tk.ID || !usage.Tasks[0].Counted {
		t.Fatalf("usage = %+v", usage)
	}

	// Follow-ups still work while tokens remain, and the first task to finish past 80% sends
	// one reminder.
	var fu struct{ ID string }
	h.do("POST", "/v1/reports/"+tk.ReportID+"/followups", map[string]string{"question": "再说说风险"}, 202, &fu)
	h.streamEvents(fu.ID, 0)
	reminders := func() (n []push.Notification) {
		for _, p := range h.pushes.Notifications() {
			if p.DeepLink == "zeus://usage" {
				n = append(n, p)
			}
		}
		return n
	}
	for deadline := time.Now().Add(5 * time.Second); len(reminders()) == 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("no quota reminder: %+v", h.pushes.Notifications())
		}
	}
	if r := reminders(); len(r) != 1 || r[0].Title != "本月 token 额度已用 80%" {
		t.Fatalf("reminders = %+v", r)
	}
}

func TestDraftTemplate(t *testing.T) {
	h := setup(t)
	h.login(randomPhone())
	var in struct {
		Name     string                   `json:"name"`
		Sections []struct{ Title string } `json:"sections"`
	}
	h.do("POST", "/v1/templates/draft", map[string]string{"prompt": "每天汇总我关注的几个关键词的新闻"}, 200, &in)
	if in.Name == "" || len(in.Sections) == 0 {
		t.Fatalf("draft: %+v", in)
	}
	var created struct{ ID, Kind string }
	h.do("POST", "/v1/templates", in, 201, &created)
	if created.Kind != "custom" {
		t.Fatalf("created: %+v", created)
	}
}

const blockedTerm = "违禁测试词"

func TestContentBlocked(t *testing.T) {
	h := setup(t)
	h.login(randomPhone())
	var e apiError
	// Spaces and punctuation between characters do not get past the check.
	h.do("POST", "/v1/tasks", map[string]any{"kind": "report", "topic": "关于 违禁-测试 词 的调研"}, 422, &e)
	if e.Code != "content_blocked" {
		t.Fatalf("report: %+v", e)
	}
	h.do("POST", "/v1/briefing-subscriptions/draft", map[string]string{"prompt": "每天推送" + blockedTerm}, 422, &e)
	h.do("POST", "/v1/templates/draft", map[string]string{"prompt": blockedTerm}, 422, &e)
	var me session
	h.do("GET", "/v1/me", nil, 200, &me.Me)
	if me.Me.Quota.Tokens.Used != 0 {
		t.Fatalf("blocked report was charged: %+v", me.Me.Quota)
	}
}

// fakeMCP is a stateless MCP server offering amap's place_search plus a write tool that the
// catalog does not list, which must never be offered.
func fakeMCP(t *testing.T) *httptest.Server {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     *int64 `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any = map[string]any{}
		switch req.Method {
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{
				// Listed first: the mock model picks the first offered tool, so offering it would fail the test.
				{"name": "delete_everything", "inputSchema": map[string]any{"type": "object"}},
				{"name": "place_search", "description": "搜地点", "inputSchema": map[string]any{"type": "object"}},
			}}
		case "tools/call":
			result = map[string]any{"content": []map[string]string{{"type": "text", "text": fmt.Sprintf("%s 结果：%v", req.Params.Name, req.Params.Arguments["query"])}}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": result})
	}))
	t.Cleanup(s.Close)
	return s
}

func TestReportUsesConnectorTool(t *testing.T) {
	h := setup(t)
	h.login(randomPhone())
	run := func() []events.Event {
		var tk struct{ ID string }
		h.do("POST", "/v1/tasks", map[string]any{"kind": "report", "topic": "上海周末亲子去处", "depth": "quick"}, 202, &tk)
		return h.streamEvents(tk.ID, 0)
	}
	mcpSources := func(evs []events.Event) (n int) {
		for _, ev := range evs {
			if ev.Type == events.TypeSourceReading && strings.HasPrefix(ev.Source.URL, "mcp://") {
				if ev.Source.URL != "mcp://amap/place_search" {
					t.Fatalf("unexpected tool source %+v", ev.Source)
				}
				n++
			}
		}
		return n
	}
	if n := mcpSources(run()); n != 0 {
		t.Fatalf("connector is off by default, got %d tool sources", n)
	}
	h.do("PATCH", "/v1/connectors/amap", map[string]any{"enabled": true}, 200, nil)
	if n := mcpSources(run()); n != 3 { // one call per sub-question of a quick report
		t.Fatalf("tool sources: %d", n)
	}
}

type fakePDF struct{}

func (fakePDF) Render(_ context.Context, html []byte) ([]byte, error) {
	return append([]byte("%PDF-fake\n"), html...), nil
}

func TestReportPDF(t *testing.T) {
	h := setup(t)
	h.login(randomPhone())
	var tk struct{ ID, ReportID string }
	h.do("POST", "/v1/tasks", map[string]any{"kind": "report", "topic": "PDF 导出测试", "depth": "quick"}, 202, &tk)
	var link struct {
		Status    string     `json:"status"`
		URL       string     `json:"url"`
		ExpiresAt *time.Time `json:"expiresAt"`
	}
	var e apiError
	h.do("GET", "/v1/reports/"+tk.ReportID+"/pdf", nil, 409, &e) // still running
	h.streamEvents(tk.ID, 0)

	waitReady := func() string {
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
			h.do("GET", "/v1/reports/"+tk.ReportID+"/pdf", nil, 200, &link)
			if link.Status == "ready" {
				return link.URL
			}
		}
		t.Fatalf("PDF never ready: %+v", link)
		return ""
	}
	h.do("GET", "/v1/reports/"+tk.ReportID+"/pdf", nil, 200, &link)
	if link.Status != "generating" {
		t.Fatalf("first request: %+v", link)
	}
	first := waitReady()
	if link.ExpiresAt == nil || time.Until(*link.ExpiresAt) > 16*time.Minute {
		t.Fatalf("expiry: %+v", link)
	}
	code, body := h.raw(strings.TrimPrefix(first, h.url), false)
	if code != 200 || !strings.HasPrefix(body, "%PDF") {
		t.Fatalf("download: %d %.40q", code, body)
	}
	if code, _ := h.raw(strings.TrimPrefix(first, h.url)+"x", false); code != 403 {
		t.Fatalf("tampered link: %d", code)
	}

	// A follow-up changes the report, so the next request prints a new file.
	var fu struct{ ID string }
	h.do("POST", "/v1/reports/"+tk.ReportID+"/followups", map[string]string{"question": "补充一点？"}, 202, &fu)
	h.streamEvents(fu.ID, 0)
	h.do("GET", "/v1/reports/"+tk.ReportID+"/pdf", nil, 200, &link)
	if link.Status != "generating" {
		t.Fatalf("after follow-up: %+v", link)
	}
	if second := waitReady(); second == first {
		t.Fatal("stale PDF link returned after follow-up")
	}

	// Other users cannot reach the report.
	h.login(randomPhone())
	h.do("GET", "/v1/reports/"+tk.ReportID+"/pdf", nil, 404, &e)
}
