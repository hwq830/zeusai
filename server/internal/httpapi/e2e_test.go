package httpapi_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	"github.com/hwq830/zeusai/server/internal/orch"
	"github.com/hwq830/zeusai/server/internal/platform/config"
	"github.com/hwq830/zeusai/server/internal/platform/testdb"
	"github.com/hwq830/zeusai/server/internal/platform/testenv"
	"github.com/hwq830/zeusai/server/internal/push"
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
	workflows.Register(w, &workflows.Activities{Q: q, Bus: &events.Bus{R: rdb}, LLM: llm.Mock{}, Search: search.Mock{}, Push: pushes})
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Stop)

	oc := &workflows.Client{C: tc, TaskQueue: queue}
	cfg := config.Config{Env: "dev", JWTSecret: "test", AccessTTL: time.Minute, RefreshTTL: time.Hour, DevSMSCode: smsCode}
	srv := httpapi.New(httpapi.Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Pool: pool, Redis: rdb, Orch: oc, LLM: llm.Mock{}, Config: cfg})
	srv.SSEPing = 200 * time.Millisecond
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	spec, err := gen.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	spec.Servers = nil // match any host
	router, err := gorillamux.NewRouter(spec)
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, router: router, url: hs.URL, orch: oc, pushes: pushes}
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
			Reports               struct{ Used, Limit int } `json:"reports"`
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
	if !s.IsNewUser || !strings.HasPrefix(s.Me.User.PhoneMasked, "138****") || s.Me.Quota.BriefingSubscriptions.Limit != 2 || s.Me.Quota.Reports.Limit != 5 {
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
	if len(h.pushes.Sent) != 0 {
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

	// Another user sees none of it.
	other := *h
	other.login(randomPhone())
	other.do("GET", "/v1/briefings/"+today.Items[0].ID, nil, 404, nil)
	other.do("GET", "/v1/tasks/"+tk.ID, nil, 404, nil)

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
