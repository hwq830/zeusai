// Package quota enforces per-plan limits. MVP has only the free plan
// (2 scheduled briefings, 5 research reports per calendar month in the user's zone).
// Operators can override either limit per user from the admin console.
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
)

type Limits struct {
	Subscriptions   int
	ReportsPerMonth int
}

var plans = map[string]Limits{
	"free": {Subscriptions: 2, ReportsPerMonth: 5},
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

// Plans lists the configured plans by code.
func Plans() []Plan {
	out := make([]Plan, 0, len(plans))
	for c, l := range plans {
		out = append(out, Plan{Code: c, Limits: l})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// Effective applies the user's override, if any, to their plan's limits.
func (s *Service) Effective(ctx context.Context, u db.User) (Limits, *db.UserQuotaOverride, error) {
	lim := LimitsFor(u.PlanCode)
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
	if o.ReportsPerMonth != nil {
		lim.ReportsPerMonth = int(*o.ReportsPerMonth)
	}
	return lim, &o, nil
}

type Item struct {
	Used, Limit int
	ResetsAt    *time.Time
}

type Usage struct {
	Subscriptions Item
	Reports       Item
}

type Service struct {
	Q   *db.Queries
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func monthWindow(now time.Time, tz string) (start, next time.Time) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	l := now.In(loc)
	start = time.Date(l.Year(), l.Month(), 1, 0, 0, 0, 0, loc)
	return start, start.AddDate(0, 1, 0)
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
	start, next := monthWindow(s.now(), u.Timezone)
	reports, err := s.Q.CountReportsSince(ctx, db.CountReportsSinceParams{UserID: u.ID, CreatedAt: start})
	if err != nil {
		return Usage{}, apperr.Internal(err)
	}
	return Usage{
		Subscriptions: Item{Used: int(subs), Limit: lim.Subscriptions},
		Reports:       Item{Used: int(reports), Limit: lim.ReportsPerMonth, ResetsAt: &next},
	}, nil
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

func (s *Service) CheckReport(ctx context.Context, u db.User) error {
	us, err := s.Usage(ctx, u)
	if err != nil {
		return err
	}
	if us.Reports.Used >= us.Reports.Limit {
		return apperr.QuotaExceeded("本月调研报告额度已用完，下月 1 日重置").
			WithDetails(map[string]any{"quota": "reports", "limit": us.Reports.Limit, "resetsAt": us.Reports.ResetsAt})
	}
	return nil
}
