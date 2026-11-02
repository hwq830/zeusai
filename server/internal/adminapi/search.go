package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/market"
	"github.com/hwq830/zeusai/server/internal/search"
)

// searchOn tells whether the orchestrator calls real search providers (not the mock).
func (s *Server) searchOn() bool {
	return s.SearchProvider != "" && s.SearchProvider != "mock"
}

func (s *Server) searchChain(ctx context.Context) (db.SearchSetting, error) {
	st, err := s.Q.GetSearchSettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.SearchSetting{Chain: []string{}}, nil
	}
	return st, err
}

func searchRegions(st db.SearchSetting) map[string][]string {
	regions := map[string][]string{}
	if len(st.RegionalChains) > 0 {
		_ = json.Unmarshal(st.RegionalChains, &regions)
	}
	return regions
}

func searchUses(st db.SearchSetting, id string) bool {
	if slices.Contains(st.Chain, id) {
		return true
	}
	for _, chain := range searchRegions(st) {
		if slices.Contains(chain, id) {
			return true
		}
	}
	return false
}

func (s *Server) searchKeyProviders(ctx context.Context) []keyProvider {
	rows, err := s.Q.ListSearchProviders(ctx)
	if err != nil {
		return nil
	}
	st, _ := s.searchChain(ctx)
	out := make([]keyProvider, 0, len(rows))
	for _, r := range rows {
		order := slices.Index(st.Chain, r.Name) + 1
		src := "console"
		if r.Builtin {
			src = "builtin"
		}
		out = append(out, keyProvider{ID: r.Name, Name: r.DisplayName, Kind: "search", Protocol: r.Protocol,
			BaseURL: s.searchBaseURL(r.Protocol, r.BaseUrl), EnvName: r.ApiKeyEnv, Source: src, Enabled: r.Enabled,
			SearchOrder: order, InRoute: s.searchOn() && r.Enabled && searchUses(st, r.Name)})
	}
	return out
}

func (s *Server) searchProviderRow(ctx context.Context, id string) (db.SearchProvider, error) {
	rows, err := s.Q.ListSearchProviders(ctx)
	if err != nil {
		return db.SearchProvider{}, apperr.Internal(err)
	}
	for _, r := range rows {
		if r.Name == id {
			return r, nil
		}
	}
	return db.SearchProvider{}, apperr.NotFound("没有这个搜索服务商")
}

func (s *Server) searchRouting(ctx context.Context) (gen.SearchRouting, error) {
	st, err := s.searchChain(ctx)
	if err != nil {
		return gen.SearchRouting{}, apperr.Internal(err)
	}
	protos := make([]gen.SearchProtocol, 0, len(search.Protocols))
	for _, p := range search.Protocols {
		protos = append(protos, gen.SearchProtocol{Id: p.ID, Name: p.Name, DefaultBaseUrl: p.DefaultBaseURL, Region: p.Region, DocsUrl: p.DocsURL})
	}
	out := gen.SearchRouting{SearchProvider: s.SearchProvider, On: s.searchOn(), Chain: st.Chain, RegionalChains: searchRegions(st), Protocols: protos}
	if out.SearchProvider == "" {
		out.SearchProvider = "mock"
	}
	if !st.UpdatedAt.IsZero() {
		t := st.UpdatedAt
		out.UpdatedAt = &t
	}
	if st.UpdatedBy != nil {
		if a, err := s.Q.GetAdminUser(ctx, *st.UpdatedBy); err == nil {
			out.UpdatedBy = &a.Email
		}
	}
	return out, nil
}

func (s *Server) GetSearchRouting(ctx context.Context, _ gen.GetSearchRoutingRequestObject) (gen.GetSearchRoutingResponseObject, error) {
	r, err := s.searchRouting(ctx)
	if err != nil {
		return nil, err
	}
	return gen.GetSearchRouting200JSONResponse(r), nil
}

func (s *Server) SaveSearchRouting(ctx context.Context, req gen.SaveSearchRoutingRequestObject) (gen.SaveSearchRoutingResponseObject, error) {
	a, err := require(ctx, PermProviderKeys)
	if err != nil {
		return nil, err
	}
	chain := req.Body.Chain
	rows, err := s.Q.ListSearchProviders(ctx)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	validate := func(ids []string) error {
		if len(ids) == 0 || len(ids) > 5 {
			return apperr.Invalid("每条搜索链需要 1–5 个服务商")
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if seen[id] {
				return apperr.Invalid("同一个服务商不能出现两次")
			}
			seen[id] = true
			i := slices.IndexFunc(rows, func(r db.SearchProvider) bool { return r.Name == id })
			if i < 0 {
				return apperr.Invalid("没有搜索服务商 " + id)
			}
			if !rows[i].Enabled {
				return apperr.Invalid(rows[i].DisplayName + " 已停用，先启用再选")
			}
		}
		return nil
	}
	if err := validate(chain); err != nil {
		return nil, err
	}
	var regional json.RawMessage
	if req.Body.RegionalChains != nil {
		for region, ids := range *req.Body.RegionalChains {
			if !market.Valid(region) {
				return nil, apperr.Invalid(fmt.Sprintf("搜索地区 %q 无效", region))
			}
			if err := validate(ids); err != nil {
				return nil, err
			}
		}
		regional, err = json.Marshal(*req.Body.RegionalChains)
		if err != nil {
			return nil, apperr.Internal(err)
		}
	}
	before, err := s.searchChain(ctx)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	after, err := s.Q.SaveSearchSettings(ctx, db.SaveSearchSettingsParams{Chain: chain, RegionalChains: regional, UpdatedBy: &a.ID})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "search_routing.save", "search_routing", "search", map[string]any{
		"before": before.Chain, "after": after.Chain,
		"beforeRegions": searchRegions(before), "afterRegions": searchRegions(after),
	})
	r, err := s.searchRouting(ctx)
	if err != nil {
		return nil, err
	}
	return gen.SaveSearchRouting200JSONResponse(r), nil
}

func cleanSearchName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 40 {
		return "", apperr.Invalid("请填写显示名称（不超过 40 字）")
	}
	return name, nil
}

func (s *Server) CreateSearchProvider(ctx context.Context, req gen.CreateSearchProviderRequestObject) (gen.CreateSearchProviderResponseObject, error) {
	a, err := require(ctx, PermProviderKeys)
	if err != nil {
		return nil, err
	}
	b := req.Body
	id := strings.TrimSpace(b.Id)
	if !providerIDPattern.MatchString(id) {
		return nil, apperr.Invalid("标识只能用小写字母、数字和连字符，以字母开头，2–32 位，如 tavily")
	}
	if _, ok := s.keyProvider(ctx, id); ok {
		return nil, apperr.Conflict("已有标识为 " + id + " 的服务商")
	}
	protocol := string(b.Protocol)
	if !search.KnownProtocol(protocol) {
		return nil, apperr.Invalid("不支持的搜索接口")
	}
	name, err := cleanSearchName(b.Name)
	if err != nil {
		return nil, err
	}
	base, err := cleanBaseURL(deref(b.BaseUrl))
	if err != nil {
		return nil, err
	}
	if b.ApiKey != nil && s.box() == nil {
		return nil, apperr.Invalid("服务端没有配置 ZEUS_SECRETS_KEY，不能在后台保存 Key")
	}
	if _, err := s.Q.CreateSearchProvider(ctx, db.CreateSearchProviderParams{Name: id, DisplayName: name, Protocol: protocol,
		BaseUrl: base, CreatedBy: &a.ID}); err != nil {
		if isUniqueViolation(err) {
			return nil, apperr.Conflict("已有标识为 " + id + " 的服务商")
		}
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "search_provider.create", "search_provider", id, map[string]any{"name": name, "protocol": protocol, "baseUrl": base})
	if b.ApiKey != nil {
		activate := true
		if _, err := s.CreateProviderKey(ctx, gen.CreateProviderKeyRequestObject{Body: &gen.CreateProviderKeyJSONRequestBody{
			Provider: id, ApiKey: *b.ApiKey, Activate: &activate}}); err != nil {
			return nil, err // the provider stays; the key can be added again from its card
		}
	}
	if deref(b.AddToChain) {
		st, err := s.searchChain(ctx)
		if err != nil {
			return nil, apperr.Internal(err)
		}
		if len(st.Chain) < 5 {
			chain := append(slices.Clone(st.Chain), id)
			if _, err := s.Q.SaveSearchSettings(ctx, db.SaveSearchSettingsParams{Chain: chain, UpdatedBy: &a.ID}); err != nil {
				return nil, apperr.Internal(err)
			}
			s.audit(ctx, "search_routing.save", "search_routing", "search", map[string]any{"before": st.Chain, "after": chain})
		}
	}
	v, err := s.providerView(ctx, id)
	if err != nil {
		return nil, err
	}
	return gen.CreateSearchProvider201JSONResponse(v), nil
}

func (s *Server) UpdateSearchProvider(ctx context.Context, req gen.UpdateSearchProviderRequestObject) (gen.UpdateSearchProviderResponseObject, error) {
	if _, err := require(ctx, PermProviderKeys); err != nil {
		return nil, err
	}
	cur, err := s.searchProviderRow(ctx, req.ProviderId)
	if err != nil {
		return nil, err
	}
	b := req.Body
	name, base, enabled := cur.DisplayName, cur.BaseUrl, cur.Enabled
	if b.Name != nil {
		if name, err = cleanSearchName(*b.Name); err != nil {
			return nil, err
		}
	}
	if b.BaseUrl != nil {
		if base, err = cleanBaseURL(*b.BaseUrl); err != nil {
			return nil, err
		}
	}
	if b.Enabled != nil {
		enabled = *b.Enabled
	}
	if !enabled && cur.Enabled {
		st, err := s.searchChain(ctx)
		if err != nil {
			return nil, apperr.Internal(err)
		}
		if searchUses(st, cur.Name) {
			return nil, apperr.Conflict("这个服务商正在主用或备用，先在搜索顺序里去掉再停用")
		}
	}
	if _, err := s.Q.UpdateSearchProvider(ctx, db.UpdateSearchProviderParams{Name: cur.Name, DisplayName: name, BaseUrl: base, Enabled: enabled}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.NotFound("没有这个搜索服务商")
		}
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "search_provider.update", "search_provider", cur.Name, map[string]any{"name": name, "baseUrl": base, "enabled": enabled})
	v, err := s.providerView(ctx, cur.Name)
	if err != nil {
		return nil, err
	}
	return gen.UpdateSearchProvider200JSONResponse(v), nil
}

func (s *Server) DeleteSearchProvider(ctx context.Context, req gen.DeleteSearchProviderRequestObject) (gen.DeleteSearchProviderResponseObject, error) {
	if _, err := require(ctx, PermProviderKeys); err != nil {
		return nil, err
	}
	cur, err := s.searchProviderRow(ctx, req.ProviderId)
	if err != nil {
		return nil, err
	}
	if cur.Builtin {
		return nil, apperr.Invalid("内置的博查搜索不能删除，不用时可以停用")
	}
	st, err := s.searchChain(ctx)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if searchUses(st, cur.Name) {
		return nil, apperr.Conflict("这个服务商正在主用或备用，先在搜索顺序里去掉再删除")
	}
	keys, err := s.Q.DeleteProviderKeysFor(ctx, cur.Name)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if _, err := s.Q.DeleteSearchProvider(ctx, cur.Name); err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "search_provider.delete", "search_provider", cur.Name, map[string]any{"name": cur.DisplayName, "keysDeleted": keys})
	return gen.DeleteSearchProvider204Response{}, nil
}
