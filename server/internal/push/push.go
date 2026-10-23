// Package push sends notifications to a user's devices. Production uses APNs (token auth via
// sideshow/apns2); Log is for dev and tests.
package push

import (
	"context"
	"log/slog"
	"sync"

	"github.com/google/uuid"
)

type Notification struct {
	Title    string
	Body     string
	DeepLink string // zeus://briefing/{id}, zeus://report/{id}
	Image    string // optional https cover image, shown by the iOS notification service extension
}

// APNsPayload is the JSON body sent to APNs. The iOS app reads the deep link from the
// top-level "deeplink" key and the cover from "image"; mutable-content lets its service
// extension download the image.
func APNsPayload(n Notification) map[string]any {
	aps := map[string]any{
		"alert": map[string]string{"title": n.Title, "body": n.Body},
		"sound": "default",
	}
	if n.Image != "" {
		aps["mutable-content"] = 1
	}
	p := map[string]any{"aps": aps}
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
