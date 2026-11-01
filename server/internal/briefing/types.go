// Package briefing owns scheduled briefing subscriptions and the briefings they produce.
package briefing

import (
	"encoding/json"
	"time"

	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/events"
	"github.com/hwq830/zeusai/server/internal/schedule"
)

type Subscription struct {
	ID             string            `json:"id"`
	TemplateID     *string           `json:"templateId,omitempty"`
	Name           string            `json:"name"`
	FieldValues    map[string]any    `json:"fieldValues"`
	Topics         []string          `json:"topics"`
	Schedule       schedule.Schedule `json:"schedule"`
	Length         string            `json:"length"`
	PushEnabled    bool              `json:"pushEnabled"`
	Status         string            `json:"status"`
	PausedReason   *string           `json:"pausedReason,omitempty"`
	NextRunAt      *time.Time        `json:"nextRunAt,omitempty"`
	LastBriefingID *string           `json:"lastBriefingId,omitempty"`
	CreatedAt      time.Time         `json:"createdAt"`
}

type Input struct {
	TemplateID  *string           `json:"templateId,omitempty"`
	Name        string            `json:"name"`
	FieldValues map[string]any    `json:"fieldValues,omitempty"`
	Topics      []string          `json:"topics,omitempty"`
	Schedule    schedule.Schedule `json:"schedule"`
	Length      string            `json:"length,omitempty"`
	PushEnabled *bool             `json:"pushEnabled,omitempty"`
}

type Patch struct {
	Name        *string            `json:"name,omitempty"`
	FieldValues map[string]any     `json:"fieldValues,omitempty"`
	Topics      []string           `json:"topics,omitempty"`
	Schedule    *schedule.Schedule `json:"schedule,omitempty"`
	Length      *string            `json:"length,omitempty"`
	PushEnabled *bool              `json:"pushEnabled,omitempty"`
	Paused      *bool              `json:"paused,omitempty"`
}

type Item struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Summary     string          `json:"summary"`
	Sources     []events.Source `json:"sources"`
	DeepDivable bool            `json:"deepDivable"`
}

type Section struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	// Private sections hold personal content and are left out of shares.
	Private bool   `json:"private,omitempty"`
	Items   []Item `json:"items"`
}

type Briefing struct {
	ID             string     `json:"id"`
	SubscriptionID string     `json:"subscriptionId"`
	Title          string     `json:"title"`
	Date           string     `json:"date"`
	Status         string     `json:"status"`
	Headline       string     `json:"headline,omitempty"`
	ItemCount      int        `json:"itemCount"`
	SourceCount    int        `json:"sourceCount"`
	ReadAt         *time.Time `json:"readAt,omitempty"`
	GeneratedAt    *time.Time `json:"generatedAt,omitempty"`
	Language       string     `json:"language,omitempty"`
	Sections       []Section  `json:"sections"`
	TaskID         *string    `json:"taskId,omitempty"`
	Feedback       *string    `json:"feedback,omitempty"`
	TokensUsed     *int64     `json:"tokensUsed,omitempty"`
	// TakenDown: an operator took the issue down; sections and headline are left out.
	TakenDown      bool   `json:"takenDown,omitempty"`
	TakedownReason string `json:"takedownReason,omitempty"`
}

// FromRow converts a subscription row.
func FromRow(r db.BriefingSubscription) Subscription {
	s := Subscription{
		ID: r.ID.String(), Name: r.Name, Topics: r.Topics, Length: r.Length, PushEnabled: r.PushEnabled,
		Status: r.Status, PausedReason: r.PausedReason, NextRunAt: r.NextRunAt, CreatedAt: r.CreatedAt,
	}
	if r.TemplateID != nil {
		id := r.TemplateID.String()
		s.TemplateID = &id
	}
	if r.LastBriefingID != nil {
		id := r.LastBriefingID.String()
		s.LastBriefingID = &id
	}
	_ = json.Unmarshal(r.FieldValues, &s.FieldValues)
	if s.FieldValues == nil {
		s.FieldValues = map[string]any{}
	}
	_ = json.Unmarshal(r.Schedule, &s.Schedule)
	return s
}

func briefingFromRow(r db.Briefing, withSections bool) Briefing {
	b := Briefing{
		ID: r.ID.String(), SubscriptionID: r.SubscriptionID.String(), Title: r.Title, Date: r.Date.Format(time.DateOnly),
		Status: r.Status, Headline: r.Headline, ItemCount: int(r.ItemCount), SourceCount: int(r.SourceCount),
		ReadAt: r.ReadAt, GeneratedAt: r.GeneratedAt, Language: r.Language,
	}
	if r.TaskID != nil {
		id := r.TaskID.String()
		b.TaskID = &id
	}
	b.Sections = []Section{}
	if r.HiddenAt != nil {
		b.TakenDown, b.TakedownReason, b.Headline = true, r.HiddenReason, ""
		return b
	}
	if withSections {
		_ = json.Unmarshal(r.Sections, &b.Sections)
	}
	return b
}
