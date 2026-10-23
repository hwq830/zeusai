// Package quota enforces per-plan limits. MVP has only the free plan
// (2 scheduled briefings, 5 research reports per calendar month in the user's zone).
package quota

import (
	"context"
	"time"

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
	lim := LimitsFor(u.PlanCode)
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
		return apperr.QuotaExceeded("免费版最多同时保留 2 个定时简报").
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
