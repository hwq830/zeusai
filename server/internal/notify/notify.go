// Package notify is the message center. Every notification is saved as an in-app message first;
// then the user's preferences, quiet hours and frequency caps decide whether it is also pushed
// through the underlying push.Notifier (APNs). Service implements push.Notifier, so workflows and
// the admin API keep calling Notify. Operator campaigns go through SendCampaignBatch.
package notify

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/i18n"
	"github.com/hwq830/zeusai/server/internal/push"
)

// Limits caps marketing pushes per user; messages over the cap still reach the message center.
type Limits struct {
	MarketingPerDay  int
	MarketingPerWeek int
}

var DefaultLimits = Limits{MarketingPerDay: 1, MarketingPerWeek: 3}

const (
	// Defaults when the user has no notification_prefs row: 22:00–08:00.
	defaultQuietStart = 22 * 60
	defaultQuietEnd   = 8 * 60
	flushBatch        = 200
)

var Categories = map[string]bool{
	push.CategoryBriefing: true, push.CategoryReport: true, push.CategoryService: true, push.CategoryMarketing: true,
}

type Service struct {
	Q *db.Queries
	// Sender delivers the push itself (push.APNs, or push.Log in dev).
	Sender push.Notifier
	Log    *slog.Logger
	Limits Limits
	Now    func() time.Time
}

var _ push.Notifier = (*Service)(nil)

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) limits() Limits {
	if s.Limits == (Limits{}) {
		return DefaultLimits
	}
	return s.Limits
}

func (s *Service) warn(msg string, args ...any) {
	if s.Log != nil {
		s.Log.Warn(msg, args...)
	}
}

// Notify saves the message and pushes it if the user's settings allow. Once the message is
// saved it returns nil: a failed push is recorded on the message, and retrying would only
// duplicate the message-center entry.
func (s *Service) Notify(ctx context.Context, userID uuid.UUID, n push.Notification) error {
	cat := n.Category
	if cat == "" {
		cat = push.CategoryService
	}
	if !Categories[cat] {
		return errors.New("notify: unknown category " + cat)
	}
	title, body := n.Title, n.Body
	if u, err := s.Q.GetUser(ctx, userID); err == nil {
		l := i18n.Parse(u.UiLanguage)
		title, body = l.Pick(n.Title, n.TitleEn), l.Pick(n.Body, n.BodyEn)
	} else if n.TitleEn != "" || n.BodyEn != "" {
		return err
	}
	m, err := s.Q.CreateMessage(ctx, db.CreateMessageParams{
		UserID: userID, Category: cat, Title: title, Body: body, ImageUrl: n.Image, Deeplink: n.DeepLink,
	})
	if err != nil {
		return err
	}
	if n.InboxOnly {
		if err := s.Q.SetMessagePush(ctx, db.SetMessagePushParams{ID: m.ID, PushStatus: "skipped"}); err != nil {
			s.warn("notify: record push", "message", m.ID, "err", err)
		}
		return nil
	}
	s.deliver(ctx, m, false)
	return nil
}

// deliver decides and records the push for a saved message. flushing is true for deferred
// (quiet-hours) messages being sent later.
func (s *Service) deliver(ctx context.Context, m db.Message, flushing bool) string {
	status, after, err := s.decide(ctx, m, flushing)
	if err != nil {
		s.warn("notify: decide", "message", m.ID, "err", err)
		status = "failed"
	}
	if status == "" {
		status = s.send(ctx, m)
	}
	var pushedAt, pushAfter *time.Time
	if status == "sent" {
		t := s.now()
		pushedAt = &t
	}
	if status == "deferred" {
		pushAfter = &after
	}
	if err := s.Q.SetMessagePush(ctx, db.SetMessagePushParams{ID: m.ID, PushStatus: status, PushAfter: pushAfter, PushedAt: pushedAt}); err != nil {
		s.warn("notify: record push", "message", m.ID, "err", err)
	}
	return status
}

// decide returns "" to push now, or the status that replaces the push.
func (s *Service) decide(ctx context.Context, m db.Message, flushing bool) (string, time.Time, error) {
	if flushing && m.ReadAt != nil {
		return "skipped", time.Time{}, nil // already seen in the app
	}
	p, loc, err := s.prefs(ctx, m.UserID)
	if err != nil {
		return "", time.Time{}, err
	}
	switch m.Category {
	case push.CategoryBriefing:
		if !p.Briefing {
			return "off", time.Time{}, nil
		}
	case push.CategoryReport:
		if !p.Report {
			return "off", time.Time{}, nil
		}
	case push.CategoryMarketing:
		if !p.Marketing {
			return "off", time.Time{}, nil
		}
		now, lim := s.now(), s.limits()
		day, err := s.Q.CountMarketingPushesSince(ctx, db.CountMarketingPushesSinceParams{UserID: m.UserID, PushedAt: ptr(now.Add(-24 * time.Hour))})
		if err != nil {
			return "", time.Time{}, err
		}
		week, err := s.Q.CountMarketingPushesSince(ctx, db.CountMarketingPushesSinceParams{UserID: m.UserID, PushedAt: ptr(now.Add(-7 * 24 * time.Hour))})
		if err != nil {
			return "", time.Time{}, err
		}
		if int(day) >= lim.MarketingPerDay || int(week) >= lim.MarketingPerWeek {
			return "capped", time.Time{}, nil
		}
	}
	// A briefing arrives at the delivery time the user picked, so quiet hours do not apply.
	if m.Category != push.CategoryBriefing && p.QuietEnabled {
		if until, quiet := QuietUntil(s.now(), loc, int(p.QuietStart), int(p.QuietEnd)); quiet {
			return "deferred", until, nil
		}
	}
	return "", time.Time{}, nil
}

func (s *Service) send(ctx context.Context, m db.Message) string {
	unread, err := s.Q.CountUnreadMessages(ctx, m.UserID)
	if err != nil {
		s.warn("notify: unread count", "user", m.UserID, "err", err)
	}
	badge := int(unread)
	err = s.Sender.Notify(ctx, m.UserID, push.Notification{
		Category: m.Category, Title: m.Title, Body: m.Body, DeepLink: m.Deeplink, Image: m.ImageUrl,
		MessageID: m.ID.String(), Badge: &badge,
	})
	switch {
	case errors.Is(err, push.ErrNoDevice):
		return "no_device"
	case err != nil:
		s.warn("notify: push failed", "message", m.ID, "user", m.UserID, "err", err)
		return "failed"
	}
	return "sent"
}

func (s *Service) prefs(ctx context.Context, userID uuid.UUID) (db.NotificationPref, *time.Location, error) {
	u, err := s.Q.GetUser(ctx, userID)
	if err != nil {
		return db.NotificationPref{}, nil, err
	}
	loc, err := time.LoadLocation(u.Timezone)
	if err != nil {
		loc, _ = time.LoadLocation("Asia/Shanghai")
	}
	p, err := s.Q.GetNotificationPrefs(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return defaultPrefs(userID), loc, nil
	}
	return p, loc, err
}

func defaultPrefs(userID uuid.UUID) db.NotificationPref {
	return db.NotificationPref{UserID: userID, Briefing: true, Report: true, QuietEnabled: true,
		QuietStart: defaultQuietStart, QuietEnd: defaultQuietEnd}
}

// QuietUntil reports whether now falls in the quiet window [start, end) (minutes after local
// midnight; start > end spans midnight) and, if so, when the window ends.
func QuietUntil(now time.Time, loc *time.Location, start, end int) (time.Time, bool) {
	if start == end {
		return time.Time{}, false
	}
	local := now.In(loc)
	mins := local.Hour()*60 + local.Minute()
	var quiet bool
	if start < end {
		quiet = mins >= start && mins < end
	} else {
		quiet = mins >= start || mins < end
	}
	if !quiet {
		return time.Time{}, false
	}
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	until := midnight.Add(time.Duration(end) * time.Minute)
	if !until.After(local) {
		until = time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, loc).Add(time.Duration(end) * time.Minute)
	}
	return until, true
}

// FlushDeferred pushes quiet-hours messages that are now due and returns how many it handled.
func (s *Service) FlushDeferred(ctx context.Context) (int, error) {
	rows, err := s.Q.ClaimDeferredMessages(ctx, db.ClaimDeferredMessagesParams{Due: s.now(), MaxRows: flushBatch})
	if err != nil {
		return 0, err
	}
	for _, m := range rows {
		s.deliver(ctx, m, true)
	}
	return len(rows), nil
}

// Run flushes deferred pushes every interval until ctx ends.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for {
				n, err := s.FlushDeferred(ctx)
				if err != nil {
					s.warn("notify: flush deferred", "err", err)
				}
				if n < flushBatch {
					break
				}
			}
		}
	}
}

// ValidDeepLink accepts routes the iOS app knows (ZeusCore DeepLink) and https pages, which the
// app opens in its in-app browser.
func ValidDeepLink(l string) bool {
	if l == "" {
		return true
	}
	for _, p := range []string{"zeus://briefing/", "zeus://report/", "zeus://task/", "zeus://briefing-subscription/",
		"zeus://feedback/", "zeus://template/", "zeus://research?topic="} {
		if strings.HasPrefix(l, p) && len(l) > len(p) {
			return true
		}
	}
	switch l {
	case "zeus://today", "zeus://messages", "zeus://templates", "zeus://connectors", "zeus://feedback":
		return true
	}
	return strings.HasPrefix(l, "https://") && len(l) > len("https://") && !strings.ContainsAny(l, " \n\t")
}

func ptr[T any](v T) *T { return &v }

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.NotFound("消息不存在")
	}
	return apperr.Internal(err)
}
