// Package quota enforces per-plan limits. MVP has only the free plan: 2 scheduled briefings,
// and a monthly allowance of billed tokens (see package billing) per calendar month in the
// user's zone. Users only ever see one token balance; the per-model weights behind it are an
// operations setting. Scheduled briefings keep running when the allowance is used up; only
// new reports and follow-ups are refused. Operators can override either limit per user and
// grant extra tokens from the admin console.
package quota

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/remoteconfig"
)

type Limits struct {
	Subscriptions  int
	TokensPerMonth int64
}

var plans = map[string]Limits{
	"free": {Subscriptions: 2, TokensPerMonth: 7_500_000},
}

func LimitsFor(plan string) Limits {
	if l, ok := plans[plan]; ok {
		return l
	}
	return plans["free"]
}

type Plan struct {
	Code string
	Limits
}

// Plans lists the configured plans by code, with the free plan's remote config defaults
// (rules that target specific users are not reflected here).
func (s *Service) Plans() []Plan {
	out := make([]Plan, 0, len(plans))
	for c := range plans {
		out = append(out, Plan{Code: c, Limits: s.PlanLimits(db.User{PlanCode: c})})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// PlanLimits is the user's plan limits after remote config (quota.free.* keys, which may
// target users like any other key), before the user's own override.
func (s *Service) PlanLimits(u db.User) Limits {
	lim := LimitsFor(u.PlanCode)
	if _, ok := plans[u.PlanCode]; !ok || u.PlanCode == "free" {
		rc := remoteconfig.UserContext(u, "")
		lim.Subscriptions = s.Config.Int(rc, remoteconfig.KeyQuotaSubscriptions, lim.Subscriptions)
		lim.TokensPerMonth = int64(s.Config.Int(rc, remoteconfig.KeyQuotaTokensPerMonth, int(lim.TokensPerMonth)))
	}
	return lim
}

// Effective applies the user's override, if any, to their plan's limits.
func (s *Service) Effective(ctx context.Context, u db.User) (Limits, *db.UserQuotaOverride, error) {
	lim := s.PlanLimits(u)
	o, err := s.Q.GetQuotaOverride(ctx, u.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return lim, nil, nil
	}
	if err != nil {
		return lim, nil, apperr.Internal(err)
	}
	if o.Subscriptions != nil {
		lim.Subscriptions = int(*o.Subscriptions)
	}
	if o.TokensPerMonth != nil {
		lim.TokensPerMonth = *o.TokensPerMonth
	}
	return lim, &o, nil
}

type Item struct {
	Used, Limit int
}

// Tokens is this quota month's billed-token balance. Limit includes Granted.
type Tokens struct {
	Used, Limit, Granted  int64
	PeriodStart, ResetsAt time.Time
}

func (t Tokens) Remaining() int64 { return max(t.Limit-t.Used, 0) }

type Usage struct {
	Subscriptions Item
	Tokens        Tokens
}

type Service struct {
	Q *db.Queries
	// Config overrides the free plan's limits; nil keeps the built-in ones.
	Config *remoteconfig.Cache
	Now    func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// MonthWindow is the quota month containing now in the user's zone.
func MonthWindow(now time.Time, tz string) (start, next time.Time) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	l := now.In(loc)
	start = time.Date(l.Year(), l.Month(), 1, 0, 0, 0, 0, loc)
	return start, start.AddDate(0, 1, 0)
}

// TokenUsage is the user's token balance for the current quota month.
func (s *Service) TokenUsage(ctx context.Context, u db.User) (Tokens, error) {
	lim, _, err := s.Effective(ctx, u)
	if err != nil {
		return Tokens{}, err
	}
	return s.tokens(ctx, u, lim)
}

func (s *Service) tokens(ctx context.Context, u db.User, lim Limits) (Tokens, error) {
	start, next := MonthWindow(s.now(), u.Timezone)
	used, err := s.Q.SumBilledTokensSince(ctx, db.SumBilledTokensSinceParams{UserID: &u.ID, CreatedAt: start})
	if err != nil {
		return Tokens{}, apperr.Internal(err)
	}
	granted, err := s.Q.SumQuotaGrantsSince(ctx, db.SumQuotaGrantsSinceParams{UserID: u.ID, CreatedAt: start})
	if err != nil {
		return Tokens{}, apperr.Internal(err)
	}
	return Tokens{Used: used, Limit: lim.TokensPerMonth + granted, Granted: granted, PeriodStart: start, ResetsAt: next}, nil
}

func (s *Service) Usage(ctx context.Context, u db.User) (Usage, error) {
	lim, _, err := s.Effective(ctx, u)
	if err != nil {
		return Usage{}, err
	}
	subs, err := s.Q.CountActiveSubscriptions(ctx, u.ID)
	if err != nil {
		return Usage{}, apperr.Internal(err)
	}
	tok, err := s.tokens(ctx, u, lim)
	if err != nil {
		return Usage{}, err
	}
	return Usage{Subscriptions: Item{Used: int(subs), Limit: lim.Subscriptions}, Tokens: tok}, nil
}

func (s *Service) CheckSubscription(ctx context.Context, u db.User) error {
	us, err := s.Usage(ctx, u)
	if err != nil {
		return err
	}
	if us.Subscriptions.Used >= us.Subscriptions.Limit {
		return apperr.QuotaExceeded(fmt.Sprintf("最多同时保留 %d 个定时简报", us.Subscriptions.Limit)).
			WithDetails(map[string]any{"quota": "briefingSubscriptions", "limit": us.Subscriptions.Limit})
	}
	return nil
}

// ReportMinTokens is the balance a new report needs, so one rarely runs out midway.
func (s *Service) ReportMinTokens(u db.User) int64 {
	return int64(s.Config.Int(remoteconfig.UserContext(u, ""), remoteconfig.KeyQuotaReportMinTokens, 500_000))
}

// Estimate is the billed tokens a report of one depth usually takes.
type Estimate struct {
	Min int64 `json:"min"`
	Max int64 `json:"max"`
}

var defaultEstimates = map[string]Estimate{
	"quick": {200_000, 400_000}, "standard": {400_000, 800_000}, "deep": {800_000, 1_500_000},
}

// ReportEstimates is shown before a report starts, keyed by depth.
func (s *Service) ReportEstimates(u db.User) map[string]Estimate {
	return remoteconfig.Value(s.Config, remoteconfig.UserContext(u, ""), remoteconfig.KeyQuotaReportEstimates, defaultEstimates)
}

func exceeded(msg string, t Tokens) error {
	return apperr.QuotaExceeded(msg).WithDetails(map[string]any{
		"quota": "tokens", "used": t.Used, "limit": t.Limit, "remaining": t.Remaining(), "resetsAt": t.ResetsAt,
	})
}

func resetDay(t Tokens) string {
	return fmt.Sprintf("%d 月 1 日", int(t.ResetsAt.Month()))
}

// CheckReport refuses a new report when the balance is below ReportMinTokens.
func (s *Service) CheckReport(ctx context.Context, u db.User) error {
	t, err := s.TokenUsage(ctx, u)
	if err != nil {
		return err
	}
	if t.Remaining() < s.ReportMinTokens(u) {
		if t.Remaining() == 0 {
			return exceeded("本月 token 额度已用完，"+resetDay(t)+"重置；定时简报照常推送", t)
		}
		return exceeded("本月剩余 token 不够生成一份报告，"+resetDay(t)+"重置；定时简报照常推送", t)
	}
	return nil
}

// CheckFollowup refuses a follow-up question once the balance is used up.
func (s *Service) CheckFollowup(ctx context.Context, u db.User) error {
	t, err := s.TokenUsage(ctx, u)
	if err != nil {
		return err
	}
	if t.Remaining() == 0 {
		return exceeded("本月 token 额度已用完，"+resetDay(t)+"重置", t)
	}
	return nil
}

// Thresholds are the usage percentages that trigger a reminder, highest last.
var Thresholds = []int{80, 100}

// NewThreshold records and returns the highest threshold the user has reached this quota
// month and not yet been told about; 0 when there is nothing to send.
func (s *Service) NewThreshold(ctx context.Context, u db.User) (int, Tokens, error) {
	t, err := s.TokenUsage(ctx, u)
	if err != nil || t.Limit <= 0 {
		return 0, t, err
	}
	reached := 0
	for _, th := range Thresholds {
		if t.Used*100 >= t.Limit*int64(th) {
			reached = th
		}
	}
	if reached == 0 {
		return 0, t, nil
	}
	// Mark every reached threshold so a lower one is never sent after a higher one.
	fresh := false
	for _, th := range Thresholds {
		if th > reached {
			break
		}
		n, err := s.Q.InsertQuotaNotice(ctx, db.InsertQuotaNoticeParams{UserID: u.ID, Period: t.PeriodStart, Threshold: int32(th)})
		if err != nil {
			return 0, t, apperr.Internal(err)
		}
		fresh = fresh || (n > 0 && th == reached)
	}
	if !fresh {
		return 0, t, nil
	}
	return reached, t, nil
}
