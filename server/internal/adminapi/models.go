package adminapi

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/llm"
	"github.com/hwq830/zeusai/server/internal/modelgw"
)

// modelChoice mirrors gen.ModelChoice; fallbacks is never null in responses.
type modelChoice struct {
	Provider  string   `json:"provider"`
	Model     string   `json:"model"`
	Fallbacks []string `json:"fallbacks"`
}

type modelOverride struct {
	Provider  string         `json:"provider"`
	Model     string         `json:"model"`
	Fallbacks []string       `json:"fallbacks,omitempty"`
	Price     *modelgw.Price `json:"price,omitempty"`
	Priced    bool           `json:"priced"`
	UpdatedBy *string        `json:"updatedBy,omitempty"`
	UpdatedAt time.Time      `json:"updatedAt"`
}

type modelRoute struct {
	Scope     string         `json:"scope"`
	Kind      string         `json:"kind"`
	Tier      llm.Tier       `json:"tier"`
	Default   modelChoice    `json:"default"`
	Effective modelChoice    `json:"effective"`
	Override  *modelOverride `json:"override,omitempty"`
}

// routing is the console's view of model choices: the routing file plus overrides.
type routing struct {
	cfg      modelgw.Config
	tiers    []modelRoute
	purposes []modelRoute
}

func (s *Server) modelGWConfig() (modelgw.Config, error) {
	if s.ModelGWConfig == "" {
		return modelgw.Config{}, apperr.Invalid("没有配置模型路由文件（ZEUS_MODELGW_CONFIG）")
	}
	cfg, err := modelgw.LoadConfig(s.ModelGWConfig)
	if err != nil {
		return modelgw.Config{}, apperr.Invalid("模型路由文件读取失败：" + err.Error())
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

// following lists names after dropping first, unknown providers and repeats.
func following(cfg modelgw.Config, first string, names []string) []string {
	out := []string{}
	for _, n := range names {
		if _, ok := providerConfig(cfg, n); ok && n != first && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
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

func (s *Server) loadRouting(ctx context.Context, cfg modelgw.Config) (routing, error) {
	rows, err := s.Q.ListModelOverrides(ctx)
	if err != nil {
		return routing{}, apperr.Internal(err)
	}
	ovs := map[string]*modelOverride{}
	for _, r := range rows {
		o := &modelOverride{Provider: r.Provider, Model: r.Model, Fallbacks: r.Fallbacks, UpdatedBy: r.UpdatedByEmail,
			UpdatedAt: r.UpdatedAt}
		if r.PriceInput != nil || r.PriceOutput != nil || r.PriceCached != nil {
			o.Price = &modelgw.Price{Input: deref(r.PriceInput), Output: deref(r.PriceOutput), Cached: deref(r.PriceCached)}
		}
		p, _ := providerConfig(cfg, r.Provider)
		_, priced := p.Prices[r.Model]
		o.Priced = priced || o.Price != nil
		ovs[r.Scope] = o
	}
	rt := routing{cfg: cfg}
	effective := map[llm.Tier]modelChoice{}
	for _, t := range modelgw.Tiers {
		route := cfg.Routes[t]
		def := modelChoice{Fallbacks: []string{}}
		if len(route) > 0 {
			p, _ := providerConfig(cfg, route[0])
			def = modelChoice{Provider: route[0], Model: p.Models[t], Fallbacks: following(cfg, route[0], route)}
		}
		r := modelRoute{Scope: string(t), Kind: "tier", Tier: t, Default: def, Effective: def}
		if o := ovs[string(t)]; o != nil {
			fb := route
			if o.Fallbacks != nil {
				fb = o.Fallbacks
			}
			r.Override, r.Effective = o, modelChoice{Provider: o.Provider, Model: o.Model, Fallbacks: following(cfg, o.Provider, fb)}
		}
		effective[t] = r.Effective
		rt.tiers = append(rt.tiers, r)
	}
	for _, p := range modelgw.Purposes {
		def := effective[p.Tier]
		r := modelRoute{Scope: p.Name, Kind: "purpose", Tier: p.Tier, Default: def, Effective: def}
		if o := ovs[p.Name]; o != nil {
			chain := append([]string{def.Provider}, def.Fallbacks...)
			r.Override, r.Effective = o, modelChoice{Provider: o.Provider, Model: o.Model, Fallbacks: following(cfg, o.Provider, chain)}
		}
		rt.purposes = append(rt.purposes, r)
	}
	return rt, nil
}

func (s *Server) GetModelRouting(ctx context.Context, _ gen.GetModelRoutingRequestObject) (gen.GetModelRoutingResponseObject, error) {
	out := map[string]any{"llmProvider": s.LLMProvider, "configured": false, "providers": []any{}, "tiers": []any{}, "purposes": []any{}}
	if s.LLMProvider == "" {
		out["llmProvider"] = "mock"
	}
	cfg, err := s.modelGWConfig()
	if err != nil {
		out["configError"] = apperr.As(err).Message
		return gen.GetModelRouting200JSONResponse(recode[gen.ModelRouting](out)), nil
	}
	rt, err := s.loadRouting(ctx, cfg)
	if err != nil {
		return nil, err
	}
	providers := []map[string]any{}
	for _, p := range cfg.Providers {
		models := map[string]string{}
		for t, m := range p.Models {
			models[string(t)] = m
		}
		priced := []string{}
		for m := range p.Prices {
			priced = append(priced, m)
		}
		slices.Sort(priced)
		present, _ := s.keySourceFor(ctx, p.Name, p.APIKeyEnv)
		providers = append(providers, map[string]any{"name": p.Name, "displayName": displayName(p.Name),
			"keyPresent": present, "models": models, "pricedModels": priced})
	}
	out["configured"] = s.LLMProvider == "gateway"
	out["providers"], out["tiers"], out["purposes"] = providers, rt.tiers, rt.purposes
	return gen.GetModelRouting200JSONResponse(recode[gen.ModelRouting](out)), nil
}

func (s *Server) SetModelOverride(ctx context.Context, req gen.SetModelOverrideRequestObject) (gen.SetModelOverrideResponseObject, error) {
	a, err := require(ctx, PermModels)
	if err != nil {
		return nil, err
	}
	scope := string(req.Scope)
	_, isPurpose := purposeTier(scope)
	if !isTier(scope) && !isPurpose {
		return nil, apperr.NotFound("没有这个档位或环节")
	}
	cfg, err := s.modelGWConfig()
	if err != nil {
		return nil, err
	}
	b := req.Body
	p, ok := providerConfig(cfg, b.Provider)
	if !ok {
		return nil, apperr.Invalid("路由文件里没有这个服务商")
	}
	model := strings.TrimSpace(b.Model)
	if model == "" {
		return nil, apperr.Invalid("请填写模型名")
	}
	var fallbacks []string
	if b.Fallbacks != nil {
		if !isTier(scope) {
			return nil, apperr.Invalid("只有档位可以设置备用顺序，环节失败后自动按所在档位的顺序继续")
		}
		for _, n := range *b.Fallbacks {
			if _, ok := providerConfig(cfg, n); !ok {
				return nil, apperr.Invalid("备用服务商 " + n + " 不在路由文件里")
			}
		}
		fallbacks = following(cfg, p.Name, *b.Fallbacks)
	}
	params := db.UpsertModelOverrideParams{Scope: scope, Provider: p.Name, Model: model, Fallbacks: fallbacks, UpdatedBy: &a.ID}
	if b.Price != nil {
		pr := b.Price
		if pr.Input < 0 || pr.Output < 0 || pr.Cached < 0 {
			return nil, apperr.Invalid("价格不能为负")
		}
		in, outp, cached := float64(pr.Input), float64(pr.Output), float64(pr.Cached)
		params.PriceInput, params.PriceOutput, params.PriceCached = &in, &outp, &cached
	} else if _, priced := p.Prices[model]; !priced {
		return nil, apperr.Invalid("路由文件里没有 " + model + " 的价格，请填写价格，否则成本会按 0 计")
	}
	if _, err := s.Q.UpsertModelOverride(ctx, params); err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "model_override.set", "model_override", scope, map[string]any{"provider": p.Name, "model": model,
		"fallbacks": fallbacks, "price": b.Price})
	rt, err := s.loadRouting(ctx, cfg)
	if err != nil {
		return nil, err
	}
	for _, r := range slices.Concat(rt.tiers, rt.purposes) {
		if r.Scope == scope {
			return gen.SetModelOverride200JSONResponse(recode[gen.ModelRoute](r)), nil
		}
	}
	return nil, apperr.Internal(nil)
}

func (s *Server) DeleteModelOverride(ctx context.Context, req gen.DeleteModelOverrideRequestObject) (gen.DeleteModelOverrideResponseObject, error) {
	if _, err := require(ctx, PermModels); err != nil {
		return nil, err
	}
	scope := string(req.Scope)
	if _, ok := purposeTier(scope); !isTier(scope) && !ok {
		return nil, apperr.NotFound("没有这个档位或环节")
	}
	n, err := s.Q.DeleteModelOverride(ctx, scope)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if n > 0 {
		s.audit(ctx, "model_override.reset", "model_override", scope, nil)
	}
	return gen.DeleteModelOverride204Response{}, nil
}

// TestModel sends one output token to provider's model with the key the gateway would use.
func (s *Server) TestModel(ctx context.Context, req gen.TestModelRequestObject) (gen.TestModelResponseObject, error) {
	if _, err := require(ctx, PermModels); err != nil {
		return nil, err
	}
	cfg, err := s.modelGWConfig()
	if err != nil {
		return nil, err
	}
	p, ok := providerConfig(cfg, req.Body.Provider)
	if !ok {
		return nil, apperr.Invalid("路由文件里没有这个服务商")
	}
	model := strings.TrimSpace(req.Body.Model)
	if model == "" {
		return nil, apperr.Invalid("请填写模型名")
	}
	key := s.Keys.Key(ctx, p.Name, p.APIKeyEnv)
	if key == "" {
		return gen.TestModel200JSONResponse(gen.ProviderKeyTest{Message: "这个服务商还没有 Key，先到「API Key」页添加"}), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	start := time.Now()
	resp, err := llm.OpenAICompat{BaseURL: p.BaseURL, APIKey: key, Name: p.Name, HTTP: &http.Client{Timeout: 30 * time.Second}}.Complete(ctx,
		llm.Request{Purpose: "model_test", Model: model, MaxTokens: 1, Messages: []llm.Message{{Role: "user", Content: "ping"}}})
	res := gen.ProviderKeyTest{LatencyMs: int(time.Since(start).Milliseconds())}
	if err != nil {
		res.Message = testError(err)
	} else {
		res.Ok, res.Message = true, "模型 "+resp.Model+" 正常响应"
	}
	return gen.TestModel200JSONResponse(res), nil
}
