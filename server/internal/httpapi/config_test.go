package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/routers/gorillamux"

	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/httpapi"
	"github.com/hwq830/zeusai/server/internal/httpapi/gen"
	"github.com/hwq830/zeusai/server/internal/llm"
	"github.com/hwq830/zeusai/server/internal/platform/config"
	"github.com/hwq830/zeusai/server/internal/platform/testdb"
	"github.com/hwq830/zeusai/server/internal/platform/testenv"
	"github.com/hwq830/zeusai/server/internal/remoteconfig"
)

func val(v gen.RemoteConfigValue) any {
	if v.Value == nil {
		return nil
	}
	return *v.Value
}

// getConfig calls GET /v1/config with the App's headers and checks the response against the contract.
func (h *harness) getConfig(headers map[string]string, wantStatus int) (gen.RemoteConfig, http.Header) {
	h.t.Helper()
	req, _ := http.NewRequest("GET", h.url+"/v1/config", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
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
		h.t.Fatalf("GET /v1/config: status %d, want %d: %s", resp.StatusCode, wantStatus, raw)
	}
	h.conform(req, resp, raw)
	var out gen.RemoteConfig
	if wantStatus == 200 {
		if err := json.Unmarshal(raw, &out); err != nil {
			h.t.Fatal(err)
		}
	}
	return out, resp.Header
}

func TestRemoteConfig(t *testing.T) {
	pool := testdb.New(t)
	rdb := testenv.Redis(t)
	ctx := context.Background()
	q := db.New(pool)
	cfg := config.Config{Env: "dev", Region: "china", JWTSecret: "test", AccessTTL: time.Minute, RefreshTTL: time.Hour, DevSMSCode: smsCode}
	srv := httpapi.New(httpapi.Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Pool: pool, Redis: rdb, LLM: llm.Mock{}, Config: cfg})
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
	h := &harness{t: t, router: router, url: hs.URL}

	// Before any release the App gets version 0 and no values: it uses its built-in defaults.
	rc, hdr := h.getConfig(nil, 200)
	if rc.Version != 0 || len(rc.Values) != 0 || hdr.Get("X-Zeus-Config-Version") != "0" {
		t.Fatalf("empty config = %+v, header %q", rc, hdr.Get("X-Zeus-Config-Version"))
	}

	// Publish: built-in keys, a server-only key, and rules on install, user and app version.
	if _, err := remoteconfig.SeedBuiltins(ctx, q); err != nil {
		t.Fatal(err)
	}
	admin, err := q.CreateAdminUser(ctx, db.CreateAdminUserParams{Email: "o@zeus.test", PasswordHash: "x", Role: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.InsertConfigKey(ctx, db.InsertConfigKeyParams{Key: "model.split_percent", Namespace: "quota", Type: "int",
		DefaultValue: json.RawMessage("10"), ApplyMode: "immediate", ClientVisible: false}); err != nil {
		t.Fatal(err)
	}
	setRules := func(key string, rules string) {
		t.Helper()
		if _, err := q.UpdateConfigKey(ctx, db.UpdateConfigKeyParams{Key: key, Rules: json.RawMessage(rules), UpdatedBy: &admin.ID}); err != nil {
			t.Fatal(err)
		}
	}
	setRules(remoteconfig.KeyHomeNotice, `[{"id":"tf","when":[{"attr":"channel","op":"eq","values":["testflight"]}],"value":{"text":"内测版","level":"info"}}]`)
	setRules(remoteconfig.KeyQuotaTokensPerMonth, `[{"id":"all","when":[],"value":7000000}]`)
	rel := &remoteconfig.Releases{Pool: pool, Redis: rdb}
	publish := func() {
		t.Helper()
		r, _, err := rel.Submit(ctx, remoteconfig.Actor{ID: admin.ID, Role: "owner"}, "")
		if err != nil || r.Status != "published" {
			t.Fatalf("publish: %+v %v", r, err)
		}
		if err := srv.RemoteConfig.Load(ctx); err != nil {
			t.Fatal(err)
		}
	}
	publish()

	app := map[string]string{"X-Zeus-Install-Id": "install-1", "X-Zeus-App-Version": "1.0.0", "X-Zeus-Platform": "ios", "X-Zeus-Channel": "testflight"}
	rc, hdr = h.getConfig(app, 200)
	if rc.Version != 1 || rc.TtlSeconds != 3600 || hdr.Get("X-Zeus-Config-Version") != "1" {
		t.Fatalf("config v1 = %+v", rc)
	}
	if _, ok := rc.Values["model.split_percent"]; ok {
		t.Fatal("server-only key sent to the App")
	}
	notice := rc.Values[remoteconfig.KeyHomeNotice]
	if notice.RuleId == nil || *notice.RuleId != "tf" || notice.ApplyMode != gen.Immediate {
		t.Fatalf("notice = %+v", notice)
	}
	if v := rc.Values[remoteconfig.KeyTemplateSharing]; v.Expose == nil || !*v.Expose || val(v) != false {
		t.Fatalf("template sharing = %+v", v)
	}
	if v := rc.Values[remoteconfig.KeyQuotaTokensPerMonth]; val(v) != float64(7000000) {
		t.Fatalf("quota = %+v", v)
	}

	// Same caller, same version: 304.
	etag := hdr.Get("ETag")
	app["If-None-Match"] = etag
	_, hdr = h.getConfig(app, 304)
	if hdr.Get("ETag") != etag {
		t.Fatalf("304 etag %q != %q", hdr.Get("ETag"), etag)
	}
	// A different channel changes the values, so the ETag no longer matches.
	app["X-Zeus-Channel"] = "appstore"
	rc, _ = h.getConfig(app, 200)
	if v := rc.Values[remoteconfig.KeyHomeNotice]; val(v) != nil || v.RuleId != nil {
		t.Fatalf("appstore notice = %+v", v)
	}

	// Logged in: user targeting, quotas from config, and the kill switch enforced on the server.
	s := h.login(randomPhone())
	if s.Me.Quota.Tokens.Limit != 7_000_000 {
		t.Fatalf("me quota = %+v", s.Me.Quota)
	}
	setRules(remoteconfig.KeyReportCreateEnabled, `[{"id":"blocked","when":[{"attr":"user_id","op":"in","values":["`+s.Me.User.ID+`"]}],"value":false}]`)
	publish()
	delete(app, "If-None-Match")
	rc, hdr = h.getConfig(app, 200)
	if v := rc.Values[remoteconfig.KeyReportCreateEnabled]; val(v) != false || v.RuleId == nil || hdr.Get("X-Zeus-Config-Version") != "2" {
		t.Fatalf("user kill switch = %+v", v)
	}
	h.do("POST", "/v1/tasks", map[string]any{"kind": "report", "topic": "新能源车"}, 503, nil)
	h.token = "not-a-token" // an invalid token on an open operation is ignored
	rc, _ = h.getConfig(app, 200)
	if v := rc.Values[remoteconfig.KeyReportCreateEnabled]; val(v) != true {
		t.Fatalf("anonymous kill switch = %+v", v)
	}
}
