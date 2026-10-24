package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/remoteconfig"
)

// ---------------------------------------------------------------- conversions

// rawValue encodes a contract "any JSON" value; nil is JSON null.
func rawValue(v *interface{}) json.RawMessage {
	if v == nil {
		return json.RawMessage("null")
	}
	b, err := json.Marshal(*v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}

func anyValue(raw json.RawMessage) *interface{} {
	if len(raw) == 0 {
		return nil
	}
	var v interface{}
	if json.Unmarshal(raw, &v) != nil || v == nil {
		return nil
	}
	return &v
}

func toRules(in []gen.ConfigRule) []remoteconfig.Rule {
	out := make([]remoteconfig.Rule, 0, len(in))
	for _, r := range in {
		rule := remoteconfig.Rule{ID: strings.TrimSpace(r.Id), Note: deref(r.Note), When: []remoteconfig.Condition{}}
		for _, c := range r.When {
			rule.When = append(rule.When, remoteconfig.Condition{Attr: string(c.Attr), Op: string(c.Op), Values: c.Values})
		}
		if r.Rollout != nil {
			rule.Rollout = &remoteconfig.Rollout{Percent: float64(r.Rollout.Percent), Value: rawValue(r.Rollout.Value), Salt: deref(r.Rollout.Salt)}
		} else {
			rule.Value = rawValue(r.Value)
		}
		out = append(out, rule)
	}
	return out
}

func fromRules(in []remoteconfig.Rule) []gen.ConfigRule {
	out := make([]gen.ConfigRule, 0, len(in))
	for _, r := range in {
		rule := gen.ConfigRule{Id: r.ID, When: make([]gen.ConfigCondition, 0, len(r.When))}
		if r.Note != "" {
			n := r.Note
			rule.Note = &n
		}
		for _, c := range r.When {
			rule.When = append(rule.When, gen.ConfigCondition{Attr: gen.ConfigConditionAttr(c.Attr), Op: gen.ConfigConditionOp(c.Op), Values: c.Values})
		}
		if r.Rollout != nil {
			ro := &gen.ConfigRollout{Percent: float32(r.Rollout.Percent), Value: anyValue(r.Rollout.Value)}
			if r.Rollout.Salt != "" {
				s := r.Rollout.Salt
				ro.Salt = &s
			}
			rule.Rollout = ro
		} else {
			rule.Value = anyValue(r.Value)
		}
		out = append(out, rule)
	}
	return out
}

func toKeyState(k *remoteconfig.Key) *gen.ConfigKeyState {
	if k == nil {
		return nil
	}
	return &gen.ConfigKeyState{
		Namespace: gen.ConfigNamespace(k.Namespace), Type: gen.ConfigValueType(k.Type), DefaultValue: anyValue(k.Default),
		Rules: fromRules(k.Rules), ApplyMode: gen.ConfigApplyMode(k.ApplyMode), ClientVisible: k.ClientVisible, Expose: k.Expose,
	}
}

func toChanges(in []remoteconfig.Change) []gen.ConfigChange {
	out := make([]gen.ConfigChange, 0, len(in))
	for _, c := range in {
		out = append(out, gen.ConfigChange{Key: c.Key, Kind: gen.ConfigChangeKind(c.Kind), Before: toKeyState(c.Before), After: toKeyState(c.After)})
	}
	return out
}

func toConfigKey(r db.ConfigKey, published *remoteconfig.Snapshot, emails map[uuid.UUID]string) (gen.ConfigKey, error) {
	k, err := remoteconfig.KeyFromRow(r)
	if err != nil {
		return gen.ConfigKey{}, apperr.Internal(err)
	}
	out := gen.ConfigKey{
		Key: r.Key, Namespace: gen.ConfigNamespace(r.Namespace), Type: gen.ConfigValueType(r.Type), Description: r.Description,
		Owner: r.Owner, DefaultValue: anyValue(r.DefaultValue), Rules: fromRules(k.Rules), ApplyMode: gen.ConfigApplyMode(r.ApplyMode),
		ClientVisible: r.ClientVisible, Expose: r.Expose, Archived: r.ArchivedAt != nil, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if m := jsonObject(r.ValueSchema); m != nil {
		out.ValueSchema = &m
	}
	if r.UpdatedBy != nil {
		if e, ok := emails[*r.UpdatedBy]; ok {
			out.UpdatedByEmail = &e
		}
	}
	if pk, ok := published.Keys[r.Key]; ok {
		out.Published = toKeyState(&pk)
		out.Unpublished = r.ArchivedAt != nil || len(remoteconfig.Diff(
			&remoteconfig.Snapshot{Keys: map[string]remoteconfig.Key{r.Key: pk}},
			&remoteconfig.Snapshot{Keys: map[string]remoteconfig.Key{r.Key: k}})) > 0
	} else {
		out.Unpublished = r.ArchivedAt == nil
	}
	return out, nil
}

func (s *Server) adminEmails(ctx context.Context) map[uuid.UUID]string {
	out := map[uuid.UUID]string{}
	if as, err := s.Q.ListAdminUsers(ctx); err == nil {
		for _, a := range as {
			out[a.ID] = a.Email
		}
	}
	return out
}

func actor(ctx context.Context) remoteconfig.Actor {
	a := current(ctx)
	return remoteconfig.Actor{ID: a.ID, Role: a.Role}
}

// ---------------------------------------------------------------- keys

func (s *Server) ListConfigKeys(ctx context.Context, req gen.ListConfigKeysRequestObject) (gen.ListConfigKeysResponseObject, error) {
	rows, err := s.Q.ListConfigKeys(ctx, deref(req.Params.Archived))
	if err != nil {
		return nil, apperr.Internal(err)
	}
	pub, snap, err := remoteconfig.Published(ctx, s.Q)
	if err != nil {
		return nil, err
	}
	emails := s.adminEmails(ctx)
	out := gen.ListConfigKeys200JSONResponse{Items: make([]gen.ConfigKey, 0, len(rows))}
	if pub != nil {
		out.PublishedVersion = *pub.Version
	}
	if rel, err := s.Q.ListConfigReleases(ctx, db.ListConfigReleasesParams{Limit: 20}); err == nil {
		for _, r := range rel {
			if r.Status == "pending" {
				id := r.ID
				out.PendingReleaseId = &id
			}
		}
	}
	for _, r := range rows {
		k, err := toConfigKey(r, snap, emails)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, k)
	}
	return out, nil
}

func (s *Server) loadConfigKey(ctx context.Context, key string) (db.ConfigKey, error) {
	r, err := s.Q.GetConfigKey(ctx, key)
	if err != nil {
		return r, notFound(err, "配置项")
	}
	return r, nil
}

func (s *Server) configKeyResponse(ctx context.Context, r db.ConfigKey) (gen.ConfigKey, error) {
	_, snap, err := remoteconfig.Published(ctx, s.Q)
	if err != nil {
		return gen.ConfigKey{}, err
	}
	return toConfigKey(r, snap, s.adminEmails(ctx))
}

func (s *Server) GetConfigKey(ctx context.Context, req gen.GetConfigKeyRequestObject) (gen.GetConfigKeyResponseObject, error) {
	r, err := s.loadConfigKey(ctx, req.Key)
	if err != nil {
		return nil, err
	}
	k, err := s.configKeyResponse(ctx, r)
	if err != nil {
		return nil, err
	}
	return gen.GetConfigKey200JSONResponse(k), nil
}

func schemaRaw(m *map[string]interface{}) json.RawMessage {
	if m == nil {
		return nil
	}
	b, _ := json.Marshal(*m)
	return b
}

func (s *Server) CreateConfigKey(ctx context.Context, req gen.CreateConfigKeyRequestObject) (gen.CreateConfigKeyResponseObject, error) {
	a, err := require(ctx, PermConfigManage)
	if err != nil {
		return nil, err
	}
	b := req.Body
	key := strings.TrimSpace(b.Key)
	applyMode := remoteconfig.ApplyNextLaunch
	if b.ApplyMode != nil {
		applyMode = string(*b.ApplyMode)
	}
	if err := remoteconfig.ValidateDefinition(key, string(b.Namespace), string(b.Type), applyMode); err != nil {
		return nil, err
	}
	var schema json.RawMessage
	if b.Type == gen.ConfigValueType(remoteconfig.TypeJSON) {
		schema = schemaRaw(b.ValueSchema)
	}
	k := remoteconfig.Key{Namespace: string(b.Namespace), Type: string(b.Type), Default: rawValue(b.DefaultValue)}
	if err := remoteconfig.ValidateKey(key, k, schema); err != nil {
		return nil, err
	}
	clientVisible := true
	if b.ClientVisible != nil {
		clientVisible = *b.ClientVisible
	}
	r, err := s.Q.InsertConfigKey(ctx, db.InsertConfigKeyParams{
		Key: key, Namespace: k.Namespace, Type: k.Type, Description: strings.TrimSpace(deref(b.Description)),
		Owner: strings.TrimSpace(deref(b.Owner)), DefaultValue: k.Default, ValueSchema: schema, ApplyMode: applyMode,
		ClientVisible: clientVisible, Expose: deref(b.Expose), CreatedBy: &a.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.Conflict("配置项 " + key + " 已存在")
	}
	if err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "config.key_create", "config_key", key, map[string]any{"namespace": k.Namespace, "type": k.Type, "default": k.Default})
	out, err := s.configKeyResponse(ctx, r)
	if err != nil {
		return nil, err
	}
	return gen.CreateConfigKey201JSONResponse(out), nil
}

func (s *Server) UpdateConfigKey(ctx context.Context, req gen.UpdateConfigKeyRequestObject) (gen.UpdateConfigKeyResponseObject, error) {
	a, err := require(ctx, PermConfig)
	if err != nil {
		return nil, err
	}
	b := req.Body
	if (b.Namespace != nil || b.Archived != nil) && !can(a.Role, PermConfigManage) {
		return nil, apperr.Forbidden("改命名空间、归档需要 owner")
	}
	before, err := s.loadConfigKey(ctx, req.Key)
	if err != nil {
		return nil, err
	}
	// Build the key as it will be and validate it whole before writing anything.
	next, err := remoteconfig.KeyFromRow(before)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	schema := before.ValueSchema
	setSchema := false
	if b.Namespace != nil {
		next.Namespace = string(*b.Namespace)
	}
	if b.DefaultValue != nil || deref(b.DefaultValueNull) {
		next.Default = rawValue(b.DefaultValue)
	}
	if b.Rules != nil {
		next.Rules = toRules(*b.Rules)
	}
	if b.ApplyMode != nil {
		next.ApplyMode = string(*b.ApplyMode)
	}
	if deref(b.ClearValueSchema) {
		schema, setSchema = nil, true
	} else if b.ValueSchema != nil {
		if before.Type != remoteconfig.TypeJSON {
			return nil, apperr.Invalid("只有 json 类型可以设置 JSON Schema")
		}
		schema, setSchema = schemaRaw(b.ValueSchema), true
	}
	if err := remoteconfig.ValidateDefinition(before.Key, next.Namespace, next.Type, next.ApplyMode); err != nil {
		return nil, err
	}
	if err := remoteconfig.ValidateKey(before.Key, next, schema); err != nil {
		return nil, err
	}
	p := db.UpdateConfigKeyParams{
		Key: before.Key, UpdatedBy: &a.ID, Description: trimmed(b.Description), Owner: trimmed(b.Owner),
		ClientVisible: b.ClientVisible, Expose: b.Expose, Archived: b.Archived, SetSchema: setSchema, ValueSchema: schema,
	}
	if b.Namespace != nil {
		p.Namespace = &next.Namespace
	}
	if b.DefaultValue != nil || deref(b.DefaultValueNull) {
		p.DefaultValue = next.Default
	}
	if b.Rules != nil {
		p.Rules, _ = json.Marshal(next.Rules)
	}
	if b.ApplyMode != nil {
		p.ApplyMode = &next.ApplyMode
	}
	r, err := s.Q.UpdateConfigKey(ctx, p)
	if err != nil {
		return nil, notFound(err, "配置项")
	}
	s.audit(ctx, "config.key_update", "config_key", r.Key, map[string]any{"before": configAuditState(before), "after": configAuditState(r)})
	out, err := s.configKeyResponse(ctx, r)
	if err != nil {
		return nil, err
	}
	return gen.UpdateConfigKey200JSONResponse(out), nil
}

func configAuditState(r db.ConfigKey) map[string]any {
	return map[string]any{
		"namespace": r.Namespace, "default": r.DefaultValue, "rules": r.Rules, "applyMode": r.ApplyMode,
		"clientVisible": r.ClientVisible, "expose": r.Expose, "archived": r.ArchivedAt != nil, "schema": r.ValueSchema,
		"description": r.Description, "owner": r.Owner,
	}
}

func trimmed(p *string) *string {
	if p == nil {
		return nil
	}
	t := strings.TrimSpace(*p)
	return &t
}

// ---------------------------------------------------------------- releases

func (s *Server) ListConfigChanges(ctx context.Context, _ gen.ListConfigChangesRequestObject) (gen.ListConfigChangesResponseObject, error) {
	next, _, err := remoteconfig.Working(ctx, s.Q)
	if err != nil {
		return nil, err
	}
	pub, cur, err := remoteconfig.Published(ctx, s.Q)
	if err != nil {
		return nil, err
	}
	changes := remoteconfig.Diff(cur, next)
	out := gen.ListConfigChanges200JSONResponse{Changes: toChanges(changes)}
	if pub != nil {
		out.PublishedVersion = *pub.Version
	}
	if len(changes) > 0 && can(current(ctx).Role, PermConfig) {
		out.PublishesDirectly, err = s.Releases.PublishesDirectly(ctx, s.Q, actor(ctx), changes)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Server) ListConfigReleases(ctx context.Context, req gen.ListConfigReleasesRequestObject) (gen.ListConfigReleasesResponseObject, error) {
	lim, off := page(req.Params.Cursor, req.Params.Limit)
	rows, err := s.Q.ListConfigReleases(ctx, db.ListConfigReleasesParams{Limit: int32(lim), Offset: int32(off)})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	latest, err := s.Q.LatestConfigVersion(ctx)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	out := gen.ListConfigReleases200JSONResponse{Items: make([]gen.ConfigRelease, 0, len(rows)), NextCursor: nextCursor(len(rows), lim, off)}
	for _, r := range rows {
		out.Items = append(out.Items, toRelease(db.ConfigRelease{
			ID: r.ID, Version: r.Version, Status: r.Status, Note: r.Note, RolledBackFrom: r.RolledBackFrom,
			DecisionNote: r.DecisionNote, CreatedAt: r.CreatedAt, DecidedAt: r.DecidedAt, PublishedAt: r.PublishedAt,
		}, r.CreatedByEmail, r.DecidedByEmail, latest))
	}
	return out, nil
}

func toRelease(r db.ConfigRelease, createdBy, decidedBy string, latest int64) gen.ConfigRelease {
	out := gen.ConfigRelease{
		Id: r.ID, Version: r.Version, Status: gen.ConfigReleaseStatus(r.Status), Note: r.Note, RolledBackFrom: r.RolledBackFrom,
		CreatedByEmail: createdBy, DecidedByEmail: decidedBy, DecisionNote: r.DecisionNote, CreatedAt: r.CreatedAt,
		DecidedAt: r.DecidedAt, PublishedAt: r.PublishedAt,
	}
	if r.Version != nil && *r.Version == latest {
		t := true
		out.Current = &t
	}
	return out
}

// releaseDetail compares a published release with the published version before it, and
// any other release with what is live now.
func (s *Server) releaseDetail(ctx context.Context, r db.ConfigRelease) (gen.ConfigReleaseDetail, error) {
	snap, err := remoteconfig.ParseSnapshot(0, r.Snapshot)
	if err != nil {
		return gen.ConfigReleaseDetail{}, apperr.Internal(err)
	}
	var base *remoteconfig.Snapshot
	var baseVersion int64
	if r.Version != nil {
		prev, err := s.Q.PreviousPublishedConfig(ctx, r.Version)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			base = &remoteconfig.Snapshot{Keys: map[string]remoteconfig.Key{}}
		case err != nil:
			return gen.ConfigReleaseDetail{}, apperr.Internal(err)
		default:
			if base, err = remoteconfig.ParseSnapshot(*prev.Version, prev.Snapshot); err != nil {
				return gen.ConfigReleaseDetail{}, apperr.Internal(err)
			}
			baseVersion = *prev.Version
		}
	} else {
		pub, cur, err := remoteconfig.Published(ctx, s.Q)
		if err != nil {
			return gen.ConfigReleaseDetail{}, err
		}
		base = cur
		if pub != nil {
			baseVersion = *pub.Version
		}
	}
	latest, err := s.Q.LatestConfigVersion(ctx)
	if err != nil {
		return gen.ConfigReleaseDetail{}, apperr.Internal(err)
	}
	emails := s.adminEmails(ctx)
	email := func(id *uuid.UUID) string {
		if id == nil {
			return ""
		}
		return emails[*id]
	}
	rel := toRelease(r, email(r.CreatedBy), email(r.DecidedBy), latest)
	return gen.ConfigReleaseDetail{
		Id: rel.Id, Version: rel.Version, Status: rel.Status, Note: rel.Note, RolledBackFrom: rel.RolledBackFrom,
		CreatedByEmail: rel.CreatedByEmail, DecidedByEmail: rel.DecidedByEmail, DecisionNote: rel.DecisionNote,
		CreatedAt: rel.CreatedAt, DecidedAt: rel.DecidedAt, PublishedAt: rel.PublishedAt, Current: rel.Current,
		BaseVersion: baseVersion, Changes: toChanges(remoteconfig.Diff(base, snap)),
	}, nil
}

func (s *Server) GetConfigRelease(ctx context.Context, req gen.GetConfigReleaseRequestObject) (gen.GetConfigReleaseResponseObject, error) {
	r, err := s.Q.GetConfigRelease(ctx, req.ReleaseId)
	if err != nil {
		return nil, notFound(err, "发布记录")
	}
	d, err := s.releaseDetail(ctx, r)
	if err != nil {
		return nil, err
	}
	return gen.GetConfigRelease200JSONResponse(d), nil
}

func note(b *gen.ConfigReleaseRequest) string {
	if b == nil {
		return ""
	}
	return strings.TrimSpace(deref(b.Note))
}

func (s *Server) auditRelease(ctx context.Context, action string, r db.ConfigRelease, changes []remoteconfig.Change) {
	keys := make([]string, 0, len(changes))
	for _, c := range changes {
		keys = append(keys, c.Key)
	}
	sort.Strings(keys)
	s.audit(ctx, action, "config_release", idString(r.ID), map[string]any{
		"status": r.Status, "version": r.Version, "note": r.Note, "keys": keys, "rolledBackFrom": r.RolledBackFrom,
	})
}

func (s *Server) SubmitConfigRelease(ctx context.Context, req gen.SubmitConfigReleaseRequestObject) (gen.SubmitConfigReleaseResponseObject, error) {
	if _, err := require(ctx, PermConfig); err != nil {
		return nil, err
	}
	r, changes, err := s.Releases.Submit(ctx, actor(ctx), note(req.Body))
	if err != nil {
		return nil, err
	}
	s.auditRelease(ctx, "config.submit", r, changes)
	if r.Status == "published" {
		s.auditRelease(ctx, "config.publish", r, changes)
	}
	d, err := s.releaseDetail(ctx, r)
	if err != nil {
		return nil, err
	}
	return gen.SubmitConfigRelease201JSONResponse(d), nil
}

func (s *Server) ApproveConfigRelease(ctx context.Context, req gen.ApproveConfigReleaseRequestObject) (gen.ApproveConfigReleaseResponseObject, error) {
	if _, err := require(ctx, PermConfigApprove); err != nil {
		return nil, err
	}
	r, err := s.Releases.Approve(ctx, actor(ctx), req.ReleaseId, note(req.Body))
	if err != nil {
		return nil, err
	}
	d, err := s.releaseDetail(ctx, r)
	if err != nil {
		return nil, err
	}
	s.audit(ctx, "config.approve", "config_release", idString(r.ID), map[string]any{"version": r.Version, "changes": len(d.Changes), "note": r.DecisionNote})
	return gen.ApproveConfigRelease200JSONResponse(d), nil
}

func (s *Server) RejectConfigRelease(ctx context.Context, req gen.RejectConfigReleaseRequestObject) (gen.RejectConfigReleaseResponseObject, error) {
	if _, err := require(ctx, PermConfigApprove); err != nil {
		return nil, err
	}
	r, err := s.Releases.Reject(ctx, actor(ctx), req.ReleaseId, note(req.Body))
	if err != nil {
		return nil, err
	}
	s.audit(ctx, "config.reject", "config_release", idString(r.ID), map[string]any{"note": r.DecisionNote})
	d, err := s.releaseDetail(ctx, r)
	if err != nil {
		return nil, err
	}
	return gen.RejectConfigRelease200JSONResponse(d), nil
}

func (s *Server) RollbackConfigRelease(ctx context.Context, req gen.RollbackConfigReleaseRequestObject) (gen.RollbackConfigReleaseResponseObject, error) {
	if _, err := require(ctx, PermConfig); err != nil {
		return nil, err
	}
	r, changes, err := s.Releases.Rollback(ctx, actor(ctx), req.ReleaseId, note(req.Body))
	if err != nil {
		return nil, err
	}
	s.auditRelease(ctx, "config.rollback", r, changes)
	d, err := s.releaseDetail(ctx, r)
	if err != nil {
		return nil, err
	}
	return gen.RollbackConfigRelease201JSONResponse(d), nil
}

// ---------------------------------------------------------------- preview

func (s *Server) PreviewConfig(ctx context.Context, req gen.PreviewConfigRequestObject) (gen.PreviewConfigResponseObject, error) {
	b := req.Body
	c := remoteconfig.Context{
		InstallID: strings.TrimSpace(deref(b.InstallId)), AppVersion: strings.TrimSpace(deref(b.AppVersion)),
		Platform: "ios", OSVersion: deref(b.OsVersion), Channel: deref(b.Channel), Locale: deref(b.Locale), Region: s.Region,
	}
	if b.Platform != nil {
		c.Platform = *b.Platform
	}
	out := gen.PreviewConfig200JSONResponse{Values: []gen.ConfigPreviewValue{}}
	u, found, err := s.previewUser(ctx, deref(b.UserId), deref(b.Phone))
	if err != nil {
		return nil, err
	}
	if found {
		uc := remoteconfig.UserContext(u, s.Region)
		c.UserID, c.Plan, c.RegisteredAt = uc.UserID, uc.Plan, uc.RegisteredAt
		id := u.ID.String()
		out.UserId, out.Plan = &id, &u.PlanCode
	}
	var snap *remoteconfig.Snapshot
	if b.Source != nil && *b.Source == gen.ConfigPreviewRequestSourceWorking {
		if snap, _, err = remoteconfig.Working(ctx, s.Q); err != nil {
			return nil, err
		}
	} else {
		pub, cur, err := remoteconfig.Published(ctx, s.Q)
		if err != nil {
			return nil, err
		}
		snap = cur
		if pub != nil {
			out.Version = *pub.Version
		}
	}
	vals := snap.Evaluate(c, false)
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		r := vals[k]
		v := gen.ConfigPreviewValue{Key: k, Value: anyValue(r.Value), IsDefault: r.RuleID == "", ClientVisible: snap.Keys[k].ClientVisible}
		if r.RuleID != "" {
			id := r.RuleID
			v.RuleId = &id
		}
		out.Values = append(out.Values, v)
	}
	return out, nil
}

func (s *Server) previewUser(ctx context.Context, id, phone string) (db.User, bool, error) {
	id, phone = strings.TrimSpace(id), strings.TrimSpace(phone)
	switch {
	case id != "":
		uid, err := parseID(id, "用户")
		if err != nil {
			return db.User{}, false, err
		}
		u, err := s.Q.GetUser(ctx, uid)
		if err != nil {
			return u, false, notFound(err, "用户")
		}
		return u, true, nil
	case phone != "":
		candidates := []string{phone}
		if !strings.HasPrefix(phone, "+") {
			candidates = append(candidates, "+86"+phone)
		}
		for _, p := range candidates {
			p := p
			u, err := s.Q.GetUserByPhone(ctx, &p)
			if err == nil {
				return u, true, nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return u, false, apperr.Internal(err)
			}
		}
		return db.User{}, false, apperr.NotFound("没有这个手机号的用户")
	}
	return db.User{}, false, nil
}
