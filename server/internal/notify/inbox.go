package notify

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/i18n"
	"github.com/hwq830/zeusai/server/internal/push"
)

type Message struct {
	ID         string    `json:"id"`
	Category   string    `json:"category"`
	Title      string    `json:"title"`
	Body       string    `json:"body"`
	ImageURL   string    `json:"imageUrl,omitempty"`
	DeepLink   string    `json:"deeplink,omitempty"`
	CampaignID string    `json:"campaignId,omitempty"`
	Read       bool      `json:"read"`
	CreatedAt  time.Time `json:"createdAt"`
}

func FromRow(m db.Message) Message {
	out := Message{ID: m.ID.String(), Category: m.Category, Title: m.Title, Body: m.Body, ImageURL: m.ImageUrl,
		DeepLink: m.Deeplink, Read: m.ReadAt != nil, CreatedAt: m.CreatedAt}
	if m.CampaignID != nil {
		out.CampaignID = m.CampaignID.String()
	}
	return out
}

func categories(cs []string) ([]string, error) {
	for _, c := range cs {
		if !Categories[c] {
			return nil, apperr.Invalid("消息类别不正确")
		}
	}
	return cs, nil
}

func (s *Service) List(ctx context.Context, userID uuid.UUID, cats []string, limit, offset int) ([]Message, error) {
	cats, err := categories(cats)
	if err != nil {
		return nil, err
	}
	rows, err := s.Q.ListMessages(ctx, db.ListMessagesParams{UserID: userID, Categories: cats, Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	out := make([]Message, len(rows))
	for i, m := range rows {
		out[i] = FromRow(m)
	}
	return out, nil
}

func (s *Service) UnreadCount(ctx context.Context, userID uuid.UUID) (int, error) {
	n, err := s.Q.CountUnreadMessages(ctx, userID)
	if err != nil {
		return 0, apperr.Internal(err)
	}
	return int(n), nil
}

func (s *Service) MarkRead(ctx context.Context, userID uuid.UUID, id string) error {
	mid, err := uuid.Parse(id)
	if err != nil {
		return apperr.NotFound("消息不存在")
	}
	return rowsOr404(s.Q.MarkMessageRead(ctx, db.MarkMessageReadParams{ID: mid, UserID: userID}))
}

// MarkOpened records that the user opened the app from this message's push.
func (s *Service) MarkOpened(ctx context.Context, userID uuid.UUID, id string) error {
	mid, err := uuid.Parse(id)
	if err != nil {
		return apperr.NotFound("消息不存在")
	}
	return rowsOr404(s.Q.MarkMessageOpened(ctx, db.MarkMessageOpenedParams{ID: mid, UserID: userID}))
}

func (s *Service) MarkAllRead(ctx context.Context, userID uuid.UUID, cats []string) error {
	cats, err := categories(cats)
	if err != nil {
		return err
	}
	if err := s.Q.MarkAllMessagesRead(ctx, db.MarkAllMessagesReadParams{UserID: userID, Categories: cats}); err != nil {
		return apperr.Internal(err)
	}
	return nil
}

func (s *Service) Delete(ctx context.Context, userID uuid.UUID, id string) error {
	mid, err := uuid.Parse(id)
	if err != nil {
		return apperr.NotFound("消息不存在")
	}
	return rowsOr404(s.Q.DeleteMessage(ctx, db.DeleteMessageParams{ID: mid, UserID: userID}))
}

func rowsOr404(n int64, err error) error {
	if err != nil {
		return apperr.Internal(err)
	}
	if n == 0 {
		return apperr.NotFound("消息不存在")
	}
	return nil
}

// ---- preferences

type QuietHours struct {
	Enabled bool   `json:"enabled"`
	Start   string `json:"start"` // HH:MM local time
	End     string `json:"end"`
}

// Prefs are the user's per-category push switches. Service messages are always pushed (subject
// to quiet hours); everything still reaches the message center.
type Prefs struct {
	Briefing   bool       `json:"briefing"`
	Report     bool       `json:"report"`
	Marketing  bool       `json:"marketing"`
	QuietHours QuietHours `json:"quietHours"`
}

func prefsFromRow(p db.NotificationPref) Prefs {
	return Prefs{Briefing: p.Briefing, Report: p.Report, Marketing: p.Marketing,
		QuietHours: QuietHours{Enabled: p.QuietEnabled, Start: clock(int(p.QuietStart)), End: clock(int(p.QuietEnd))}}
}

func clock(m int) string { return fmt.Sprintf("%02d:%02d", m/60, m%60) }

func parseClock(s string) (int, bool) {
	h, m, ok := strings.Cut(s, ":")
	if !ok || len(h) != 2 || len(m) != 2 {
		return 0, false
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, false
	}
	return hh*60 + mm, true
}

func (s *Service) Prefs(ctx context.Context, userID uuid.UUID) (Prefs, error) {
	p, err := s.Q.GetNotificationPrefs(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return prefsFromRow(defaultPrefs(userID)), nil
	}
	if err != nil {
		return Prefs{}, apperr.Internal(err)
	}
	return prefsFromRow(p), nil
}

func (s *Service) UpdatePrefs(ctx context.Context, userID uuid.UUID, in Prefs) (Prefs, error) {
	start, ok1 := parseClock(in.QuietHours.Start)
	end, ok2 := parseClock(in.QuietHours.End)
	if !ok1 || !ok2 {
		return Prefs{}, apperr.Invalid("免打扰时间格式应为 HH:MM")
	}
	if in.QuietHours.Enabled && start == end {
		return Prefs{}, apperr.Invalid("免打扰开始和结束时间不能相同")
	}
	old, err := s.Q.GetNotificationPrefs(ctx, userID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Prefs{}, apperr.Internal(err)
	}
	// Marketing consent: keep the time the user first turned it on, clear it when turned off.
	var optIn *time.Time
	if in.Marketing {
		optIn = old.MarketingOptInAt
		if optIn == nil {
			optIn = ptr(s.now())
		}
	}
	p, err := s.Q.UpsertNotificationPrefs(ctx, db.UpsertNotificationPrefsParams{
		UserID: userID, Briefing: in.Briefing, Report: in.Report, Marketing: in.Marketing, MarketingOptInAt: optIn,
		QuietEnabled: in.QuietHours.Enabled, QuietStart: int32(start), QuietEnd: int32(end),
	})
	if err != nil {
		return Prefs{}, apperr.Internal(err)
	}
	return prefsFromRow(p), nil
}

// TestSend pushes a preview straight to the user's devices, without a message-center entry.
func (s *Service) TestSend(ctx context.Context, userID uuid.UUID, n push.Notification) error {
	l := i18n.ZH
	if u, err := s.Q.GetUser(ctx, userID); err == nil {
		l = i18n.Parse(u.UiLanguage)
	}
	switch {
	case l == i18n.EN && n.TitleEn != "":
		n.Title, n.Body = "[Test] "+n.TitleEn, n.BodyEn
	case l == i18n.ZHHant:
		n.Title, n.Body = "[測試] "+i18n.Hant(n.Title), i18n.Hant(n.Body)
	default:
		n.Title = "[测试] " + n.Title
	}
	return s.Sender.Notify(ctx, userID, n)
}
