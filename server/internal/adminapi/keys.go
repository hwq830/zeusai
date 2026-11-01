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

// keyProvider is a provider whose key can be set in the console: every provider in the
// routing file, plus the search provider.
type keyProvider struct {
	ID, Name, Kind, BaseURL, EnvName string
	Models                           map[llm.Tier]string
	InRoute                          bool
}

var providerNames = map[string]string{
	"qwen":                  "通义千问",
	"deepseek":              "DeepSeek",
	providers.BochaProvider: "博查搜索",
}

func (s *Server) keyProviders() []keyProvider {
	var out []keyProvider
	if s.ModelGWConfig != "" {
		if cfg, err := modelgw.LoadConfig(s.ModelGWConfig); err == nil {
			routed := map[string]bool{}
			for _, names := range cfg.Routes {
				for _, n := range names {
					routed[n] = true
				}
			}
			for _, p := range cfg.Providers {
				out = append(out, keyProvider{ID: p.Name, Name: displayName(p.Name), Kind: "llm", BaseURL: p.BaseURL,
					EnvName: p.APIKeyEnv, Models: p.Models, InRoute: s.LLMProvider == "gateway" && routed[p.Name]})
			}
		}
	}
	return append(out, keyProvider{ID: providers.BochaProvider, Name: displayName(providers.BochaProvider), Kind: "search",
		BaseURL: s.bochaBaseURL(), EnvName: "ZEUS_BOCHA_API_KEY", InRoute: s.SearchProvider == "bocha"})
}

func displayName(id string) string {
	if n, ok := providerNames[id]; ok {
		return n
	}
	return id
}

func (s *Server) bochaBaseURL() string {
	if s.BochaBaseURL != "" {
		return s.BochaBaseURL
	}
	return "https://api.bochaai.com"
}

func (s *Server) keyProvider(id string) (keyProvider, bool) {
	for _, p := range s.keyProviders() {
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
	for _, p := range s.keyProviders() {
		base := p.BaseURL
		out.Providers = append(out.Providers, gen.KeyProvider{Id: p.ID, Name: p.Name, Kind: gen.KeyProviderKind(p.Kind),
			BaseUrl: &base, ApiKeyEnv: p.EnvName, EnvKeyPresent: p.EnvName != "" && os.Getenv(p.EnvName) != "",
			KeySource: keySource(s.Keys.Source(ctx, p.ID, p.EnvName)), InRoute: p.InRoute})
	}
	for _, r := range rows {
		out.Keys = append(out.Keys, toProviderKey(db.ProviderKey{ID: r.ID, Provider: r.Provider, Label: r.Label, Last4: r.Last4,
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
	p, ok := s.keyProvider(req.Body.Provider)
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
	p, ok := s.keyProvider(req.Body.Provider)
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
	return gen.TestNewProviderKey200JSONResponse(s.testKey(ctx, p, strings.TrimSpace(req.Body.ApiKey))), nil
}

func (s *Server) TestProviderKey(ctx context.Context, req gen.TestProviderKeyRequestObject) (gen.TestProviderKeyResponseObject, error) {
	if _, err := require(ctx, PermProviderKeys); err != nil {
		return nil, err
	}
	k, err := s.getKey(ctx, req.KeyId)
	if err != nil {
		return nil, err
	}
	p, ok := s.keyProvider(k.Provider)
	if !ok {
		return nil, apperr.Invalid("这个服务商已不在路由文件里")
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
		res = s.testKey(ctx, p, key)
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

// testKey sends the smallest real request: one output token from the fast model, or a
// one-result search.
func (s *Server) testKey(ctx context.Context, p keyProvider, key string) gen.ProviderKeyTest {
	if key == "" {
		return gen.ProviderKeyTest{Message: "Key 为空"}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	hc := &http.Client{Timeout: 30 * time.Second}
	start := time.Now()
	var err error
	var detail string
	switch p.Kind {
	case "search":
		var rs []search.Result
		rs, err = search.Bocha{APIKey: key, BaseURL: p.BaseURL, HTTP: hc}.Search(ctx, search.Query{Q: "今日新闻", Limit: 1})
		if err == nil {
			detail = "返回 " + strconv.Itoa(len(rs)) + " 条结果"
		}
	default:
		var resp llm.Response
		resp, err = llm.OpenAICompat{BaseURL: p.BaseURL, APIKey: key, Name: p.ID, Models: p.Models, HTTP: hc}.Complete(ctx,
			llm.Request{Purpose: "key_test", Tier: llm.TierFast, MaxTokens: 1, Messages: []llm.Message{{Role: "user", Content: "ping"}}})
		if err == nil {
			detail = "模型 " + resp.Model + " 正常响应"
		}
	}
	res := gen.ProviderKeyTest{LatencyMs: int(time.Since(start).Milliseconds())}
	if err != nil {
		res.Message = testError(err)
		return res
	}
	res.Ok, res.Message = true, "连通正常，"+detail
	return res
}

func testError(err error) string {
	var se *llm.StatusError
	if errors.As(err, &se) {
		switch se.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return "Key 无效或没有权限（HTTP " + strconv.Itoa(se.Status) + "）"
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
