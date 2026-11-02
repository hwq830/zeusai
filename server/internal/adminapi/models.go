package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/calllog"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/llm"
	"github.com/hwq830/zeusai/server/internal/modelgw"
)

// routeStep mirrors gen.RouteStep.
type routeStep struct {
	Provider    string         `json:"provider"`
	Model       string         `json:"model"`
	UsesDefault bool           `json:"usesDefault"`
	Available   bool           `json:"available"`
	KeyPresent  bool           `json:"keyPresent"`
	Priced      bool           `json:"priced"`
	Price       *modelgw.Price `json:"price,omitempty"`
}

type purposeRoute struct {
	Scope     string         `json:"scope"`
	Tier      llm.Tier       `json:"tier"`
	Override  map[string]any `json:"override,omitempty"`
	Effective []routeStep    `json:"effective"`
}

type tierRoute struct {
	Tier  llm.Tier    `json:"tier"`
	Steps []routeStep `json:"steps"`
}

// ensureRouting moves the routing file into the database if no process has yet (the api and
// orchestrator do the same at start).
func (s *Server) ensureRouting(ctx context.Context) {
	if s.ModelGWConfig == "" {
		return
	}
	if _, err := s.Q.GetGatewaySettings(ctx); err == nil {
		return
	}
	cfg, err := modelgw.LoadConfig(s.ModelGWConfig)
	if err != nil {
		return
	}
	if err := modelgw.Bootstrap(ctx, s.Q, cfg, s.Log); err != nil && s.Log != nil {
		s.Log.Warn("model routing import failed", "err", err)
	}
}

// gatewayConfig lists the enabled model providers, as the gateway sees them.
func (s *Server) gatewayConfig(ctx context.Context) (modelgw.Config, error) {
	s.ensureRouting(ctx)
	rows, err := s.Q.ListModelProviders(ctx)
	if err != nil {
		return modelgw.Config{}, apperr.Internal(err)
	}
	var cfg modelgw.Config
	for _, r := range rows {
		if r.ModelProvider.Enabled {
			cfg.Providers = append(cfg.Providers, modelgw.ConsoleProvider(r.ModelProvider))
		}
	}
	return cfg, nil
}

func providerConfig(cfg modelgw.Config, name string) (modelgw.ProviderConfig, bool) {
	for _, p := range cfg.Providers {
		if p.Name == name {
			return p, true
		}
	}
	return modelgw.ProviderConfig{}, false
}

// meteredPrice is the price cost metering uses for provider's model: the pricing page's admin
// price, else on Bailian endpoints the synced or built-in list price.
func meteredPrice(pc modelgw.ProviderConfig, model string, pl priceList) (modelgw.Price, bool) {
	for _, r := range pl.Models {
		if r.Model != model {
			continue
		}
		if r.Manual != nil {
			return *r.Manual, true
		}
		if r.Metered != nil && modelgw.IsBailian(pc.BaseURL) {
			return *r.Metered, true
		}
	}
	if modelgw.IsBailian(pc.BaseURL) {
		return modelgw.BailianPrice(model)
	}
	return modelgw.Price{}, false
}

func isTier(scope string) bool { return slices.Contains(modelgw.Tiers, llm.Tier(scope)) }

func purposeTier(scope string) (llm.Tier, bool) {
	for _, p := range modelgw.Purposes {
		if p.Name == scope {
			return p.Tier, true
		}
	}
	return "", false
}

// routingView builds the console's routing page from the database.
func (s *Server) routingView(ctx context.Context) (map[string]any, error) {
	cfg, err := s.gatewayConfig(ctx)
	if err != nil {
		return nil, err
	}
	pl, err := s.prices(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"llmProvider": s.LLMProvider, "gatewayOn": s.LLMProvider == "gateway", "configured": false,
		"importedFromFile": false}
	if s.LLMProvider == "" {
		out["llmProvider"] = "mock"
	}
	st := modelgw.Settings{Routes: map[llm.Tier][]modelgw.RouteStep{}, BreakerFailures: modelgw.DefaultBreakerFailures,
		BreakerCooldown: modelgw.DefaultBreakerCooldown}
	row, err := s.Q.GetGatewaySettings(ctx)
	switch {
	case err == nil:
		st = modelgw.ParseSettings(row.GatewaySetting)
		out["configured"], out["importedFromFile"] = true, row.GatewaySetting.ImportedFromFile
		out["updatedAt"] = row.GatewaySetting.UpdatedAt
		if row.UpdatedByEmail != nil {
			out["updatedBy"] = *row.UpdatedByEmail
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, apperr.Internal(err)
	}
	keyOK := map[string]bool{}
	for _, p := range cfg.Providers {
		keyOK[p.Name], _ = s.keySourceFor(ctx, p.Name, p.APIKeyEnv)
	}
	view := func(tier llm.Tier, step modelgw.RouteStep) routeStep {
		v := routeStep{Provider: step.Provider, Model: strings.TrimSpace(step.Model)}
		pc, ok := providerConfig(cfg, step.Provider)
		v.Available, v.KeyPresent = ok, keyOK[step.Provider]
		if v.Model == "" {
			v.Model, v.UsesDefault = pc.Models[tier], true
		}
		if ok && v.Model != "" {
			if p, priced := meteredPrice(pc, v.Model, pl); priced {
				v.Priced, v.Price = true, &p
			}
		}
		return v
	}
	tiers := []tierRoute{}
	chains := map[llm.Tier][]routeStep{}
	for _, t := range modelgw.Tiers {
		tr := tierRoute{Tier: t, Steps: []routeStep{}}
		for _, step := range st.Routes[t] {
			tr.Steps = append(tr.Steps, view(t, step))
		}
		chains[t] = tr.Steps
		tiers = append(tiers, tr)
	}
	ovs, err := s.Q.ListPurposeOverrides(ctx)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	byScope := map[string]db.ListPurposeOverridesRow{}
	for _, o := range ovs {
		byScope[o.Scope] = o
	}
	purposes := []purposeRoute{}
	for _, p := range modelgw.Purposes {
		pr := purposeRoute{Scope: p.Name, Tier: p.Tier, Effective: []routeStep{}}
		seen := map[string]bool{}
		if o, ok := byScope[p.Name]; ok {
			pr.Override = map[string]any{"provider": o.Provider, "model": o.Model, "updatedAt": o.UpdatedAt}
			if o.UpdatedByEmail != nil {
				pr.Override["updatedBy"] = *o.UpdatedByEmail
			}
			pr.Effective = append(pr.Effective, view(p.Tier, modelgw.RouteStep{Provider: o.Provider, Model: o.Model}))
			seen[o.Provider] = true
		}
		for _, step := range chains[p.Tier] {
			if !seen[step.Provider] {
				seen[step.Provider] = true
				pr.Effective = append(pr.Effective, step)
			}
		}
		purposes = append(purposes, pr)
	}
	providers := []map[string]any{}
	for _, p := range cfg.Providers {
		models := map[string]string{}
		for t, m := range p.Models {
			if m != "" {
				models[string(t)] = m
			}
		}
		providers = append(providers, map[string]any{"name": p.Name, "displayName": providerDisplayName(p),
			"timeoutSeconds": int(p.Timeout / time.Second), "keyPresent": keyOK[p.Name], "models": models, "bailian": modelgw.IsBailian(p.BaseURL)})
	}
	cat := map[string]any{"source": modelgw.BailianSource(), "updated": pl.BuiltinUpdated}
	if pl.SyncedAt != nil {
		cat["source"], cat["updated"] = "百炼实时价格（「模型定价」页同步）", pl.SyncedAt.Format("2006-01-02 15:04")
	}
	catModels := []map[string]any{}
	for _, m := range pl.Models {
		e := map[string]any{"model": m.Model, "vendor": m.Vendor, "contextWindow": m.ContextWindow}
		if m.Metered != nil {
			e["price"] = m.Metered
		}
		catModels = append(catModels, e)
	}
	cat["models"] = catModels
	usage, err := s.Q.UsageByProviderSince(ctx, s.now().Add(-24*time.Hour))
	if err != nil {
		return nil, apperr.Internal(err)
	}
	usage24h := []gwUsage{}
	for _, u := range usage {
		last := u.LastCallAt
		usage24h = append(usage24h, gwUsage{Provider: u.Provider, Calls: int(u.Calls), CostCny: cny(u.CostMicros), LastCallAt: &last})
	}
	vs, err := s.Q.ListRoutingVersions(ctx, 10)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	versions := []map[string]any{}
	for _, v := range vs {
		e := map[string]any{"id": v.ID, "action": v.Action, "createdAt": v.CreatedAt}
		if v.CreatedByEmail != nil {
			e["createdBy"] = *v.CreatedByEmail
		}
		versions = append(versions, e)
	}
	out["catalog"], out["providers"], out["tiers"], out["purposes"] = cat, providers, tiers, purposes
	out["breaker"] = map[string]any{"failures": st.BreakerFailures, "cooldownSeconds": int(st.BreakerCooldown / time.Second)}
	out["usage24h"], out["versions"] = usage24h, versions
	return out, nil
}

func (s *Server) routingResponse(ctx context.Context) (gen.ModelRouting, error) {
	v, err := s.routingView(ctx)
	if err != nil {
		return gen.ModelRouting{}, err
	}
	return recode[gen.ModelRouting](v), nil
}

// recordVersion snapshots the routing after a change.
func (s *Server) recordVersion(ctx context.Context, action string, by db.AdminUser) error {
	snap, err := modelgw.LoadSnapshot(ctx, s.Q)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // no tier routes yet: nothing to go back to
	}
	if err != nil {
		return apperr.Internal(err)
	}
	var id = by.ID
	if _, err := s.Q.InsertRoutingVersion(ctx, db.InsertRoutingVersionParams{Snapshot: snap, Action: action, CreatedBy: &id}); err != nil {
		return apperr.Internal(err)
	}
	return nil
}

func (s *Server) GetModelRouting(ctx context.Context, _ gen.GetModelRoutingRequestObject) (gen.GetModelRoutingResponseObject, error) {
	r, err := s.routingResponse(ctx)
	if err != nil {
		return nil, err
	}
	return gen.GetModelRouting200JSONResponse(r), nil
}

// cleanRoutes checks a tier route: known, enabled providers, each once, every step resolving
// to a model.
func cleanRoutes(cfg modelgw.Config, tier llm.Tier, in []gen.RouteStepInput) ([]modelgw.RouteStep, error) {
	label := map[llm.Tier]string{llm.TierStrong: "强档", llm.TierFast: "快档"}[tier]
	if len(in) == 0 {
		return nil, apperr.Invalid(label + "至少要有一个服务商")
	}
	if len(in) > 10 {
		return nil, apperr.Invalid(label + "最多 10 个服务商")
	}
	out := []modelgw.RouteStep{}
	seen := map[string]bool{}
	for _, st := range in {
		pc, ok := providerConfig(cfg, st.Provider)
		if !ok {
			return nil, apperr.Invalid(label + "里的服务商 " + st.Provider + " 不存在或已停用")
		}
		if seen[pc.Name] {
			return nil, apperr.Invalid(label + "里 " + providerDisplayName(pc) + " 出现了两次")
		}
		seen[pc.Name] = true
		model := strings.TrimSpace(deref(st.Model))
		if len(model) > 100 {
			return nil, apperr.Invalid("模型名不超过 100 个字符")
		}
		if model == "" && pc.Models[tier] == "" {
			return nil, apperr.Invalid(providerDisplayName(pc) + " 没有" + label + "的默认模型，请选一个模型")
		}
		if model == pc.Models[tier] {
			model = ""
		}
		out = append(out, modelgw.RouteStep{Provider: pc.Name, Model: model})
	}
	return out, nil
}

func (s *Server) SaveRoutingSettings(ctx context.Context, req gen.SaveRoutingSettingsRequestObject) (gen.SaveRoutingSettingsResponseObject, error) {
	a, err := require(ctx, PermModels)
	if err != nil {
		return nil, err
	}
	cfg, err := s.gatewayConfig(ctx)
	if err != nil {
		return nil, err
	}
	b := req.Body
	routes := map[llm.Tier][]modelgw.RouteStep{}
	if routes[llm.TierStrong], err = cleanRoutes(cfg, llm.TierStrong, b.Routes.Strong); err != nil {
		return nil, err
	}
	if routes[llm.TierFast], err = cleanRoutes(cfg, llm.TierFast, b.Routes.Fast); err != nil {
		return nil, err
	}
	if b.Breaker.Failures < 1 || b.Breaker.Failures > 20 {
		return nil, apperr.Invalid("连续失败次数应在 1 到 20 之间")
	}
	if b.Breaker.CooldownSeconds < 5 || b.Breaker.CooldownSeconds > 3600 {
		return nil, apperr.Invalid("暂停时间应在 5 到 3600 秒之间")
	}
	id := a.ID
	if err := s.Q.SaveGatewaySettings(ctx, db.SaveGatewaySettingsParams{Routes: modelgw.RoutesJSON(routes),
		BreakerFailures: int32(b.Breaker.Failures), BreakerCooldownSeconds: int32(b.Breaker.CooldownSeconds), UpdatedBy: &id}); err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "model_routing.save", "model_routing", "settings", map[string]any{"routes": routes, "breaker": b.Breaker})
	if err := s.recordVersion(ctx, "settings", a); err != nil {
		return nil, err
	}
	r, err := s.routingResponse(ctx)
	if err != nil {
		return nil, err
	}
	return gen.SaveRoutingSettings200JSONResponse(r), nil
}

func (s *Server) RestoreRoutingVersion(ctx context.Context, req gen.RestoreRoutingVersionRequestObject) (gen.RestoreRoutingVersionResponseObject, error) {
	a, err := require(ctx, PermModels)
	if err != nil {
		return nil, err
	}
	v, err := s.Q.GetRoutingVersion(ctx, req.VersionId)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.NotFound("没有这个历史版本")
	}
	if err != nil {
		return nil, apperr.Internal(err)
	}
	var snap modelgw.Snapshot
	if err := json.Unmarshal(v.Snapshot, &snap); err != nil {
		return nil, apperr.Internal(err)
	}
	cfg, err := s.gatewayConfig(ctx)
	if err != nil {
		return nil, err
	}
	check := func(name string) error {
		if _, ok := providerConfig(cfg, name); !ok {
			return apperr.Invalid("这个版本用到的服务商 " + name + " 已删除或停用，先到「服务商」页恢复它")
		}
		return nil
	}
	for _, t := range modelgw.Tiers {
		for _, st := range snap.Routes[t] {
			if err := check(st.Provider); err != nil {
				return nil, err
			}
		}
	}
	for _, p := range snap.Purposes {
		if err := check(p.Provider); err != nil {
			return nil, err
		}
	}
	id := a.ID
	if err := s.Q.SaveGatewaySettings(ctx, db.SaveGatewaySettingsParams{Routes: modelgw.RoutesJSON(snap.Routes),
		BreakerFailures: int32(snap.BreakerFailures), BreakerCooldownSeconds: int32(snap.BreakerCooldownSeconds), UpdatedBy: &id}); err != nil {
		return nil, apperr.Internal(err)
	}
	if err := s.Q.DeletePurposeOverrides(ctx); err != nil {
		return nil, apperr.Internal(err)
	}
	for scope, p := range snap.Purposes {
		if _, ok := purposeTier(scope); !ok {
			continue
		}
		if err := s.Q.SetPurposeOverride(ctx, db.SetPurposeOverrideParams{Scope: scope, Provider: p.Provider, Model: p.Model, UpdatedBy: &id}); err != nil {
			return nil, apperr.Internal(err)
		}
	}
	s.audit(ctx, "model_routing.restore", "model_routing", "settings", map[string]any{"version": v.ID})
	if err := s.recordVersion(ctx, "restore", a); err != nil {
		return nil, err
	}
	r, err := s.routingResponse(ctx)
	if err != nil {
		return nil, err
	}
	return gen.RestoreRoutingVersion200JSONResponse(r), nil
}

func (s *Server) SetModelOverride(ctx context.Context, req gen.SetModelOverrideRequestObject) (gen.SetModelOverrideResponseObject, error) {
	a, err := require(ctx, PermModels)
	if err != nil {
		return nil, err
	}
	scope := string(req.Scope)
	if isTier(scope) {
		return nil, apperr.Invalid("档位的调用顺序在「档位顺序」里修改")
	}
	if _, ok := purposeTier(scope); !ok {
		return nil, apperr.NotFound("没有这个环节")
	}
	cfg, err := s.gatewayConfig(ctx)
	if err != nil {
		return nil, err
	}
	b := req.Body
	p, ok := providerConfig(cfg, b.Provider)
	if !ok {
		return nil, apperr.Invalid("没有这个服务商，或已停用")
	}
	model := strings.TrimSpace(b.Model)
	if model == "" || len(model) > 100 {
		return nil, apperr.Invalid("请填写模型名（不超过 100 个字符）")
	}
	id := a.ID
	if err := s.Q.SetPurposeOverride(ctx, db.SetPurposeOverrideParams{Scope: scope, Provider: p.Name, Model: model, UpdatedBy: &id}); err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "model_override.set", "model_override", scope, map[string]any{"provider": p.Name, "model": model})
	if err := s.recordVersion(ctx, "purpose", a); err != nil {
		return nil, err
	}
	r, err := s.routingResponse(ctx)
	if err != nil {
		return nil, err
	}
	return gen.SetModelOverride200JSONResponse(r), nil
}

func (s *Server) DeleteModelOverride(ctx context.Context, req gen.DeleteModelOverrideRequestObject) (gen.DeleteModelOverrideResponseObject, error) {
	a, err := require(ctx, PermModels)
	if err != nil {
		return nil, err
	}
	scope := string(req.Scope)
	if _, ok := purposeTier(scope); !ok {
		return nil, apperr.NotFound("没有这个环节")
	}
	n, err := s.Q.DeleteModelOverride(ctx, scope)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if n > 0 {
		s.audit(ctx, "model_override.reset", "model_override", scope, nil)
		if err := s.recordVersion(ctx, "purpose", a); err != nil {
			return nil, err
		}
	}
	return gen.DeleteModelOverride204Response{}, nil
}

// ListProviderModels asks the provider which models its current key can use (GET /models).
func (s *Server) ListProviderModels(ctx context.Context, req gen.ListProviderModelsRequestObject) (gen.ListProviderModelsResponseObject, error) {
	if _, err := require(ctx, PermModels); err != nil {
		return nil, err
	}
	cfg, err := s.gatewayConfig(ctx)
	if err != nil {
		return nil, err
	}
	p, ok := providerConfig(cfg, req.Provider)
	if !ok {
		return nil, apperr.NotFound("没有这个服务商，或已停用")
	}
	none := func(msg string) (gen.ListProviderModelsResponseObject, error) {
		return gen.ListProviderModels200JSONResponse{Models: []string{}, Error: &msg}, nil
	}
	key := s.Keys.Key(ctx, p.Name, p.APIKeyEnv)
	if key == "" {
		return none("这个服务商还没有 Key，先到「服务商」页添加")
	}
	base := p.BaseURL
	if u := s.Keys.BaseURL(ctx, p.Name); u != "" {
		base = u
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	models, err := llm.OpenAICompat{Calls: calllog.New(s.Q, s.Log), BaseURL: base, APIKey: key, Name: p.Name, HTTP: &http.Client{Timeout: 30 * time.Second}}.ListModels(ctx)
	if err != nil {
		return none(testError(err))
	}
	slices.Sort(models)
	return gen.ListProviderModels200JSONResponse{Models: slices.Compact(models)}, nil
}

// TestModel sends one output token to provider's model with the key the gateway would use.
func (s *Server) TestModel(ctx context.Context, req gen.TestModelRequestObject) (gen.TestModelResponseObject, error) {
	if _, err := require(ctx, PermModels); err != nil {
		return nil, err
	}
	cfg, err := s.gatewayConfig(ctx)
	if err != nil {
		return nil, err
	}
	p, ok := providerConfig(cfg, req.Body.Provider)
	if !ok {
		return nil, apperr.Invalid("没有这个服务商，或已停用")
	}
	model := strings.TrimSpace(req.Body.Model)
	if model == "" {
		return nil, apperr.Invalid("请填写模型名")
	}
	key := s.Keys.Key(ctx, p.Name, p.APIKeyEnv)
	if key == "" {
		return gen.TestModel200JSONResponse(gen.ProviderKeyTest{Message: "这个服务商还没有 Key，先到「服务商」页添加"}), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	start := time.Now()
	base := p.BaseURL
	if u := s.Keys.BaseURL(ctx, p.Name); u != "" {
		base = u
	}
	resp, err := llm.OpenAICompat{Calls: calllog.New(s.Q, s.Log), BaseURL: base, APIKey: key, Name: p.Name, HTTP: &http.Client{Timeout: 30 * time.Second}}.Complete(ctx,
		llm.Request{Origin: "admin_test", Purpose: "model_test", Model: model, MaxTokens: 1, Messages: []llm.Message{{Role: "user", Content: "ping"}}})
	res := gen.ProviderKeyTest{LatencyMs: int(time.Since(start).Milliseconds())}
	if err != nil {
		res.Message = testError(err)
	} else {
		res.Ok, res.Message = true, "模型 "+resp.Model+" 正常响应"
	}
	return gen.TestModel200JSONResponse(res), nil
}
