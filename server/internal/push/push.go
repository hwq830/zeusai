// Package push sends notifications to a user's devices. Production uses APNs (token auth via
// sideshow/apns2); Log is for dev and tests.
package push

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/google/uuid"
)

// Message categories. Users can turn off pushes per category except service; marketing needs an opt-in.
const (
	CategoryBriefing  = "briefing"
	CategoryReport    = "report"
	CategoryService   = "service"
	CategoryMarketing = "marketing"
)

// ErrNoDevice means the user has no device that can receive the push.
var ErrNoDevice = errors.New("push: no device")

type Notification struct {
	// Category routes the notification through the user's preferences (notify.Service); empty is service.
	Category string
	Title    string
	Body     string
	// English texts for users whose display language is English; empty falls back to Title/Body.
	// notify.Service picks one before saving the message.
	TitleEn  string
	BodyEn   string
	DeepLink string // zeus://briefing/{id}, zeus://report/{id}
	Image    string // optional https cover image, shown by the iOS notification service extension
	// InboxOnly saves the message-center entry without pushing it (notify.Service).
	InboxOnly bool
	// Set by notify.Service: the message-center entry this push opens, and the app icon badge.
	MessageID string
	Badge     *int
}

// APNsPayload is the JSON body sent to APNs. The iOS app reads the deep link from the
// top-level "deeplink" key, the cover from "image" and the message-center entry from
// "message_id"; mutable-content lets its service extension download the image.
func APNsPayload(n Notification) map[string]any {
	aps := map[string]any{
		"alert": map[string]string{"title": n.Title, "body": n.Body},
		"sound": "default",
	}
	if n.Image != "" {
		aps["mutable-content"] = 1
	}
	if n.Badge != nil {
		aps["badge"] = *n.Badge
	}
	p := map[string]any{"aps": aps}
	if n.MessageID != "" {
		p["message_id"] = n.MessageID
	}
	if n.DeepLink != "" {
		p["deeplink"] = n.DeepLink
	}
	if n.Image != "" {
		p["image"] = n.Image
	}
	return p
}

type Notifier interface {
	Notify(ctx context.Context, userID uuid.UUID, n Notification) error
}

// Log records notifications in memory and logs them.
type Log struct {
	L    *slog.Logger
	mu   sync.Mutex
	Sent []Notification
}

func (l *Log) Notify(_ context.Context, userID uuid.UUID, n Notification) error {
	l.mu.Lock()
	l.Sent = append(l.Sent, n)
	l.mu.Unlock()
	if l.L != nil {
		l.L.Info("push", "user", userID, "title", n.Title, "link", n.DeepLink)
	}
	return nil
}

// Notifications returns a copy of what was sent so far.
func (l *Log) Notifications() []Notification {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Notification(nil), l.Sent...)
}
