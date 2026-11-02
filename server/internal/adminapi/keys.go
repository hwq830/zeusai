package adminapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/llm"
	"github.com/hwq830/zeusai/server/internal/modelgw"
	"github.com/hwq830/zeusai/server/internal/platform/providers"
	"github.com/hwq830/zeusai/server/internal/search"
	"github.com/hwq830/zeusai/server/internal/secrets"
)

// keyProvider is a provider whose key can be set in the console: the model providers (added
// in the console or imported from the routing file) and the search provider.
type keyProvider struct {
	ID, Name, Kind, BaseURL, EnvName string
	Protocol                         string // search only
	SearchOrder                      int    // search only: 1 = primary, 2+ = fallback, 0 = unused
	Source                           string // console or builtin
	Imported, Enabled                bool
	Models                           map[llm.Tier]string
	Timeout                          time.Duration
	InRoute                          bool
}

var providerNames = map[string]string{
	"qwen":                  "通义千问",
	"deepseek":              "DeepSeek",
	providers.BochaProvider: "博查搜索",
}

// routingUses maps each provider to where the routing uses it: tier routes ("strong", "fast")
// and purpose choices.
func (s *Server) routingUses(ctx context.Context) (map[string][]string, error) {
	used := map[string][]string{}
	row, err := s.Q.GetGatewaySettings(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		st := modelgw.ParseSettings(row.GatewaySetting)
		for _, t := range modelgw.Tiers {
			for _, step := range st.Routes[t] {
				used[step.Provider] = append(used[step.Provider], string(t))
			}
		}
	}
	ovs, err := s.Q.ListPurposeOverrides(ctx)
	if err != nil {
		return nil, err
	}
	for _, o := range ovs {
		used[o.Provider] = append(used[o.Provider], o.Scope)
	}
	return used, nil
}

func (s *Server) keyProviders(ctx context.Context) []keyProvider {
	s.ensureRouting(ctx)
	var out []keyProvider
	used, _ := s.routingUses(ctx)
	gateway := s.LLMProvider == "gateway"
	if rows, err := s.Q.ListModelProviders(ctx); err == nil {
		for _, r := range rows {
			pc := modelgw.ConsoleProvider(r.ModelProvider)
			out = append(out, keyProvider{ID: pc.Name, Name: providerDisplayName(pc), Kind: "llm", BaseURL: pc.BaseURL,
				EnvName: pc.APIKeyEnv, Source: "console", Imported: r.ModelProvider.Imported, Enabled: r.ModelProvider.Enabled,
				Models: pc.Models, Timeout: pc.Timeout, InRoute: gateway && r.ModelProvider.Enabled && len(used[pc.Name]) > 0})
		}
	}
	return append(out, s.searchKeyProviders(ctx)...)
}

func providerDisplayName(p modelgw.ProviderConfig) string {
	if p.DisplayName != "" {
		return p.DisplayName
	}
	return displayName(p.Name)
}

func displayName(id string) string {
	if n, ok := modelgw.KnownNames[id]; ok {
		return n
	}
	if n, ok := providerNames[id]; ok {
		return n
	}
	return id
}

// searchBaseURL is where a search provider is called when its key has no address of its own.
func (s *Server) searchBaseURL(protocol, base string) string {
	if base != "" {
		return base
	}
	if protocol == search.ProtocolBocha && s.BochaBaseURL != "" {
		return s.BochaBaseURL
	}
	for _, p := range search.Protocols {
		if p.ID == protocol {
			return p.DefaultBaseURL
		}
	}
	return ""
}

func (s *Server) keyProvider(ctx context.Context, id string) (keyProvider, bool) {
	for _, p := range s.keyProviders(ctx) {
		if p.ID == id {
			return p, true
		}
	}
	return keyProvider{}, false
}

func (s *Server) box() *secrets.Box {
	if s.Keys == nil {
		return nil
	}
	return s.Keys.Box
}

func (s *Server) toKeyProvider(ctx context.Context, p keyProvider) gen.KeyProvider {
	base := p.BaseURL
	kp := gen.KeyProvider{Id: p.ID, Name: p.Name, Kind: gen.KeyProviderKind(p.Kind), BaseUrl: &base, ApiKeyEnv: p.EnvName,
		EnvKeyPresent: p.EnvName != "" && os.Getenv(p.EnvName) != "", KeySource: keySource(s.Keys.Source(ctx, p.ID, p.EnvName)),
		InRoute: p.InRoute, Source: gen.KeyProviderSource(p.Source), Enabled: p.Enabled, Imported: &p.Imported}
	if p.Kind == "search" {
		kp.Protocol = &p.Protocol
		if p.SearchOrder > 0 {
			kp.SearchOrder = &p.SearchOrder
		}
	}
	if p.Kind == "llm" {
		strong, fast := p.Models[llm.TierStrong], p.Models[llm.TierFast]
		kp.Models = &gen.ProviderTierModels{Strong: &strong, Fast: &fast}
		secs := int(p.Timeout / time.Second)
		if secs == 0 {
			secs = 120
		}
		kp.TimeoutSeconds = &secs
	}
	return kp
}

func keySource(src string) gen.KeyProviderKeySource {
	if src == "" {
		return gen.KeyProviderKeySource("none")
	}
	return gen.KeyProviderKeySource(src)
}

func toProviderKey(k db.ProviderKey, by *string) gen.ProviderKey {
	var latency *int
	if k.LastTestLatencyMs != nil {
		v := int(*k.LastTestLatencyMs)
		latency = &v
	}
	msg := k.LastTestMessage
	return gen.ProviderKey{Id: k.ID, Provider: k.Provider, Label: k.Label, Last4: k.Last4, BaseUrl: k.BaseUrl, Active: k.Active,
		CreatedBy: by, CreatedAt: k.CreatedAt, LastTestedAt: k.LastTestedAt, LastTestOk: k.LastTestOk,
		LastTestMessage: &msg, LastTestLatencyMs: latency}
}

func (s *Server) ListProviderKeys(ctx context.Context, _ gen.ListProviderKeysRequestObject) (gen.ListProviderKeysResponseObject, error) {
	rows, err := s.Q.ListProviderKeys(ctx)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	out := gen.ListProviderKeys200JSONResponse{EncryptionEnabled: s.box() != nil,
		Providers: []gen.KeyProvider{}, Keys: make([]gen.ProviderKey, 0, len(rows))}
	for _, p := range s.keyProviders(ctx) {
		out.Providers = append(out.Providers, s.toKeyProvider(ctx, p))
	}
	for _, r := range rows {
		out.Keys = append(out.Keys, toProviderKey(db.ProviderKey{ID: r.ID, Provider: r.Provider, Label: r.Label, Last4: r.Last4, BaseUrl: r.BaseUrl,
			Active: r.Active, CreatedAt: r.CreatedAt, LastTestedAt: r.LastTestedAt, LastTestOk: r.LastTestOk,
			LastTestMessage: r.LastTestMessage, LastTestLatencyMs: r.LastTestLatencyMs}, r.CreatedByEmail))
	}
	return out, nil
}

func (s *Server) CreateProviderKey(ctx context.Context, req gen.CreateProviderKeyRequestObject) (gen.CreateProviderKeyResponseObject, error) {
	a, err := require(ctx, PermProviderKeys)
	if err != nil {
		return nil, err
	}
	box := s.box()
	if box == nil {
		return nil, apperr.Invalid("服务端没有配置 ZEUS_SECRETS_KEY，不能在后台保存 Key")
	}
	p, ok := s.keyProvider(ctx, req.Body.Provider)
	if !ok {
		return nil, apperr.Invalid("未知的服务商")
	}
	key := strings.TrimSpace(req.Body.ApiKey)
	if len(key) < 8 || len(key) > 512 || strings.ContainsAny(key, " \t\r\n") {
		return nil, apperr.Invalid("Key 格式不正确")
	}
	label := strings.TrimSpace(deref(req.Body.Label))
	if len([]rune(label)) > 50 {
		return nil, apperr.Invalid("备注不超过 50 字")
	}
	base, err := cleanBaseURL(deref(req.Body.BaseUrl))
	if err != nil {
		return nil, err
	}
	k, err := s.Q.CreateProviderKey(ctx, db.CreateProviderKeyParams{Provider: p.ID, Label: label,
		Ciphertext: box.Seal(p.ID, key), Last4: secrets.Last4(key), CreatedBy: &a.ID, BaseUrl: base})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "provider_key.create", "provider_key", k.ID.String(), map[string]any{"provider": p.ID, "label": label, "last4": k.Last4, "baseUrl": base})
	if req.Body.Activate == nil || *req.Body.Activate {
		if k, err = s.activateKey(ctx, k); err != nil {
			return nil, err
		}
	}
	return gen.CreateProviderKey201JSONResponse(toProviderKey(k, &a.Email)), nil
}

func (s *Server) activateKey(ctx context.Context, k db.ProviderKey) (db.ProviderKey, error) {
	if err := s.Q.DeactivateProviderKeys(ctx, k.Provider); err != nil {
		return k, apperr.Internal(err)
	}
	if _, err := s.Q.SetProviderKeyActive(ctx, db.SetProviderKeyActiveParams{ID: k.ID, Active: true}); err != nil {
		return k, apperr.Internal(err)
	}
	s.Keys.Invalidate()
	k.Active = true
	s.audit(ctx, "provider_key.activate", "provider_key", k.ID.String(), map[string]any{"provider": k.Provider, "last4": k.Last4})
	return k, nil
}

func (s *Server) getKey(ctx context.Context, id gen.KeyId) (db.ProviderKey, error) {
	k, err := s.Q.GetProviderKey(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return k, apperr.NotFound("Key 不存在")
	}
	if err != nil {
		return k, apperr.Internal(err)
	}
	return k, nil
}

func (s *Server) ActivateProviderKey(ctx context.Context, req gen.ActivateProviderKeyRequestObject) (gen.ActivateProviderKeyResponseObject, error) {
	if _, err := require(ctx, PermProviderKeys); err != nil {
		return nil, err
	}
	k, err := s.getKey(ctx, req.KeyId)
	if err != nil {
		return nil, err
	}
	if s.box() == nil {
		return nil, apperr.Invalid("服务端没有配置 ZEUS_SECRETS_KEY")
	}
	if _, err := s.box().Open(k.Provider, k.Ciphertext); err != nil {
		return nil, apperr.Invalid("这把 Key 无法解密（ZEUS_SECRETS_KEY 可能换过），请重新添加")
	}
	if k, err = s.activateKey(ctx, k); err != nil {
		return nil, err
	}
	return gen.ActivateProviderKey200JSONResponse(toProviderKey(k, nil)), nil
}

func (s *Server) DeactivateProviderKey(ctx context.Context, req gen.DeactivateProviderKeyRequestObject) (gen.DeactivateProviderKeyResponseObject, error) {
	if _, err := require(ctx, PermProviderKeys); err != nil {
		return nil, err
	}
	k, err := s.getKey(ctx, req.KeyId)
	if err != nil {
		return nil, err
	}
	if _, err := s.Q.SetProviderKeyActive(ctx, db.SetProviderKeyActiveParams{ID: k.ID, Active: false}); err != nil {
		return nil, apperr.Internal(err)
	}
	s.Keys.Invalidate()
	k.Active = false
	s.audit(ctx, "provider_key.deactivate", "provider_key", k.ID.String(), map[string]any{"provider": k.Provider, "last4": k.Last4})
	return gen.DeactivateProviderKey200JSONResponse(toProviderKey(k, nil)), nil
}

func (s *Server) DeleteProviderKey(ctx context.Context, req gen.DeleteProviderKeyRequestObject) (gen.DeleteProviderKeyResponseObject, error) {
	if _, err := require(ctx, PermProviderKeys); err != nil {
		return nil, err
	}
	k, err := s.getKey(ctx, req.KeyId)
	if err != nil {
		return nil, err
	}
	if k.Active {
		return nil, apperr.Conflict("启用中的 Key 不能删除，请先停用或启用另一把")
	}
	if _, err := s.Q.DeleteProviderKey(ctx, k.ID); err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "provider_key.delete", "provider_key", k.ID.String(), map[string]any{"provider": k.Provider, "last4": k.Last4})
	return gen.DeleteProviderKey204Response{}, nil
}

func (s *Server) TestNewProviderKey(ctx context.Context, req gen.TestNewProviderKeyRequestObject) (gen.TestNewProviderKeyResponseObject, error) {
	if _, err := require(ctx, PermProviderKeys); err != nil {
		return nil, err
	}
	p, ok := s.keyProvider(ctx, req.Body.Provider)
	if !ok {
		return nil, apperr.Invalid("未知的服务商")
	}
	base, err := cleanBaseURL(deref(req.Body.BaseUrl))
	if err != nil {
		return nil, err
	}
	if base != "" {
		p.BaseURL = base
	}
	if err := checkTestParams(req.Body.Params); err != nil {
		return nil, err
	}
	return gen.TestNewProviderKey200JSONResponse(s.testKey(ctx, p, strings.TrimSpace(req.Body.ApiKey), req.Body.Params)), nil
}

func (s *Server) TestProviderKey(ctx context.Context, req gen.TestProviderKeyRequestObject) (gen.TestProviderKeyResponseObject, error) {
	if _, err := require(ctx, PermProviderKeys); err != nil {
		return nil, err
	}
	if err := checkTestParams(req.Body.Params); err != nil {
		return nil, err
	}
	k, err := s.getKey(ctx, req.KeyId)
	if err != nil {
		return nil, err
	}
	p, ok := s.keyProvider(ctx, k.Provider)
	if !ok {
		return nil, apperr.Invalid("这个服务商已删除")
	}
	if s.box() == nil {
		return nil, apperr.Invalid("服务端没有配置 ZEUS_SECRETS_KEY")
	}
	var res gen.ProviderKeyTest
	if key, err := s.box().Open(k.Provider, k.Ciphertext); err != nil {
		res = gen.ProviderKeyTest{Message: "无法解密（ZEUS_SECRETS_KEY 可能换过），请重新添加"}
	} else {
		if k.BaseUrl != "" {
			p.BaseURL = k.BaseUrl
		}
		res = s.testKey(ctx, p, key, req.Body.Params)
	}
	latency := int32(res.LatencyMs)
	if err := s.Q.RecordProviderKeyTest(ctx, db.RecordProviderKeyTestParams{ID: k.ID, LastTestOk: &res.Ok,
		LastTestMessage: res.Message, LastTestLatencyMs: &latency}); err != nil {
		return nil, apperr.Internal(err)
	}
	return gen.TestProviderKey200JSONResponse(res), nil
}

func (s *Server) UpdateProviderKey(ctx context.Context, req gen.UpdateProviderKeyRequestObject) (gen.UpdateProviderKeyResponseObject, error) {
	_, err := require(ctx, PermProviderKeys)
	if err != nil {
		return nil, err
	}
	k, err := s.getKey(ctx, req.KeyId)
	if err != nil {
		return nil, err
	}
	label, base := k.Label, k.BaseUrl
	if req.Body.Label != nil {
		label = strings.TrimSpace(*req.Body.Label)
		if len([]rune(label)) > 50 {
			return nil, apperr.Invalid("备注不超过 50 字")
		}
	}
	if req.Body.BaseUrl != nil {
		if base, err = cleanBaseURL(*req.Body.BaseUrl); err != nil {
			return nil, err
		}
	}
	k, err = s.Q.UpdateProviderKey(ctx, db.UpdateProviderKeyParams{ID: k.ID, Label: label, BaseUrl: base})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	s.Keys.Invalidate()
	s.audit(ctx, "provider_key.update", "provider_key", k.ID.String(), map[string]any{"provider": k.Provider, "label": label, "baseUrl": base})
	return gen.UpdateProviderKey200JSONResponse(toProviderKey(k, nil)), nil
}

// cleanBaseURL accepts an http(s) address without query or fragment and drops a trailing
// slash; "" means the provider's default address.
func cleanBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", apperr.Invalid("API 地址格式不正确，应以 https:// 开头，例如 https://dashscope.aliyuncs.com/compatible-mode/v1")
	}
	if len(raw) > 300 {
		return "", apperr.Invalid("API 地址太长")
	}
	return strings.TrimRight(raw, "/"), nil
}

// checkTestParams enforces the contract's limits: a test call is paid for, so it stays small.
func checkTestParams(p *gen.KeyTestParams) error {
	if p == nil {
		return nil
	}
	if n := deref(p.MaxTokens); p.MaxTokens != nil && (n < 1 || n > 2048) {
		return apperr.Invalid("最大输出 token 需在 1–2048 之间")
	}
	if t := deref(p.Temperature); p.Temperature != nil && (t < 0 || t > 2) {
		return apperr.Invalid("temperature 需在 0–2 之间")
	}
	if n := deref(p.Count); p.Count != nil && (n < 1 || n > 10) {
		return apperr.Invalid("搜索条数需在 1–10 之间")
	}
	if len([]rune(deref(p.Prompt))) > 4000 || len([]rune(deref(p.System))) > 4000 || len([]rune(deref(p.Query))) > 200 || len(deref(p.Model)) > 100 {
		return apperr.Invalid("测试参数太长")
	}
	return nil
}

// searchHit is one search result in a key test (the contract's inline item type).
type searchHit = struct {
	Title string `json:"title"`
	Url   string `json:"url"`
}

// testKey sends a real request: by default one output token from the fast model, or a
// one-result search; params lets the console choose the model, prompt and limits.
func (s *Server) testKey(ctx context.Context, p keyProvider, key string, params *gen.KeyTestParams) gen.ProviderKeyTest {
	if key == "" {
		return gen.ProviderKeyTest{Message: "Key 为空"}
	}
	var tp gen.KeyTestParams
	if params != nil {
		tp = *params
	}
	timeout := 30 * time.Second
	if deref(tp.MaxTokens) > 64 {
		timeout = 120 * time.Second // longer answers take longer
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	hc := &http.Client{Timeout: timeout}
	start := time.Now()
	res := gen.ProviderKeyTest{}
	var err error
	var detail string
	switch p.Kind {
	case "search":
		q := strings.TrimSpace(deref(tp.Query))
		if q == "" {
			q = "今日新闻"
		}
		n := min(max(deref(tp.Count), 1), 10)
		var rs []search.Result
		var sr search.Searcher
		if sr, err = search.New(p.Protocol, key, p.BaseURL, hc); err == nil {
			rs, err = sr.Search(ctx, search.Query{Q: q, Limit: n})
		}
		if err == nil {
			detail = "返回 " + strconv.Itoa(len(rs)) + " 条结果"
			results := make([]searchHit, 0, len(rs))
			for _, r := range rs {
				results = append(results, searchHit{Title: r.Title, Url: r.URL})
			}
			res.Results = &results
		}
	default:
		prompt := deref(tp.Prompt)
		if strings.TrimSpace(prompt) == "" {
			prompt = "ping"
		}
		maxTokens := min(max(deref(tp.MaxTokens), 1), 2048)
		req := llm.Request{Purpose: "key_test", Tier: llm.TierFast, Model: strings.TrimSpace(deref(tp.Model)),
			System: deref(tp.System), JSON: deref(tp.Json), MaxTokens: maxTokens, Messages: []llm.Message{{Role: "user", Content: prompt}}}
		if tp.Temperature != nil {
			t := float64(*tp.Temperature)
			req.Temperature = &t
		}
		var resp llm.Response
		resp, err = llm.OpenAICompat{BaseURL: p.BaseURL, APIKey: key, Name: p.ID, Models: p.Models, HTTP: hc}.Complete(ctx, req)
		if err == nil {
			detail = "模型 " + resp.Model + " 正常响应"
			reply := resp.Content
			if r := []rune(reply); len(r) > 4000 {
				reply = string(r[:4000]) + "…"
			}
			in, out := resp.Usage.InputTokens, resp.Usage.OutputTokens
			res.Model, res.Reply, res.InputTokens, res.OutputTokens = &resp.Model, &reply, &in, &out
		}
	}
	res.LatencyMs = int(time.Since(start).Milliseconds())
	if err != nil {
		res.Message = testError(err)
		return res
	}
	res.Ok, res.Message = true, "连通正常，"+detail
	return res
}

func testError(err error) string {
	status := 0
	var se *llm.StatusError
	var sse *search.StatusError
	if errors.As(err, &se) {
		status = se.Status
	} else if errors.As(err, &sse) {
		status = sse.Status
	}
	if status != 0 {
		switch status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return "Key 无效或没有权限（HTTP " + strconv.Itoa(status) + "）"
		case http.StatusTooManyRequests:
			return "被限流或余额不足（HTTP 429）"
		}
	}
	msg := err.Error()
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return "失败：" + msg
}

// keySourceFor tells the gateway page whether provider has a usable key and where it comes from.
func (s *Server) keySourceFor(ctx context.Context, provider, env string) (bool, string) {
	src := s.Keys.Source(ctx, provider, env)
	return src != "", string(keySource(src))
}
