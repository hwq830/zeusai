package adminapi

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/platform/providers"
)

var providerIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

type providerFields struct {
	name, baseURL, strong, fast string
	timeout                     int32
}

func cleanProviderFields(name, baseURL string, models *gen.ProviderTierModels, timeout *int) (providerFields, error) {
	f := providerFields{name: strings.TrimSpace(name), timeout: 120}
	if f.name == "" || len([]rune(f.name)) > 40 {
		return f, apperr.Invalid("请填写显示名称（不超过 40 字）")
	}
	base, err := cleanBaseURL(baseURL)
	if err != nil {
		return f, err
	}
	if base == "" {
		return f, apperr.Invalid("请填写 API 地址")
	}
	f.baseURL = base
	if models != nil {
		f.strong, f.fast = strings.TrimSpace(deref(models.Strong)), strings.TrimSpace(deref(models.Fast))
	}
	if len(f.strong) > 100 || len(f.fast) > 100 {
		return f, apperr.Invalid("模型名不超过 100 个字符")
	}
	if timeout != nil {
		if *timeout < 10 || *timeout > 600 {
			return f, apperr.Invalid("超时时间应在 10 到 600 秒之间")
		}
		f.timeout = int32(*timeout)
	}
	return f, nil
}

// consoleProvider loads a provider added in the console, refusing routing-file ones.
func (s *Server) consoleProvider(ctx context.Context, id string) (db.ModelProvider, error) {
	rows, err := s.Q.ListModelProviders(ctx)
	if err != nil {
		return db.ModelProvider{}, apperr.Internal(err)
	}
	for _, r := range rows {
		if r.ModelProvider.Name == id {
			return r.ModelProvider, nil
		}
	}
	if p, ok := s.keyProvider(ctx, id); ok && p.Source != "console" {
		return db.ModelProvider{}, apperr.Invalid("这个服务商来自服务端配置，只能在服务器上修改")
	}
	return db.ModelProvider{}, apperr.NotFound("没有这个服务商")
}

func (s *Server) providerView(ctx context.Context, id string) (gen.KeyProvider, error) {
	p, ok := s.keyProvider(ctx, id)
	if !ok {
		return gen.KeyProvider{}, apperr.Internal(errors.New("provider vanished: " + id))
	}
	return s.toKeyProvider(ctx, p), nil
}

func (s *Server) CreateModelProvider(ctx context.Context, req gen.CreateModelProviderRequestObject) (gen.CreateModelProviderResponseObject, error) {
	a, err := require(ctx, PermProviderKeys)
	if err != nil {
		return nil, err
	}
	b := req.Body
	id := strings.TrimSpace(b.Id)
	if !providerIDPattern.MatchString(id) {
		return nil, apperr.Invalid("标识只能用小写字母、数字和连字符，以字母开头，2–32 位，如 moonshot")
	}
	if id == providers.BochaProvider {
		return nil, apperr.Conflict("这个标识已被占用")
	}
	if _, ok := s.keyProvider(ctx, id); ok {
		return nil, apperr.Conflict("已有标识为 " + id + " 的服务商")
	}
	f, err := cleanProviderFields(b.Name, b.BaseUrl, &b.Models, b.TimeoutSeconds)
	if err != nil {
		return nil, err
	}
	if f.strong == "" && f.fast == "" {
		return nil, apperr.Invalid("至少填写一个档位的默认模型")
	}
	if b.ApiKey != nil && s.box() == nil {
		return nil, apperr.Invalid("服务端没有配置 ZEUS_SECRETS_KEY，不能在后台保存 Key")
	}
	if _, err := s.Q.CreateModelProvider(ctx, db.CreateModelProviderParams{Name: id, DisplayName: f.name, BaseUrl: f.baseURL,
		ModelStrong: f.strong, ModelFast: f.fast, TimeoutSeconds: f.timeout, CreatedBy: &a.ID}); err != nil {
		if isUniqueViolation(err) {
			return nil, apperr.Conflict("已有标识为 " + id + " 的服务商")
		}
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "model_provider.create", "model_provider", id, map[string]any{"name": f.name, "baseUrl": f.baseURL,
		"strong": f.strong, "fast": f.fast, "timeoutSeconds": f.timeout})
	if b.ApiKey != nil {
		activate := true
		if _, err := s.CreateProviderKey(ctx, gen.CreateProviderKeyRequestObject{Body: &gen.CreateProviderKeyJSONRequestBody{
			Provider: id, ApiKey: *b.ApiKey, Activate: &activate}}); err != nil {
			// The provider stays; the key can be added again from its card.
			return nil, err
		}
	}
	v, err := s.providerView(ctx, id)
	if err != nil {
		return nil, err
	}
	return gen.CreateModelProvider201JSONResponse(v), nil
}

func (s *Server) UpdateModelProvider(ctx context.Context, req gen.UpdateModelProviderRequestObject) (gen.UpdateModelProviderResponseObject, error) {
	if _, err := require(ctx, PermProviderKeys); err != nil {
		return nil, err
	}
	cur, err := s.consoleProvider(ctx, req.ProviderId)
	if err != nil {
		return nil, err
	}
	b := req.Body
	name, base := cur.DisplayName, cur.BaseUrl
	if b.Name != nil {
		name = *b.Name
	}
	if b.BaseUrl != nil {
		base = *b.BaseUrl
	}
	models := &gen.ProviderTierModels{Strong: &cur.ModelStrong, Fast: &cur.ModelFast}
	if b.Models != nil {
		if b.Models.Strong != nil {
			models.Strong = b.Models.Strong
		}
		if b.Models.Fast != nil {
			models.Fast = b.Models.Fast
		}
	}
	timeout := int(cur.TimeoutSeconds)
	if b.TimeoutSeconds != nil {
		timeout = *b.TimeoutSeconds
	}
	f, err := cleanProviderFields(name, base, models, &timeout)
	if err != nil {
		return nil, err
	}
	if f.strong == "" && f.fast == "" {
		return nil, apperr.Invalid("至少填写一个档位的默认模型")
	}
	enabled := cur.Enabled
	if b.Enabled != nil {
		enabled = *b.Enabled
	}
	if !enabled && cur.Enabled {
		if used, err := s.Q.ModelOverridesUsingProvider(ctx, cur.Name); err != nil {
			return nil, apperr.Internal(err)
		} else if len(used) > 0 {
			return nil, apperr.Conflict("「模型指定」里还在用这个服务商（" + scopeList(used) + "），先改掉再停用")
		}
	}
	if _, err := s.Q.UpdateModelProvider(ctx, db.UpdateModelProviderParams{Name: cur.Name, DisplayName: f.name, BaseUrl: f.baseURL,
		ModelStrong: f.strong, ModelFast: f.fast, TimeoutSeconds: f.timeout, Enabled: enabled}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.NotFound("没有这个服务商")
		}
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "model_provider.update", "model_provider", cur.Name, map[string]any{"name": f.name, "baseUrl": f.baseURL,
		"strong": f.strong, "fast": f.fast, "timeoutSeconds": f.timeout, "enabled": enabled})
	v, err := s.providerView(ctx, cur.Name)
	if err != nil {
		return nil, err
	}
	return gen.UpdateModelProvider200JSONResponse(v), nil
}

func (s *Server) DeleteModelProvider(ctx context.Context, req gen.DeleteModelProviderRequestObject) (gen.DeleteModelProviderResponseObject, error) {
	if _, err := require(ctx, PermProviderKeys); err != nil {
		return nil, err
	}
	cur, err := s.consoleProvider(ctx, req.ProviderId)
	if err != nil {
		return nil, err
	}
	used, err := s.Q.ModelOverridesUsingProvider(ctx, cur.Name)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if len(used) > 0 {
		return nil, apperr.Conflict("「模型指定」里还在用这个服务商（" + scopeList(used) + "），先改掉再删除")
	}
	keys, err := s.Q.DeleteProviderKeysFor(ctx, cur.Name)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if _, err := s.Q.DeleteModelProvider(ctx, cur.Name); err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "model_provider.delete", "model_provider", cur.Name, map[string]any{"name": cur.DisplayName, "keysDeleted": keys})
	return gen.DeleteModelProvider204Response{}, nil
}

func scopeList(scopes []string) string { return strings.Join(scopes, "、") }

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
