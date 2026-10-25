package adminapi

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/schedule"
)

func toUserSummary(u db.User) gen.UserSummary {
	methods := []gen.UserSummaryLoginMethods{}
	if u.Phone != nil {
		methods = append(methods, "phone")
	}
	if u.AppleSub != nil {
		methods = append(methods, "apple")
	}
	if u.WechatUnionid != nil {
		methods = append(methods, "wechat")
	}
	reason := u.DisabledReason
	return gen.UserSummary{
		Id: u.ID.String(), Nickname: u.Nickname, PhoneMasked: maskPhone(u.Phone), LoginMethods: methods,
		PlanCode: u.PlanCode, PlanExpiresAt: u.PlanExpiresAt, Timezone: u.Timezone,
		Disabled: u.DisabledAt != nil, DisabledAt: u.DisabledAt, DisabledReason: &reason, CreatedAt: u.CreatedAt,
	}
}

func (s *Server) ListUsers(ctx context.Context, req gen.ListUsersRequestObject) (gen.ListUsersResponseObject, error) {
	p := req.Params
	lim, off := page(p.Cursor, p.Limit)
	var status *string
	if p.Status != nil {
		st := string(*p.Status)
		status = &st
	}
	rows, err := s.Q.AdminListUsers(ctx, db.AdminListUsersParams{
		Q: strPtr(p.Q), Plan: strPtr(p.Plan), Status: status, Limit: int32(lim), Offset: int32(off),
	})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	items := make([]gen.UserSummary, 0, len(rows))
	for _, u := range rows {
		items = append(items, toUserSummary(u))
	}
	return gen.ListUsers200JSONResponse{Items: items, NextCursor: nextCursor(len(rows), lim, off)}, nil
}

func (s *Server) loadUser(ctx context.Context, id string) (db.User, error) {
	uid, err := parseID(id, "用户")
	if err != nil {
		return db.User{}, err
	}
	u, err := s.Q.GetUser(ctx, uid)
	if err != nil {
		return u, notFound(err, "用户")
	}
	return u, nil
}

func (s *Server) userDetail(ctx context.Context, u db.User) (gen.UserDetail, error) {
	var d gen.UserDetail
	d.User = toUserSummary(u)
	if can(current(ctx).Role, PermUsers) {
		d.Phone = u.Phone
	}
	usage, err := s.Quota.Usage(ctx, u)
	if err != nil {
		return d, err
	}
	_, o, err := s.Quota.Effective(ctx, u)
	if err != nil {
		return d, err
	}
	plan := s.Quota.PlanLimits(u)
	d.Quota.Subscriptions = gen.QuotaItem{Used: usage.Subscriptions.Used, Limit: usage.Subscriptions.Limit, PlanLimit: plan.Subscriptions}
	t := usage.Tokens
	d.Quota.Tokens = gen.TokenQuotaItem{Used: t.Used, Limit: t.Limit, PlanLimit: plan.TokensPerMonth, Granted: t.Granted, ResetsAt: t.ResetsAt}
	if o != nil {
		d.Quota.Override = &gen.QuotaOverride{
			Subscriptions: intPtr(o.Subscriptions), TokensPerMonth: o.TokensPerMonth, Note: o.Note, UpdatedAt: o.UpdatedAt,
		}
	}
	grants, err := s.Q.ListQuotaGrantsSince(ctx, db.ListQuotaGrantsSinceParams{UserID: u.ID, CreatedAt: t.PeriodStart})
	if err != nil {
		return d, apperr.Internal(err)
	}
	d.Quota.Grants = make([]gen.QuotaGrant, 0, len(grants))
	for _, g := range grants {
		d.Quota.Grants = append(d.Quota.Grants, gen.QuotaGrant{Id: g.ID, Tokens: g.Tokens, Reason: g.Reason, GrantedBy: idStr(g.GrantedBy), CreatedAt: g.CreatedAt})
	}
	subs, err := s.Q.ListSubscriptions(ctx, u.ID)
	if err != nil {
		return d, apperr.Internal(err)
	}
	d.Subscriptions = make([]gen.SubscriptionSummary, 0, len(subs))
	for _, r := range subs {
		d.Subscriptions = append(d.Subscriptions, gen.SubscriptionSummary{
			Id: r.ID.String(), Name: r.Name, TemplateId: idStr(r.TemplateID), Status: r.Status,
			PausedReason: r.PausedReason, NextRunAt: r.NextRunAt, CreatedAt: r.CreatedAt,
		})
	}
	c, err := s.Q.UserCostSince(ctx, db.UserCostSinceParams{UserID: &u.ID, CreatedAt: s.now().AddDate(0, 0, -30)})
	if err != nil {
		return d, apperr.Internal(err)
	}
	d.Cost30dCny, d.Tasks30d = cny(c.CostMicros), int(c.Tasks)
	return d, nil
}

func intPtr(p *int32) *int {
	if p == nil {
		return nil
	}
	v := int(*p)
	return &v
}

func int32Ptr(p *int) *int32 {
	if p == nil {
		return nil
	}
	v := int32(*p)
	return &v
}

func (s *Server) GetUser(ctx context.Context, req gen.GetUserRequestObject) (gen.GetUserResponseObject, error) {
	u, err := s.loadUser(ctx, req.UserId)
	if err != nil {
		return nil, err
	}
	d, err := s.userDetail(ctx, u)
	if err != nil {
		return nil, err
	}
	return gen.GetUser200JSONResponse(d), nil
}

func (s *Server) UpdateUserPlan(ctx context.Context, req gen.UpdateUserPlanRequestObject) (gen.UpdateUserPlanResponseObject, error) {
	if _, err := require(ctx, PermUsers); err != nil {
		return nil, err
	}
	u, err := s.loadUser(ctx, req.UserId)
	if err != nil {
		return nil, err
	}
	known := false
	for _, p := range s.Quota.Plans() {
		known = known || p.Code == req.Body.PlanCode
	}
	if !known {
		return nil, apperr.Invalid("方案不存在")
	}
	u, err = s.Q.AdminSetUserPlan(ctx, db.AdminSetUserPlanParams{ID: u.ID, PlanCode: req.Body.PlanCode, PlanExpiresAt: req.Body.PlanExpiresAt})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "user.set_plan", "user", u.ID.String(), req.Body)
	d, err := s.userDetail(ctx, u)
	if err != nil {
		return nil, err
	}
	return gen.UpdateUserPlan200JSONResponse(d), nil
}

func (s *Server) SetUserQuota(ctx context.Context, req gen.SetUserQuotaRequestObject) (gen.SetUserQuotaResponseObject, error) {
	a, err := require(ctx, PermUsers)
	if err != nil {
		return nil, err
	}
	u, err := s.loadUser(ctx, req.UserId)
	if err != nil {
		return nil, err
	}
	b := req.Body
	if v := b.Subscriptions; v != nil && (*v < 0 || *v > 100) {
		return nil, apperr.Invalid("定时简报数应在 0–100 之间")
	}
	if v := b.TokensPerMonth; v != nil && (*v < 0 || *v > 1_000_000_000) {
		return nil, apperr.Invalid("每月 token 额度应在 0–10 亿之间")
	}
	if _, err := s.Q.UpsertQuotaOverride(ctx, db.UpsertQuotaOverrideParams{
		UserID: u.ID, Subscriptions: int32Ptr(b.Subscriptions), TokensPerMonth: b.TokensPerMonth,
		Note: strings.TrimSpace(b.Note), UpdatedBy: &a.ID,
	}); err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "user.set_quota", "user", u.ID.String(), b)
	d, err := s.userDetail(ctx, u)
	if err != nil {
		return nil, err
	}
	return gen.SetUserQuota200JSONResponse(d), nil
}

func (s *Server) GrantUserTokens(ctx context.Context, req gen.GrantUserTokensRequestObject) (gen.GrantUserTokensResponseObject, error) {
	a, err := require(ctx, PermUsers)
	if err != nil {
		return nil, err
	}
	u, err := s.loadUser(ctx, req.UserId)
	if err != nil {
		return nil, err
	}
	b := req.Body
	reason := strings.TrimSpace(b.Reason)
	if b.Tokens < 1 || b.Tokens > 100_000_000 {
		return nil, apperr.Invalid("加赠 token 应在 1–1 亿之间")
	}
	if reason == "" || utf8.RuneCountInString(reason) > 200 {
		return nil, apperr.Invalid("请填写原因（不超过 200 字）")
	}
	if _, err := s.Q.InsertQuotaGrant(ctx, db.InsertQuotaGrantParams{UserID: u.ID, Tokens: b.Tokens, Reason: reason, GrantedBy: &a.ID}); err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "user.grant_tokens", "user", u.ID.String(), b)
	d, err := s.userDetail(ctx, u)
	if err != nil {
		return nil, err
	}
	return gen.GrantUserTokens200JSONResponse(d), nil
}

func (s *Server) ClearUserQuota(ctx context.Context, req gen.ClearUserQuotaRequestObject) (gen.ClearUserQuotaResponseObject, error) {
	if _, err := require(ctx, PermUsers); err != nil {
		return nil, err
	}
	u, err := s.loadUser(ctx, req.UserId)
	if err != nil {
		return nil, err
	}
	if err := s.Q.DeleteQuotaOverride(ctx, u.ID); err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "user.clear_quota", "user", u.ID.String(), nil)
	d, err := s.userDetail(ctx, u)
	if err != nil {
		return nil, err
	}
	return gen.ClearUserQuota200JSONResponse(d), nil
}

// DisableUser signs the user out everywhere and pauses their scheduled briefings. Access
// tokens already issued stay valid for at most their 15-minute lifetime on endpoints that do
// not load the user.
func (s *Server) DisableUser(ctx context.Context, req gen.DisableUserRequestObject) (gen.DisableUserResponseObject, error) {
	if !can(current(ctx).Role, PermUsers) && !can(current(ctx).Role, PermModeration) {
		return nil, apperr.Forbidden("当前角色没有这个权限")
	}
	u, err := s.loadUser(ctx, req.UserId)
	if err != nil {
		return nil, err
	}
	reason := strings.TrimSpace(req.Body.Reason)
	if reason == "" {
		return nil, apperr.Invalid("请填写停用原因")
	}
	u, err = s.Q.AdminSetUserDisabled(ctx, db.AdminSetUserDisabledParams{ID: u.ID, Disabled: true, Reason: reason})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if err := s.Q.RevokeAllRefreshTokens(ctx, u.ID); err != nil {
		return nil, apperr.Internal(err)
	}
	paused, err := s.Q.SetSubscriptionsPausedFor(ctx, u.ID)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if err := s.syncSchedules(ctx, u.ID, paused, true); err != nil {
		return nil, err
	}
	s.audit(ctx, "user.disable", "user", u.ID.String(), map[string]any{"reason": reason, "pausedSubscriptions": len(paused)})
	d, err := s.userDetail(ctx, u)
	if err != nil {
		return nil, err
	}
	return gen.DisableUser200JSONResponse(d), nil
}

func (s *Server) EnableUser(ctx context.Context, req gen.EnableUserRequestObject) (gen.EnableUserResponseObject, error) {
	if !can(current(ctx).Role, PermUsers) && !can(current(ctx).Role, PermModeration) {
		return nil, apperr.Forbidden("当前角色没有这个权限")
	}
	u, err := s.loadUser(ctx, req.UserId)
	if err != nil {
		return nil, err
	}
	u, err = s.Q.AdminSetUserDisabled(ctx, db.AdminSetUserDisabledParams{ID: u.ID, Disabled: false, Reason: ""})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	resumed, err := s.Q.ResumeSubscriptionsFor(ctx, u.ID)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if err := s.syncSchedules(ctx, u.ID, resumed, false); err != nil {
		return nil, err
	}
	s.audit(ctx, "user.enable", "user", u.ID.String(), map[string]any{"resumedSubscriptions": len(resumed)})
	d, err := s.userDetail(ctx, u)
	if err != nil {
		return nil, err
	}
	return gen.EnableUser200JSONResponse(d), nil
}

// syncSchedules pauses or resumes the Temporal schedules behind subscriptions.
func (s *Server) syncSchedules(ctx context.Context, userID uuid.UUID, subs []db.BriefingSubscription, paused bool) error {
	for _, r := range subs {
		var sc schedule.Schedule
		if err := json.Unmarshal(r.Schedule, &sc); err != nil {
			return apperr.Internal(err)
		}
		if !paused {
			next := sc.Next(s.now())
			if err := s.Q.SetSubscriptionNextRun(ctx, db.SetSubscriptionNextRunParams{ID: r.ID, NextRunAt: &next}); err != nil {
				return apperr.Internal(err)
			}
		}
		if err := s.Orch.UpsertBriefingSchedule(ctx, userID, r.ID, sc, paused); err != nil {
			return apperr.Unavailable("更新定时任务失败，请重试").WithCause(err)
		}
	}
	return nil
}

func (s *Server) ListPlans(ctx context.Context, _ gen.ListPlansRequestObject) (gen.ListPlansResponseObject, error) {
	out := gen.ListPlans200JSONResponse{}
	for _, p := range s.Quota.Plans() {
		out = append(out, gen.Plan{Code: p.Code, Subscriptions: p.Subscriptions, TokensPerMonth: p.TokensPerMonth})
	}
	return out, nil
}
