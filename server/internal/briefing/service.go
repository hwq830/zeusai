package briefing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hwq830/zeusai/prompts"
	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/llm"
	"github.com/hwq830/zeusai/server/internal/moderation"
	"github.com/hwq830/zeusai/server/internal/orch"
	"github.com/hwq830/zeusai/server/internal/quota"
	"github.com/hwq830/zeusai/server/internal/schedule"
	"github.com/hwq830/zeusai/server/internal/template"
)

type Service struct {
	Q         *db.Queries
	Quota     *quota.Service
	Templates *template.Service
	Orch      orch.Client
	LLM       llm.Provider
	Mod       moderation.Checker
	Now       func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

var lengths = map[string]bool{"short": true, "standard": true, "long": true}

func parseID(id, what string) (uuid.UUID, error) {
	u, err := uuid.Parse(id)
	if err != nil {
		return uuid.Nil, apperr.NotFound(what + "不存在")
	}
	return u, nil
}

// validate checks the input against its template and returns the template (if any).
func (s *Service) validate(ctx context.Context, userID uuid.UUID, in *Input) (*template.Template, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 40 {
		return nil, apperr.Invalid("简报名称不能为空且不超过 40 字")
	}
	if err := in.Schedule.Validate(); err != nil {
		return nil, err
	}
	if in.Length == "" {
		in.Length = "standard"
	}
	if !lengths[in.Length] {
		return nil, apperr.Invalid("篇幅应为 short / standard / long")
	}
	if len(in.Topics) > 10 {
		return nil, apperr.Invalid("关注点最多 10 个")
	}
	texts := append([]string{in.Name}, in.Topics...)
	for _, v := range in.FieldValues {
		texts = append(texts, template.ValueString(v))
	}
	if err := moderation.Require(ctx, s.Mod, texts...); err != nil {
		return nil, err
	}
	if in.FieldValues == nil {
		in.FieldValues = map[string]any{}
	}
	if in.TemplateID == nil {
		if len(in.Topics) == 0 {
			return nil, apperr.Invalid("请至少填写一个关注点或选择模板")
		}
		return nil, nil
	}
	t, err := s.Templates.Get(ctx, userID, *in.TemplateID)
	if err != nil {
		return nil, err
	}
	for _, f := range t.Fields {
		if f.Required && strings.TrimSpace(template.ValueString(in.FieldValues[f.Key])) == "" {
			return nil, apperr.Invalid(fmt.Sprintf("请填写「%s」", f.Label))
		}
	}
	return &t, nil
}

func (s *Service) List(ctx context.Context, userID uuid.UUID) ([]Subscription, error) {
	rows, err := s.Q.ListSubscriptions(ctx, userID)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	out := make([]Subscription, 0, len(rows))
	for _, r := range rows {
		out = append(out, FromRow(r))
	}
	return out, nil
}

func (s *Service) Get(ctx context.Context, userID uuid.UUID, id string) (Subscription, error) {
	r, err := s.getRow(ctx, userID, id)
	if err != nil {
		return Subscription{}, err
	}
	return FromRow(r), nil
}

func (s *Service) getRow(ctx context.Context, userID uuid.UUID, id string) (db.BriefingSubscription, error) {
	sid, err := parseID(id, "简报")
	if err != nil {
		return db.BriefingSubscription{}, err
	}
	r, err := s.Q.GetSubscription(ctx, db.GetSubscriptionParams{ID: sid, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return r, apperr.NotFound("简报不存在")
	}
	if err != nil {
		return r, apperr.Internal(err)
	}
	return r, nil
}

func (s *Service) Create(ctx context.Context, u db.User, in Input) (Subscription, error) {
	if err := s.Quota.CheckSubscription(ctx, u); err != nil {
		return Subscription{}, err
	}
	t, err := s.validate(ctx, u.ID, &in)
	if err != nil {
		return Subscription{}, err
	}
	p := db.CreateSubscriptionParams{
		UserID: u.ID, Name: in.Name, Topics: orEmpty(in.Topics), Length: in.Length, PushEnabled: in.PushEnabled == nil || *in.PushEnabled,
	}
	if t != nil {
		tid := uuid.MustParse(t.ID)
		v := int32(t.Version)
		p.TemplateID, p.TemplateVersion = &tid, &v
	}
	p.FieldValues, _ = json.Marshal(in.FieldValues)
	p.Schedule, _ = json.Marshal(in.Schedule)
	next := in.Schedule.Next(s.now())
	p.NextRunAt = &next
	r, err := s.Q.CreateSubscription(ctx, p)
	if err != nil {
		return Subscription{}, apperr.Internal(err)
	}
	if err := s.Orch.UpsertBriefingSchedule(ctx, u.ID, r.ID, in.Schedule, false); err != nil {
		_, _ = s.Q.DeleteSubscription(ctx, db.DeleteSubscriptionParams{ID: r.ID, UserID: u.ID})
		return Subscription{}, apperr.Unavailable("创建定时任务失败，请稍后再试").WithCause(err)
	}
	return FromRow(r), nil
}

func (s *Service) Update(ctx context.Context, userID uuid.UUID, id string, p Patch) (Subscription, error) {
	r, err := s.getRow(ctx, userID, id)
	if err != nil {
		return Subscription{}, err
	}
	cur := FromRow(r)
	in := Input{TemplateID: cur.TemplateID, Name: cur.Name, FieldValues: cur.FieldValues, Topics: cur.Topics, Schedule: cur.Schedule, Length: cur.Length, PushEnabled: &cur.PushEnabled}
	if p.Name != nil {
		in.Name = *p.Name
	}
	if p.FieldValues != nil {
		in.FieldValues = p.FieldValues
	}
	if p.Topics != nil {
		in.Topics = p.Topics
	}
	if p.Schedule != nil {
		in.Schedule = *p.Schedule
	}
	if p.Length != nil {
		in.Length = *p.Length
	}
	if p.PushEnabled != nil {
		in.PushEnabled = p.PushEnabled
	}
	if _, err := s.validate(ctx, userID, &in); err != nil {
		return Subscription{}, err
	}
	status, reason := r.Status, r.PausedReason
	if p.Paused != nil {
		if *p.Paused {
			status, reason = "paused", ptr("user")
		} else {
			status, reason = "active", nil
		}
	}
	var next *time.Time
	if status == "active" {
		n := in.Schedule.Next(s.now())
		next = &n
	}
	fv, _ := json.Marshal(in.FieldValues)
	sc, _ := json.Marshal(in.Schedule)
	r, err = s.Q.UpdateSubscription(ctx, db.UpdateSubscriptionParams{
		ID: r.ID, UserID: userID, Name: in.Name, FieldValues: fv, Topics: orEmpty(in.Topics), Schedule: sc,
		Length: in.Length, PushEnabled: *in.PushEnabled, Status: status, PausedReason: reason, NextRunAt: next,
	})
	if err != nil {
		return Subscription{}, apperr.Internal(err)
	}
	if err := s.Orch.UpsertBriefingSchedule(ctx, userID, r.ID, in.Schedule, status != "active"); err != nil {
		return Subscription{}, apperr.Unavailable("更新定时任务失败，请稍后再试").WithCause(err)
	}
	return FromRow(r), nil
}

func (s *Service) Delete(ctx context.Context, userID uuid.UUID, id string) error {
	sid, err := parseID(id, "简报")
	if err != nil {
		return err
	}
	n, err := s.Q.DeleteSubscription(ctx, db.DeleteSubscriptionParams{ID: sid, UserID: userID})
	if err != nil {
		return apperr.Internal(err)
	}
	if n == 0 {
		return apperr.NotFound("简报不存在")
	}
	if err := s.Orch.DeleteBriefingSchedule(ctx, sid); err != nil {
		return apperr.Unavailable("删除定时任务失败，请稍后再试").WithCause(err)
	}
	return nil
}

// RunNow creates a task and starts one issue immediately (preview; no push).
func (s *Service) RunNow(ctx context.Context, userID uuid.UUID, id string) (db.Task, error) {
	r, err := s.getRow(ctx, userID, id)
	if err != nil {
		return db.Task{}, err
	}
	active, err := s.Q.CountActiveTasks(ctx, userID)
	if err != nil {
		return db.Task{}, apperr.Internal(err)
	}
	if active >= MaxConcurrentTasks {
		return db.Task{}, apperr.RateLimited("同时进行的任务太多，请等当前任务完成")
	}
	t, err := s.Q.CreateTask(ctx, db.CreateTaskParams{
		UserID: userID, Kind: "briefing", Title: r.Name, Subtitle: "立即生成", Input: json.RawMessage(`{}`),
		SubscriptionID: &r.ID, PromptVersion: prompts.Version,
	})
	if err != nil {
		return db.Task{}, apperr.Internal(err)
	}
	if err := s.Orch.StartBriefing(ctx, orch.BriefingRun{UserID: userID, SubscriptionID: r.ID, TaskID: t.ID}); err != nil {
		_ = s.Q.UpdateTaskProgress(ctx, db.UpdateTaskProgressParams{ID: t.ID, UserID: userID, Status: "failed"})
		return db.Task{}, apperr.Unavailable("任务启动失败，请稍后再试").WithCause(err)
	}
	return t, nil
}

// MaxConcurrentTasks caps a user's running tasks (foreground and background).
const MaxConcurrentTasks = 2

type ListParams struct {
	SubscriptionID string
	Limit, Offset  int
}

func (s *Service) ListBriefings(ctx context.Context, userID uuid.UUID, p ListParams) ([]Briefing, error) {
	var sub *uuid.UUID
	if p.SubscriptionID != "" {
		id, err := parseID(p.SubscriptionID, "简报")
		if err != nil {
			return nil, err
		}
		sub = &id
	}
	rows, err := s.Q.ListBriefings(ctx, db.ListBriefingsParams{UserID: userID, SubscriptionID: sub, Limit: int32(p.Limit), Offset: int32(p.Offset)})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	out := make([]Briefing, 0, len(rows))
	for _, r := range rows {
		out = append(out, briefingFromRow(r, false))
	}
	return out, nil
}

// Today returns today's issues (user's zone) and subscriptions still due later today.
func (s *Service) Today(ctx context.Context, u db.User) (string, []Briefing, []Subscription, error) {
	loc, err := time.LoadLocation(u.Timezone)
	if err != nil {
		loc = time.UTC
	}
	now := s.now()
	date := schedule.LocalDate(now, loc)
	rows, err := s.Q.ListBriefingsOnDate(ctx, db.ListBriefingsOnDateParams{UserID: u.ID, Date: date})
	if err != nil {
		return "", nil, nil, apperr.Internal(err)
	}
	have := map[string]bool{}
	items := make([]Briefing, 0, len(rows))
	for _, r := range rows {
		b := briefingFromRow(r, true)
		have[b.SubscriptionID] = true
		items = append(items, b)
	}
	subs, err := s.List(ctx, u.ID)
	if err != nil {
		return "", nil, nil, err
	}
	upcoming := []Subscription{}
	endOfDay := time.Date(now.In(loc).Year(), now.In(loc).Month(), now.In(loc).Day()+1, 0, 0, 0, 0, loc)
	for _, sub := range subs {
		if sub.Status != "active" || have[sub.ID] {
			continue
		}
		if next := sub.Schedule.Next(now); !next.IsZero() && next.Before(endOfDay) {
			upcoming = append(upcoming, sub)
		}
	}
	return date.Format(time.DateOnly), items, upcoming, nil
}

// GetBriefing returns one issue and marks it read (which also keeps the subscription active).
func (s *Service) GetBriefing(ctx context.Context, userID uuid.UUID, id string) (Briefing, error) {
	bid, err := parseID(id, "简报")
	if err != nil {
		return Briefing{}, err
	}
	r, err := s.Q.GetBriefing(ctx, db.GetBriefingParams{ID: bid, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Briefing{}, apperr.NotFound("简报不存在")
	}
	if err != nil {
		return Briefing{}, apperr.Internal(err)
	}
	b := briefingFromRow(r, true)
	if r.Status == "ready" && r.ReadAt == nil {
		_ = s.Q.MarkBriefingRead(ctx, db.MarkBriefingReadParams{ID: r.ID, UserID: userID})
		_ = s.Q.TouchSubscriptionOpened(ctx, db.TouchSubscriptionOpenedParams{ID: r.SubscriptionID, UserID: userID})
	}
	if rating, err := s.Q.GetBriefingFeedback(ctx, db.GetBriefingFeedbackParams{BriefingID: r.ID, UserID: userID}); err == nil {
		b.Feedback = &rating
	}
	if r.TaskID != nil {
		n, err := s.Q.SumTaskBilledTokens(ctx, r.TaskID)
		if err != nil {
			return Briefing{}, apperr.Internal(err)
		}
		b.TokensUsed = &n
	}
	return b, nil
}

func (s *Service) Feedback(ctx context.Context, userID uuid.UUID, id, itemID, rating, comment string) error {
	if rating != "useful" && rating != "meh" {
		return apperr.Invalid("rating 应为 useful / meh")
	}
	if _, err := s.GetBriefing(ctx, userID, id); err != nil {
		return err
	}
	bid := uuid.MustParse(id)
	if err := s.Q.UpsertBriefingFeedback(ctx, db.UpsertBriefingFeedbackParams{ID: bid, UserID: userID, ItemID: itemID, Rating: rating, Comment: comment}); err != nil {
		return apperr.Internal(err)
	}
	return nil
}

// Draft turns one sentence into a subscription draft. Nothing is saved.
func (s *Service) Draft(ctx context.Context, u db.User, prompt string) (Input, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return Input{}, apperr.Invalid("请描述你想要的简报")
	}
	if err := moderation.Require(ctx, s.Mod, prompt); err != nil {
		return Input{}, err
	}
	sys, err := prompts.FS.ReadFile("briefing/draft_subscription.md")
	if err != nil {
		return Input{}, apperr.Internal(err)
	}
	var out struct {
		Name     string   `json:"name"`
		Topics   []string `json:"topics"`
		Freq     string   `json:"freq"`
		Weekdays []int    `json:"weekdays"`
		MonthDay int      `json:"monthDay"`
		Time     string   `json:"time"`
		Length   string   `json:"length"`
	}
	if _, err := llm.CompleteJSON(ctx, s.LLM, llm.Request{
		Purpose: "subscription.draft", Tier: llm.TierFast, System: string(sys),
		Messages: []llm.Message{{Role: "user", Content: prompt}}, UserID: &u.ID,
	}, &out); err != nil {
		return Input{}, apperr.Unavailable("没能理解这句话，换个说法试试").WithCause(err)
	}
	in := Input{
		Name:     out.Name,
		Topics:   out.Topics,
		Schedule: schedule.Schedule{Freq: out.Freq, Weekdays: out.Weekdays, MonthDay: out.MonthDay, Time: out.Time, TZ: u.Timezone},
		Length:   out.Length,
	}
	if in.Name == "" {
		in.Name = "我的简报"
	}
	if in.Schedule.Freq == "" {
		in.Schedule.Freq = "daily"
	}
	if in.Schedule.Time == "" {
		in.Schedule.Time = "08:00"
	}
	if in.Schedule.Freq == "weekly" && len(in.Schedule.Weekdays) == 0 {
		in.Schedule.Weekdays = []int{1}
	}
	if in.Length == "" {
		in.Length = "standard"
	}
	return in, nil
}

func ptr[T any](v T) *T { return &v }

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
