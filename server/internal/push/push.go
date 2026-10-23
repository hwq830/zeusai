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
	DeepLink string // zeus://briefing/{id} etc.
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
