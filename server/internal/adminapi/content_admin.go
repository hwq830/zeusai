package adminapi

// 内容 → 调研报告 / 简报 / 内容质量: browse users' reports and briefings, re-run them for free,
// take them down, refund their tokens, leave notes and work through the exception queue.
// Reading a body needs content.read and is audited as content.view.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/hwq830/zeusai/prompts"
	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/briefing"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/events"
	"github.com/hwq830/zeusai/server/internal/i18n"
	"github.com/hwq830/zeusai/server/internal/orch"
	"github.com/hwq830/zeusai/server/internal/push"
	"github.com/hwq830/zeusai/server/internal/report"
	"github.com/hwq830/zeusai/server/internal/schedule"
)

// costlyReportMicros flags reports above gate B's ¥3 per report in the exception queue.
const costlyReportMicros = 3_000_000

func durationSeconds(start, end *time.Time) *int {
	if start == nil || end == nil {
		return nil
	}
	d := int(end.Sub(*start).Seconds())
	return &d
}

func toSources(raw json.RawMessage) []gen.ContentSource {
	var in []events.Source
	_ = json.Unmarshal(raw, &in)
	return convertSources(in)
}

func convertSources(in []events.Source) []gen.ContentSource {
	out := make([]gen.ContentSource, 0, len(in))
	for _, s := range in {
		cs := gen.ContentSource{Url: s.URL, Title: s.Title}
		if s.SiteName != "" {
			cs.SiteName = &s.SiteName
		}
		if u, err := url.Parse(s.URL); err == nil && u.Host != "" {
			h := strings.TrimPrefix(u.Hostname(), "www.")
			cs.Domain = &h
		}
		out = append(out, cs)
	}
	return out
}

func toShare(sh db.Share, title, nickname string, phone *string, base string) gen.AdminShare {
	link := strings.TrimRight(base, "/") + "/s/" + sh.Token
	out := gen.AdminShare{
		Id: sh.ID.String(), Title: title, UserId: sh.UserID.String(), Nickname: nickname, PhoneMasked: maskPhone(phone),
		Scope: sh.Scope, Channel: sh.Channel, ViewCount: int(sh.ViewCount), WechatViewCount: int(sh.WechatViewCount),
		CtaClicks: int(sh.CtaClicks), Url: &link, CreatedAt: sh.CreatedAt, ExpiresAt: sh.ExpiresAt, RevokedAt: sh.RevokedAt,
	}
	if sh.ReportID != nil {
		out.TargetType, out.TargetId = gen.AdminShareTargetTypeReport, sh.ReportID.String()
	} else if sh.BriefingID != nil {
		out.TargetType, out.TargetId = gen.AdminShareTargetTypeBriefing, sh.BriefingID.String()
	}
	if sh.ItemID != "" {
		out.ItemId = &sh.ItemID
	}
	return out
}

func (s *Server) notes(ctx context.Context, typ string, id uuid.UUID) ([]gen.ContentNote, error) {
	rows, err := s.Q.ListContentNotes(ctx, db.ListContentNotesParams{TargetType: typ, TargetID: id})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	out := make([]gen.ContentNote, 0, len(rows))
	for _, n := range rows {
		out = append(out, gen.ContentNote{
			Id: n.ID, TargetType: gen.NoteTargetType(n.TargetType), TargetId: n.TargetID.String(), Body: n.Body,
			AdminName: n.AdminName, CreatedAt: n.CreatedAt,
		})
	}
	return out, nil
}

// taskDetail is a task with its model calls, as on the 任务 page.
func (s *Server) taskDetail(ctx context.Context, t db.Task) (gen.TaskDetail, error) {
	usage, err := s.Q.ListTaskUsage(ctx, &t.ID)
	if err != nil {
		return gen.TaskDetail{}, apperr.Internal(err)
	}
	var total int64
	calls := make([]gen.ModelCall, 0, len(usage))
	for _, u := range usage {
		total += u.CostMicros
		calls = append(calls, gen.ModelCall{
			Provider: u.Provider, Model: u.Model, Tier: u.Tier, InputTokens: int(u.InputTokens), OutputTokens: int(u.OutputTokens),
			CachedTokens: int(u.CachedTokens), CostCny: cny(u.CostMicros), CreatedAt: u.CreatedAt,
		})
	}
	d := gen.TaskDetail{Task: toAdminTask(t, total), Input: orEmptyMap(jsonObject(t.Input)), Calls: calls}
	if m := jsonObject(t.Plan); m != nil {
		d.Plan = &m
	}
	return d, nil
}

func (s *Server) refunded(ctx context.Context, ref string) (bool, error) {
	_, err := s.Q.GetQuotaGrantByRef(ctx, ref)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, apperr.Internal(err)
	}
	return true, nil
}

// refund gives back what the tasks counted against the user's quota, once per ref.
func (s *Server) refund(ctx context.Context, userID uuid.UUID, ref, reason string, taskIDs []uuid.UUID) (gen.RefundResult, error) {
	a, err := require(ctx, PermUsers)
	if err != nil {
		return gen.RefundResult{}, err
	}
	if done, err := s.refunded(ctx, ref); err != nil {
		return gen.RefundResult{}, err
	} else if done {
		return gen.RefundResult{}, apperr.Conflict("已经退还过")
	}
	tokens, err := s.Q.ChargedTaskTokens(ctx, taskIDs)
	if err != nil {
		return gen.RefundResult{}, apperr.Internal(err)
	}
	if tokens <= 0 {
		return gen.RefundResult{Tokens: 0}, nil
	}
	g, err := s.Q.InsertQuotaGrantRef(ctx, db.InsertQuotaGrantRefParams{UserID: userID, Tokens: tokens, Reason: reason, GrantedBy: &a.ID, Ref: ref})
	if errors.Is(err, pgx.ErrNoRows) {
		return gen.RefundResult{}, apperr.Conflict("已经退还过")
	}
	if err != nil {
		return gen.RefundResult{}, apperr.Internal(err)
	}
	s.audit(ctx, "content.refund", strings.SplitN(ref, ":", 2)[0], strings.SplitN(ref, ":", 2)[1], map[string]any{"tokens": tokens, "user": userID})
	return gen.RefundResult{Tokens: tokens, Grant: &gen.QuotaGrant{Id: g.ID, Tokens: g.Tokens, Reason: g.Reason, GrantedBy: idStr(g.GrantedBy), CreatedAt: g.CreatedAt}}, nil
}

func hideReason(b *gen.HideRequest) (string, bool, error) {
	if b == nil {
		return "", false, apperr.Invalid("请填写下架原因")
	}
	r := strings.TrimSpace(b.Reason)
	if r == "" || utf8.RuneCountInString(r) > 200 {
		return "", false, apperr.Invalid("下架原因不能为空且不超过 200 字")
	}
	return r, b.Notify == nil || *b.Notify, nil
}

// noticeTakedown leaves a message-center entry (no push) telling the user what was taken down and why.
func (s *Server) noticeTakedown(ctx context.Context, userID uuid.UUID, kind, title, reason, link string) {
	if s.Push == nil {
		return
	}
	n := push.Notification{
		Category: push.CategoryService, InboxOnly: true, DeepLink: link,
		Title:   "你的一份" + kind + "已下架",
		TitleEn: "Some of your content was taken down",
		Body:    fmt.Sprintf("《%s》因「%s」已下架，分享链接已失效。如有疑问可在“意见反馈”里联系我们。", title, reason),
		BodyEn:  fmt.Sprintf("\"%s\" was taken down (%s) and its share links no longer work. Contact us through Feedback if you have questions.", title, reason),
	}
	if err := s.Push.Notify(ctx, userID, n); err != nil {
		s.Log.Warn("takedown notice failed", "user", userID, "err", err)
	}
}

// ------------------------------------------------------------------ reports

func toReportRow(r db.AdminListReportsRow) gen.AdminReportRow {
	out := gen.AdminReportRow{
		Id: r.ID.String(), UserId: r.UserID.String(), Nickname: r.Nickname, PhoneMasked: maskPhone(r.Phone),
		Title: r.Title, Depth: r.Depth, Status: r.Status, Hidden: r.HiddenAt != nil, SourceCount: int(r.SourceCount),
		WordCount: int(r.WordCount), DurationSeconds: durationSeconds(r.StartedAt, r.FinishedAt), CostCny: cny(r.CostMicros),
		Rating: r.Rating, Reasons: &r.Reasons, Followups: int(r.Followups), Shares: int(r.Shares), ShareViews: int(r.ShareViews),
		FromBriefing: r.SourceBriefingItemID != nil, AdminRun: r.AdminID != nil, RegeneratedFrom: idStr(r.RegeneratedFrom),
		PromptVersion: &r.PromptVersion, CreatedAt: r.CreatedAt, CompletedAt: r.CompletedAt,
	}
	return out
}

func optUser(id *string) (*uuid.UUID, error) {
	if id == nil || strings.TrimSpace(*id) == "" {
		return nil, nil
	}
	u, err := uuid.Parse(strings.TrimSpace(*id))
	if err != nil {
		return nil, apperr.Invalid("id 格式不正确")
	}
	return &u, nil
}

func enumPtr[T ~string](p *T) *string {
	if p == nil {
		return nil
	}
	v := string(*p)
	return &v
}

func (s *Server) ListReports(ctx context.Context, req gen.ListReportsRequestObject) (gen.ListReportsResponseObject, error) {
	p := req.Params
	lim, off := page(p.Cursor, p.Limit)
	uid, err := optUser(p.UserId)
	if err != nil {
		return nil, err
	}
	params := db.AdminListReportsParams{
		Limit: int32(lim), Offset: int32(off), UserID: uid, Q: strPtr(p.Q), Status: enumPtr(p.Status), Depth: enumPtr(p.Depth),
		CreatedFrom: p.From, CreatedTo: p.To, Rating: enumPtr(p.Rating), Origin: enumPtr(p.Origin), Shared: p.Shared,
		PromptVersion: strPtr(p.PromptVersion),
	}
	if p.MinCostCny != nil {
		m := int64(*p.MinCostCny * 1e6)
		params.MinCostMicros = &m
	}
	rows, err := s.Q.AdminListReports(ctx, params)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	items := make([]gen.AdminReportRow, 0, len(rows))
	for _, r := range rows {
		items = append(items, toReportRow(r))
	}
	return gen.ListReports200JSONResponse{Items: items, NextCursor: nextCursor(len(rows), lim, off)}, nil
}

func (s *Server) reportRow(ctx context.Context, id uuid.UUID) (gen.AdminReportRow, error) {
	rows, err := s.Q.AdminListReports(ctx, db.AdminListReportsParams{ID: &id, Limit: 1})
	if err != nil {
		return gen.AdminReportRow{}, apperr.Internal(err)
	}
	if len(rows) == 0 {
		return gen.AdminReportRow{}, apperr.NotFound("报告不存在")
	}
	return toReportRow(rows[0]), nil
}

func (s *Server) reportDetail(ctx context.Context, id uuid.UUID) (gen.AdminReportDetail, error) {
	r, err := s.Q.AdminGetReport(ctx, id)
	if err != nil {
		return gen.AdminReportDetail{}, notFound(err, "报告")
	}
	row, err := s.reportRow(ctx, id)
	if err != nil {
		return gen.AdminReportDetail{}, err
	}
	d := gen.AdminReportDetail{
		Report: row, Topic: r.Topic, Focuses: orEmpty(r.Focuses), Language: r.Language, Summary: r.Summary, Markdown: r.Markdown,
		Sources: toSources(r.Sources), HiddenReason: r.HiddenReason, HiddenAt: r.HiddenAt, SourceBriefingItemId: r.SourceBriefingItemID,
		Tasks: []gen.TaskDetail{}, Shares: []gen.AdminShare{}, Regenerations: []gen.ContentRef{},
	}
	tasks, err := s.Q.AdminListReportTasks(ctx, &id)
	if err != nil {
		return d, apperr.Internal(err)
	}
	for _, t := range tasks {
		td, err := s.taskDetail(ctx, db.Task{
			ID: t.ID, UserID: t.UserID, Kind: t.Kind, Status: t.Status, Title: t.Title, Subtitle: t.Subtitle, Input: t.Input,
			ReportID: t.ReportID, BriefingID: t.BriefingID, Plan: t.Plan, Error: t.Error, PromptVersion: t.PromptVersion,
			CreatedAt: t.CreatedAt, StartedAt: t.StartedAt, FinishedAt: t.FinishedAt, AdminID: t.AdminID,
		})
		if err != nil {
			return d, err
		}
		td.Task.Nickname, td.Task.PhoneMasked = row.Nickname, row.PhoneMasked
		d.Tasks = append(d.Tasks, td)
	}
	if f, err := s.Q.AdminGetReportFeedback(ctx, id); err == nil {
		d.Feedback = &gen.ContentFeedback{Rating: f.Rating, Reasons: orEmpty(f.Reasons), Comment: f.Comment, UpdatedAt: f.UpdatedAt}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return d, apperr.Internal(err)
	}
	shares, err := s.Q.AdminListSharesFor(ctx, db.AdminListSharesForParams{ReportID: &id})
	if err != nil {
		return d, apperr.Internal(err)
	}
	u, err := s.Q.GetUser(ctx, r.UserID)
	if err != nil {
		return d, apperr.Internal(err)
	}
	for _, sh := range shares {
		d.Shares = append(d.Shares, toShare(sh, r.Title, u.Nickname, u.Phone, s.PublicBaseURL))
	}
	regs, err := s.Q.AdminReportRegenerations(ctx, &id)
	if err != nil {
		return d, apperr.Internal(err)
	}
	for _, g := range regs {
		d.Regenerations = append(d.Regenerations, gen.ContentRef{Id: g.ID.String(), Status: g.Status, CreatedAt: g.CreatedAt})
	}
	if d.Notes, err = s.notes(ctx, "report", id); err != nil {
		return d, err
	}
	if d.Refunded, err = s.refunded(ctx, "report:"+id.String()); err != nil {
		return d, err
	}
	return d, nil
}

func (s *Server) GetReport(ctx context.Context, req gen.GetReportRequestObject) (gen.GetReportResponseObject, error) {
	if _, err := require(ctx, PermContentRead); err != nil {
		return nil, err
	}
	id, err := parseID(req.ReportId, "报告")
	if err != nil {
		return nil, err
	}
	d, err := s.reportDetail(ctx, id)
	if err != nil {
		return nil, err
	}
	s.audit(ctx, "content.view", "report", id.String(), map[string]any{"user": d.Report.UserId})
	return gen.GetReport200JSONResponse(d), nil
}

func (s *Server) RegenerateReport(ctx context.Context, req gen.RegenerateReportRequestObject) (gen.RegenerateReportResponseObject, error) {
	a, err := require(ctx, PermContentWrite)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.ReportId, "报告")
	if err != nil {
		return nil, err
	}
	r, err := s.Q.AdminGetReport(ctx, id)
	if err != nil {
		return nil, notFound(err, "报告")
	}
	if r.Status == "running" {
		return nil, apperr.Conflict("报告还在生成中")
	}
	u, err := s.Q.GetUser(ctx, r.UserID)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	input := json.RawMessage(nil)
	if r.TaskID != nil {
		if t, err := s.Q.AdminGetTask(ctx, *r.TaskID); err == nil && len(t.Input) > 0 {
			input = t.Input
		}
	}
	if input == nil {
		input, _ = json.Marshal(report.Input{Topic: r.Topic, Depth: r.Depth, Focuses: r.Focuses, SourceBriefingItemID: r.SourceBriefingItemID})
	}
	t, err := s.Q.CreateTask(ctx, db.CreateTaskParams{
		UserID: u.ID, Kind: "report", Title: r.Title, Subtitle: report.Subtitle(i18n.Parse(u.UiLanguage), r.Depth, r.Focuses),
		Input: input, PromptVersion: prompts.Version,
	})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if err := s.Q.SetTaskAdmin(ctx, db.SetTaskAdminParams{ID: t.ID, AdminID: &a.ID}); err != nil {
		return nil, apperr.Internal(err)
	}
	nr, err := s.Q.AdminCreateRegeneratedReport(ctx, db.AdminCreateRegeneratedReportParams{
		UserID: u.ID, TaskID: &t.ID, Title: r.Title, Topic: r.Topic, Depth: r.Depth, Focuses: orEmpty(r.Focuses),
		SourceBriefingItemID: r.SourceBriefingItemID, Language: r.Language, RegeneratedFrom: &r.ID,
	})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if err := s.Q.SetTaskLinks(ctx, db.SetTaskLinksParams{ID: t.ID, UserID: u.ID, ReportID: &nr.ID}); err != nil {
		return nil, apperr.Internal(err)
	}
	if err := s.Orch.StartReport(ctx, orch.ReportRun{UserID: u.ID, TaskID: t.ID, ReportID: nr.ID}); err != nil {
		_ = s.Q.SetReportStatus(ctx, db.SetReportStatusParams{ID: nr.ID, UserID: u.ID, Status: "failed"})
		_ = s.Q.UpdateTaskProgress(ctx, db.UpdateTaskProgressParams{ID: t.ID, UserID: u.ID, Status: "failed"})
		return nil, apperr.Unavailable("任务启动失败，请稍后再试").WithCause(err)
	}
	s.audit(ctx, "report.regenerate", "report", id.String(), map[string]any{"newReport": nr.ID, "task": t.ID})
	rid := nr.ID.String()
	return gen.RegenerateReport202JSONResponse{TaskId: t.ID.String(), ReportId: &rid}, nil
}

func (s *Server) HideReport(ctx context.Context, req gen.HideReportRequestObject) (gen.HideReportResponseObject, error) {
	a, err := require(ctx, PermModeration)
	if err != nil {
		return nil, err
	}
	reason, notify, err := hideReason(req.Body)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.ReportId, "报告")
	if err != nil {
		return nil, err
	}
	now := s.now()
	r, err := s.Q.AdminSetReportHidden(ctx, db.AdminSetReportHiddenParams{ID: id, HiddenAt: &now, Reason: reason, HiddenBy: &a.ID})
	if err != nil {
		return nil, notFound(err, "报告")
	}
	revoked, err := s.Q.AdminRevokeSharesFor(ctx, db.AdminRevokeSharesForParams{ReportID: &id})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if notify {
		s.noticeTakedown(ctx, r.UserID, "报告", r.Title, reason, "zeus://report/"+id.String())
	}
	s.audit(ctx, "report.hide", "report", id.String(), map[string]any{"reason": reason, "revokedShares": revoked, "notified": notify})
	d, err := s.reportDetail(ctx, id)
	if err != nil {
		return nil, err
	}
	return gen.HideReport200JSONResponse(d), nil
}

func (s *Server) UnhideReport(ctx context.Context, req gen.UnhideReportRequestObject) (gen.UnhideReportResponseObject, error) {
	if _, err := require(ctx, PermModeration); err != nil {
		return nil, err
	}
	id, err := parseID(req.ReportId, "报告")
	if err != nil {
		return nil, err
	}
	if _, err := s.Q.AdminSetReportHidden(ctx, db.AdminSetReportHiddenParams{ID: id}); err != nil {
		return nil, notFound(err, "报告")
	}
	s.audit(ctx, "report.unhide", "report", id.String(), nil)
	d, err := s.reportDetail(ctx, id)
	if err != nil {
		return nil, err
	}
	return gen.UnhideReport200JSONResponse(d), nil
}

func (s *Server) RefundReport(ctx context.Context, req gen.RefundReportRequestObject) (gen.RefundReportResponseObject, error) {
	id, err := parseID(req.ReportId, "报告")
	if err != nil {
		return nil, err
	}
	r, err := s.Q.AdminGetReport(ctx, id)
	if err != nil {
		return nil, notFound(err, "报告")
	}
	tasks, err := s.Q.AdminListReportTasks(ctx, &id)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	ids := make([]uuid.UUID, 0, len(tasks))
	for _, t := range tasks {
		ids = append(ids, t.ID)
	}
	out, err := s.refund(ctx, r.UserID, "report:"+id.String(), "退还：报告《"+clipRunes(r.Title, 30)+"》", ids)
	if err != nil {
		return nil, err
	}
	return gen.RefundReport200JSONResponse(out), nil
}

// ------------------------------------------------------------------ subscriptions

var weekdayNames = []string{"", "一", "二", "三", "四", "五", "六", "日"}

func scheduleText(sc schedule.Schedule) string {
	switch sc.Freq {
	case "weekly":
		days := make([]string, 0, len(sc.Weekdays))
		for _, d := range sc.Weekdays {
			if d >= 1 && d <= 7 {
				days = append(days, weekdayNames[d])
			}
		}
		return "每周" + strings.Join(days, "、") + " " + sc.Time
	case "monthly":
		return fmt.Sprintf("每月 %d 日 %s", sc.MonthDay, sc.Time)
	default:
		return "每天 " + sc.Time
	}
}

func toSubscription(r db.AdminListSubscriptionsRow) gen.AdminSubscription {
	var sc schedule.Schedule
	_ = json.Unmarshal(r.Schedule, &sc)
	gs := recode[gen.Schedule](sc)
	fv := jsonObject(r.FieldValues)
	out := gen.AdminSubscription{
		Id: r.ID.String(), UserId: r.UserID.String(), Nickname: r.Nickname, PhoneMasked: maskPhone(r.Phone), Name: r.Name,
		TemplateId: idStr(r.TemplateID), TemplateName: r.TemplateName, TemplateVersion: intPtr(r.TemplateVersion),
		Topics: orEmpty(r.Topics), FieldValues: &fv, Schedule: &gs, ScheduleText: scheduleText(sc), Length: r.Length,
		PushEnabled: r.PushEnabled, Status: r.Status, PausedReason: r.PausedReason, NextRunAt: r.NextRunAt, LastOpenedAt: r.LastOpenedAt,
		LastStatus: &r.LastStatus, LastDate: &r.LastDate, Issues: int(r.Issues), Recent: int(r.Recent), RecentReady: int(r.RecentReady),
		RecentRead: int(r.RecentRead), RecentFailed: int(r.RecentFailed), CreatedAt: r.CreatedAt,
	}
	if r.LastStatus == "ready" && r.LastItemCount == 0 {
		empty := "empty"
		out.LastStatus = &empty
	}
	return out
}

func (s *Server) ListSubscriptions(ctx context.Context, req gen.ListSubscriptionsRequestObject) (gen.ListSubscriptionsResponseObject, error) {
	p := req.Params
	lim, off := page(p.Cursor, p.Limit)
	uid, err := optUser(p.UserId)
	if err != nil {
		return nil, err
	}
	tid, err := optUser(p.TemplateId)
	if err != nil {
		return nil, err
	}
	rows, err := s.Q.AdminListSubscriptions(ctx, db.AdminListSubscriptionsParams{
		Limit: int32(lim), Offset: int32(off), UserID: uid, TemplateID: tid, Status: enumPtr(p.Status),
		LastStatus: enumPtr(p.LastStatus), Q: strPtr(p.Q),
	})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	items := make([]gen.AdminSubscription, 0, len(rows))
	for _, r := range rows {
		items = append(items, toSubscription(r))
	}
	return gen.ListSubscriptions200JSONResponse{Items: items, NextCursor: nextCursor(len(rows), lim, off)}, nil
}

func (s *Server) subscriptionRow(ctx context.Context, id uuid.UUID) (gen.AdminSubscription, error) {
	rows, err := s.Q.AdminListSubscriptions(ctx, db.AdminListSubscriptionsParams{ID: &id, Limit: 1})
	if err != nil {
		return gen.AdminSubscription{}, apperr.Internal(err)
	}
	if len(rows) == 0 {
		return gen.AdminSubscription{}, apperr.NotFound("订阅不存在")
	}
	return toSubscription(rows[0]), nil
}

func (s *Server) setSubscriptionPaused(ctx context.Context, rawID string, paused bool) (gen.AdminSubscription, error) {
	if _, err := require(ctx, PermContentWrite); err != nil {
		return gen.AdminSubscription{}, err
	}
	id, err := parseID(rawID, "订阅")
	if err != nil {
		return gen.AdminSubscription{}, err
	}
	sub, err := s.Q.AdminGetSubscription(ctx, id)
	if err != nil {
		return gen.AdminSubscription{}, notFound(err, "订阅")
	}
	var sc schedule.Schedule
	if err := json.Unmarshal(sub.Schedule, &sc); err != nil {
		return gen.AdminSubscription{}, apperr.Internal(err)
	}
	if paused {
		if sub.Status != "active" {
			return gen.AdminSubscription{}, apperr.Conflict("订阅已经是暂停状态")
		}
		reason := "admin"
		if _, err := s.Q.AdminSetSubscriptionStatus(ctx, db.AdminSetSubscriptionStatusParams{ID: id, Status: "paused", PausedReason: &reason}); err != nil {
			return gen.AdminSubscription{}, apperr.Internal(err)
		}
	} else {
		if sub.Status == "active" {
			return gen.AdminSubscription{}, apperr.Conflict("订阅已经在运行")
		}
		next := sc.Next(s.now())
		if _, err := s.Q.AdminSetSubscriptionStatus(ctx, db.AdminSetSubscriptionStatusParams{ID: id, Status: "active", NextRunAt: &next}); err != nil {
			return gen.AdminSubscription{}, apperr.Internal(err)
		}
	}
	if err := s.Orch.UpsertBriefingSchedule(ctx, sub.UserID, id, sc, paused); err != nil {
		return gen.AdminSubscription{}, apperr.Unavailable("更新定时任务失败，请重试").WithCause(err)
	}
	action := "subscription.resume"
	if paused {
		action = "subscription.pause"
	}
	s.audit(ctx, action, "subscription", id.String(), map[string]any{"user": sub.UserID})
	return s.subscriptionRow(ctx, id)
}

func (s *Server) PauseSubscription(ctx context.Context, req gen.PauseSubscriptionRequestObject) (gen.PauseSubscriptionResponseObject, error) {
	out, err := s.setSubscriptionPaused(ctx, req.SubscriptionId, true)
	if err != nil {
		return nil, err
	}
	return gen.PauseSubscription200JSONResponse(out), nil
}

func (s *Server) ResumeSubscription(ctx context.Context, req gen.ResumeSubscriptionRequestObject) (gen.ResumeSubscriptionResponseObject, error) {
	out, err := s.setSubscriptionPaused(ctx, req.SubscriptionId, false)
	if err != nil {
		return nil, err
	}
	return gen.ResumeSubscription200JSONResponse(out), nil
}

// runBriefing starts a free console run of today's issue (in the subscription's zone). Today's
// issue, if any, is kept as a revision first because the run overwrites it in place.
func (s *Server) runBriefing(ctx context.Context, sub db.BriefingSubscription, pushIt bool, from string) (gen.ContentRunResult, error) {
	a, err := require(ctx, PermContentWrite)
	if err != nil {
		return gen.ContentRunResult{}, err
	}
	var sc schedule.Schedule
	if err := json.Unmarshal(sub.Schedule, &sc); err != nil {
		return gen.ContentRunResult{}, apperr.Internal(err)
	}
	date := schedule.LocalDate(s.now(), sc.Location())
	existing, err := s.Q.AdminGetBriefingOnDate(ctx, db.AdminGetBriefingOnDateParams{SubscriptionID: sub.ID, Date: date})
	switch {
	case err == nil:
		if existing.Status == "generating" {
			return gen.ContentRunResult{}, apperr.Conflict("这一期正在生成中")
		}
		if err := s.Q.InsertBriefingRevision(ctx, db.InsertBriefingRevisionParams{ID: existing.ID, CreatedBy: &a.ID}); err != nil {
			return gen.ContentRunResult{}, apperr.Internal(err)
		}
		if err := s.Q.DeleteBriefingHiddenItems(ctx, existing.ID); err != nil {
			return gen.ContentRunResult{}, apperr.Internal(err)
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return gen.ContentRunResult{}, apperr.Internal(err)
	}
	t, err := s.Q.CreateTask(ctx, db.CreateTaskParams{
		UserID: sub.UserID, Kind: "briefing", Title: sub.Name, Subtitle: "重新生成", Input: json.RawMessage(`{}`),
		SubscriptionID: &sub.ID, PromptVersion: prompts.Version,
	})
	if err != nil {
		return gen.ContentRunResult{}, apperr.Internal(err)
	}
	if err := s.Q.SetTaskAdmin(ctx, db.SetTaskAdminParams{ID: t.ID, AdminID: &a.ID}); err != nil {
		return gen.ContentRunResult{}, apperr.Internal(err)
	}
	if err := s.Orch.StartBriefing(ctx, orch.BriefingRun{UserID: sub.UserID, SubscriptionID: sub.ID, TaskID: t.ID, Push: pushIt}); err != nil {
		_ = s.Q.UpdateTaskProgress(ctx, db.UpdateTaskProgressParams{ID: t.ID, UserID: sub.UserID, Status: "failed"})
		return gen.ContentRunResult{}, apperr.Unavailable("任务启动失败，请稍后再试").WithCause(err)
	}
	s.audit(ctx, "briefing.regenerate", "subscription", sub.ID.String(), map[string]any{"task": t.ID, "push": pushIt, "from": from})
	return gen.ContentRunResult{TaskId: t.ID.String()}, nil
}

func wantPush(b *gen.BriefingRunRequest) bool { return b != nil && b.Push != nil && *b.Push }

func (s *Server) RunSubscription(ctx context.Context, req gen.RunSubscriptionRequestObject) (gen.RunSubscriptionResponseObject, error) {
	id, err := parseID(req.SubscriptionId, "订阅")
	if err != nil {
		return nil, err
	}
	sub, err := s.Q.AdminGetSubscription(ctx, id)
	if err != nil {
		return nil, notFound(err, "订阅")
	}
	out, err := s.runBriefing(ctx, sub, wantPush(req.Body), "subscription")
	if err != nil {
		return nil, err
	}
	return gen.RunSubscription202JSONResponse(out), nil
}

// ------------------------------------------------------------------ briefings

func toBriefingRow(r db.AdminListBriefingsRow) gen.AdminBriefingRow {
	return gen.AdminBriefingRow{
		Id: r.ID.String(), UserId: r.UserID.String(), Nickname: r.Nickname, PhoneMasked: maskPhone(r.Phone),
		SubscriptionId: r.SubscriptionID.String(), TemplateId: idStr(r.TemplateID), TemplateName: r.TemplateName, Title: r.Title,
		Date: openapi_types.Date{Time: r.Date}, Status: r.Status, Hidden: r.HiddenAt != nil, Headline: r.Headline,
		ItemCount: int(r.ItemCount), SourceCount: int(r.SourceCount), ReadAt: r.ReadAt,
		DurationSeconds: durationSeconds(r.StartedAt, r.FinishedAt), CostCny: cny(r.CostMicros), Useful: int(r.Useful), Meh: int(r.Meh),
		AdminRun: r.AdminID != nil, GeneratedAt: r.GeneratedAt, CreatedAt: r.CreatedAt,
	}
}

func datePtr(d *openapi_types.Date) *time.Time {
	if d == nil {
		return nil
	}
	return &d.Time
}

func (s *Server) ListBriefings(ctx context.Context, req gen.ListBriefingsRequestObject) (gen.ListBriefingsResponseObject, error) {
	p := req.Params
	lim, off := page(p.Cursor, p.Limit)
	uid, err := optUser(p.UserId)
	if err != nil {
		return nil, err
	}
	sid, err := optUser(p.SubscriptionId)
	if err != nil {
		return nil, err
	}
	tid, err := optUser(p.TemplateId)
	if err != nil {
		return nil, err
	}
	rows, err := s.Q.AdminListBriefings(ctx, db.AdminListBriefingsParams{
		Limit: int32(lim), Offset: int32(off), UserID: uid, SubscriptionID: sid, TemplateID: tid, Q: strPtr(p.Q),
		Status: enumPtr(p.Status), DateFrom: datePtr(p.From), DateTo: datePtr(p.To), Read: p.Read, Disliked: p.Disliked != nil && *p.Disliked,
	})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	items := make([]gen.AdminBriefingRow, 0, len(rows))
	for _, r := range rows {
		items = append(items, toBriefingRow(r))
	}
	return gen.ListBriefings200JSONResponse{Items: items, NextCursor: nextCursor(len(rows), lim, off)}, nil
}

type itemFeedback struct {
	useful, meh int
	comments    []string
}

// toSections renders stored sections for the console; private sections are masked unless revealed.
func toSections(raw json.RawMessage, fb map[string]*itemFeedback, reveal bool) []gen.AdminBriefingSection {
	var in []briefing.Section
	_ = json.Unmarshal(raw, &in)
	out := make([]gen.AdminBriefingSection, 0, len(in))
	for _, sec := range in {
		n := len(sec.Items)
		gs := gen.AdminBriefingSection{Key: sec.Key, Title: sec.Title, Private: sec.Private, ItemCount: &n, Items: []gen.AdminBriefingItem{}}
		if sec.Private && !reveal {
			gs.Masked = true
			out = append(out, gs)
			continue
		}
		for _, it := range sec.Items {
			gi := gen.AdminBriefingItem{Id: it.ID, Title: it.Title, Summary: it.Summary, Sources: convertSources(it.Sources), Comments: []string{}}
			if f := fb[it.ID]; f != nil {
				gi.Useful, gi.Meh, gi.Comments = f.useful, f.meh, orEmpty(f.comments)
			}
			gs.Items = append(gs.Items, gi)
		}
		out = append(out, gs)
	}
	return out
}

func (s *Server) briefingDetail(ctx context.Context, id uuid.UUID, reveal bool) (gen.AdminBriefingDetail, error) {
	b, err := s.Q.AdminGetBriefing(ctx, id)
	if err != nil {
		return gen.AdminBriefingDetail{}, notFound(err, "简报")
	}
	rows, err := s.Q.AdminListBriefings(ctx, db.AdminListBriefingsParams{ID: &id, Limit: 1})
	if err != nil {
		return gen.AdminBriefingDetail{}, apperr.Internal(err)
	}
	if len(rows) == 0 {
		return gen.AdminBriefingDetail{}, apperr.NotFound("简报不存在")
	}
	row := toBriefingRow(rows[0])
	fbRows, err := s.Q.AdminListBriefingFeedback(ctx, id)
	if err != nil {
		return gen.AdminBriefingDetail{}, apperr.Internal(err)
	}
	fb := map[string]*itemFeedback{}
	overall := []string{}
	for _, f := range fbRows {
		if f.ItemID == "" {
			overall = append(overall, f.Rating)
			continue
		}
		e := fb[f.ItemID]
		if e == nil {
			e = &itemFeedback{}
			fb[f.ItemID] = e
		}
		if f.Rating == "useful" {
			e.useful++
		} else {
			e.meh++
		}
		if c := strings.TrimSpace(f.Comment); c != "" {
			e.comments = append(e.comments, c)
		}
	}
	d := gen.AdminBriefingDetail{
		Briefing: row, Sections: toSections(b.Sections, fb, reveal), PrivateRevealed: reveal, HiddenReason: b.HiddenReason,
		HiddenAt: b.HiddenAt, HiddenItems: []gen.HiddenBriefingItem{}, Revisions: []gen.BriefingRevision{}, OverallFeedback: overall,
		Shares: []gen.AdminShare{},
	}
	if sub, err := s.subscriptionRow(ctx, b.SubscriptionID); err == nil {
		d.Subscription = &sub
	}
	hidden, err := s.Q.ListBriefingHiddenItems(ctx, id)
	if err != nil {
		return d, apperr.Internal(err)
	}
	for _, h := range hidden {
		var it briefing.Item
		_ = json.Unmarshal(h.Item, &it)
		d.HiddenItems = append(d.HiddenItems, gen.HiddenBriefingItem{ItemId: h.ItemID, SectionKey: h.SectionKey, Title: it.Title, Reason: h.Reason, CreatedAt: h.CreatedAt})
	}
	revs, err := s.Q.ListBriefingRevisions(ctx, id)
	if err != nil {
		return d, apperr.Internal(err)
	}
	for _, r := range revs {
		d.Revisions = append(d.Revisions, gen.BriefingRevision{
			Id: r.ID, Status: r.Status, Headline: r.Headline, ItemCount: int(r.ItemCount), SourceCount: int(r.SourceCount),
			Sections: toSections(r.Sections, nil, reveal), GeneratedAt: r.GeneratedAt, CreatedAt: r.CreatedAt,
		})
	}
	if b.TaskID != nil {
		if t, err := s.Q.AdminGetTask(ctx, *b.TaskID); err == nil {
			td, err := s.taskDetail(ctx, t)
			if err != nil {
				return d, err
			}
			td.Task.Nickname, td.Task.PhoneMasked = row.Nickname, row.PhoneMasked
			d.Task = &td
		}
	}
	shares, err := s.Q.AdminListSharesFor(ctx, db.AdminListSharesForParams{BriefingID: &id})
	if err != nil {
		return d, apperr.Internal(err)
	}
	phone := row.PhoneMasked
	for _, sh := range shares {
		g := toShare(sh, b.Title, row.Nickname, nil, s.PublicBaseURL)
		g.PhoneMasked = phone
		d.Shares = append(d.Shares, g)
	}
	if d.Notes, err = s.notes(ctx, "briefing", id); err != nil {
		return d, err
	}
	if d.Refunded, err = s.refunded(ctx, "briefing:"+id.String()); err != nil {
		return d, err
	}
	return d, nil
}

func (s *Server) GetBriefing(ctx context.Context, req gen.GetBriefingRequestObject) (gen.GetBriefingResponseObject, error) {
	if _, err := require(ctx, PermContentRead); err != nil {
		return nil, err
	}
	id, err := parseID(req.BriefingId, "简报")
	if err != nil {
		return nil, err
	}
	reason := ""
	if req.Params.RevealReason != nil {
		reason = strings.TrimSpace(*req.Params.RevealReason)
		if utf8.RuneCountInString(reason) > 200 {
			return nil, apperr.Invalid("原因不超过 200 字")
		}
	}
	d, err := s.briefingDetail(ctx, id, reason != "")
	if err != nil {
		return nil, err
	}
	s.audit(ctx, "content.view", "briefing", id.String(), map[string]any{"user": d.Briefing.UserId})
	if reason != "" {
		s.audit(ctx, "content.reveal_private", "briefing", id.String(), map[string]any{"user": d.Briefing.UserId, "reason": reason})
	}
	return gen.GetBriefing200JSONResponse(d), nil
}

func (s *Server) RegenerateBriefing(ctx context.Context, req gen.RegenerateBriefingRequestObject) (gen.RegenerateBriefingResponseObject, error) {
	id, err := parseID(req.BriefingId, "简报")
	if err != nil {
		return nil, err
	}
	b, err := s.Q.AdminGetBriefing(ctx, id)
	if err != nil {
		return nil, notFound(err, "简报")
	}
	sub, err := s.Q.AdminGetSubscription(ctx, b.SubscriptionID)
	if err != nil {
		return nil, notFound(err, "订阅")
	}
	var sc schedule.Schedule
	_ = json.Unmarshal(sub.Schedule, &sc)
	if !schedule.LocalDate(s.now(), sc.Location()).Equal(b.Date) {
		return nil, apperr.Invalid("只能重新生成订阅时区里今天这一期；更早的期次可以在订阅上“立即生成今天这期”")
	}
	out, err := s.runBriefing(ctx, sub, wantPush(req.Body), "briefing:"+id.String())
	if err != nil {
		return nil, err
	}
	return gen.RegenerateBriefing202JSONResponse(out), nil
}

func (s *Server) HideBriefing(ctx context.Context, req gen.HideBriefingRequestObject) (gen.HideBriefingResponseObject, error) {
	a, err := require(ctx, PermModeration)
	if err != nil {
		return nil, err
	}
	reason, notify, err := hideReason(req.Body)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.BriefingId, "简报")
	if err != nil {
		return nil, err
	}
	now := s.now()
	b, err := s.Q.AdminSetBriefingHidden(ctx, db.AdminSetBriefingHiddenParams{ID: id, HiddenAt: &now, Reason: reason, HiddenBy: &a.ID})
	if err != nil {
		return nil, notFound(err, "简报")
	}
	revoked, err := s.Q.AdminRevokeSharesFor(ctx, db.AdminRevokeSharesForParams{BriefingID: &id})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if notify {
		s.noticeTakedown(ctx, b.UserID, "简报", b.Title+" "+b.Date.Format(time.DateOnly), reason, "zeus://briefing/"+id.String())
	}
	s.audit(ctx, "briefing.hide", "briefing", id.String(), map[string]any{"reason": reason, "revokedShares": revoked, "notified": notify})
	d, err := s.briefingDetail(ctx, id, false)
	if err != nil {
		return nil, err
	}
	return gen.HideBriefing200JSONResponse(d), nil
}

func (s *Server) UnhideBriefing(ctx context.Context, req gen.UnhideBriefingRequestObject) (gen.UnhideBriefingResponseObject, error) {
	if _, err := require(ctx, PermModeration); err != nil {
		return nil, err
	}
	id, err := parseID(req.BriefingId, "简报")
	if err != nil {
		return nil, err
	}
	if _, err := s.Q.AdminSetBriefingHidden(ctx, db.AdminSetBriefingHiddenParams{ID: id}); err != nil {
		return nil, notFound(err, "简报")
	}
	s.audit(ctx, "briefing.unhide", "briefing", id.String(), nil)
	d, err := s.briefingDetail(ctx, id, false)
	if err != nil {
		return nil, err
	}
	return gen.UnhideBriefing200JSONResponse(d), nil
}

func (s *Server) RefundBriefing(ctx context.Context, req gen.RefundBriefingRequestObject) (gen.RefundBriefingResponseObject, error) {
	id, err := parseID(req.BriefingId, "简报")
	if err != nil {
		return nil, err
	}
	b, err := s.Q.AdminGetBriefing(ctx, id)
	if err != nil {
		return nil, notFound(err, "简报")
	}
	ids := []uuid.UUID{}
	if b.TaskID != nil {
		ids = append(ids, *b.TaskID)
	}
	out, err := s.refund(ctx, b.UserID, "briefing:"+id.String(), "退还：简报《"+clipRunes(b.Title, 30)+"》"+b.Date.Format(time.DateOnly), ids)
	if err != nil {
		return nil, err
	}
	return gen.RefundBriefing200JSONResponse(out), nil
}

func (s *Server) HideBriefingItem(ctx context.Context, req gen.HideBriefingItemRequestObject) (gen.HideBriefingItemResponseObject, error) {
	a, err := require(ctx, PermModeration)
	if err != nil {
		return nil, err
	}
	reason, _, err := hideReason(req.Body)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.BriefingId, "简报")
	if err != nil {
		return nil, err
	}
	b, err := s.Q.AdminGetBriefing(ctx, id)
	if err != nil {
		return nil, notFound(err, "简报")
	}
	var secs []json.RawMessage
	if err := json.Unmarshal(b.Sections, &secs); err != nil {
		return nil, apperr.Internal(err)
	}
	found := false
	for si, raw := range secs {
		var sec map[string]json.RawMessage
		if err := json.Unmarshal(raw, &sec); err != nil {
			return nil, apperr.Internal(err)
		}
		var items []json.RawMessage
		_ = json.Unmarshal(sec["items"], &items)
		for ii, it := range items {
			var probe struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(it, &probe)
			if probe.ID != req.ItemId {
				continue
			}
			var key string
			_ = json.Unmarshal(sec["key"], &key)
			if err := s.Q.InsertBriefingHiddenItem(ctx, db.InsertBriefingHiddenItemParams{
				BriefingID: id, ItemID: req.ItemId, SectionKey: key, Position: int32(ii), Item: it, Reason: reason, HiddenBy: &a.ID,
			}); err != nil {
				return nil, apperr.Internal(err)
			}
			items = append(items[:ii], items[ii+1:]...)
			sec["items"], _ = json.Marshal(items)
			secs[si], _ = json.Marshal(sec)
			found = true
			break
		}
		if found {
			break
		}
	}
	if !found {
		return nil, apperr.NotFound("条目不存在")
	}
	out, _ := json.Marshal(secs)
	if err := s.Q.AdminSetBriefingSections(ctx, db.AdminSetBriefingSectionsParams{ID: id, Sections: out, ItemCount: max(b.ItemCount-1, 0)}); err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "briefing.hide_item", "briefing", id.String(), map[string]any{"item": req.ItemId, "reason": reason})
	d, err := s.briefingDetail(ctx, id, false)
	if err != nil {
		return nil, err
	}
	return gen.HideBriefingItem200JSONResponse(d), nil
}

func (s *Server) UnhideBriefingItem(ctx context.Context, req gen.UnhideBriefingItemRequestObject) (gen.UnhideBriefingItemResponseObject, error) {
	if _, err := require(ctx, PermModeration); err != nil {
		return nil, err
	}
	id, err := parseID(req.BriefingId, "简报")
	if err != nil {
		return nil, err
	}
	b, err := s.Q.AdminGetBriefing(ctx, id)
	if err != nil {
		return nil, notFound(err, "简报")
	}
	h, err := s.Q.DeleteBriefingHiddenItem(ctx, db.DeleteBriefingHiddenItemParams{BriefingID: id, ItemID: req.ItemId})
	if err != nil {
		return nil, notFound(err, "隐藏的条目")
	}
	var secs []map[string]json.RawMessage
	if err := json.Unmarshal(b.Sections, &secs); err != nil {
		return nil, apperr.Internal(err)
	}
	placed := false
	for _, sec := range secs {
		var key string
		_ = json.Unmarshal(sec["key"], &key)
		if key != h.SectionKey {
			continue
		}
		var items []json.RawMessage
		_ = json.Unmarshal(sec["items"], &items)
		pos := min(max(int(h.Position), 0), len(items))
		items = append(items[:pos], append([]json.RawMessage{h.Item}, items[pos:]...)...)
		sec["items"], _ = json.Marshal(items)
		placed = true
		break
	}
	if !placed {
		return nil, apperr.Conflict("原区块已不存在，无法放回")
	}
	out, _ := json.Marshal(secs)
	if err := s.Q.AdminSetBriefingSections(ctx, db.AdminSetBriefingSectionsParams{ID: id, Sections: out, ItemCount: b.ItemCount + 1}); err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "briefing.unhide_item", "briefing", id.String(), map[string]any{"item": req.ItemId})
	d, err := s.briefingDetail(ctx, id, false)
	if err != nil {
		return nil, err
	}
	return gen.UnhideBriefingItem200JSONResponse(d), nil
}

// ------------------------------------------------------------------ notes & issues

func (s *Server) ListContentNotes(ctx context.Context, req gen.ListContentNotesRequestObject) (gen.ListContentNotesResponseObject, error) {
	id, err := parseID(req.Params.TargetId, "内容")
	if err != nil {
		return nil, err
	}
	out, err := s.notes(ctx, string(req.Params.TargetType), id)
	if err != nil {
		return nil, err
	}
	return gen.ListContentNotes200JSONResponse(out), nil
}

func (s *Server) CreateContentNote(ctx context.Context, req gen.CreateContentNoteRequestObject) (gen.CreateContentNoteResponseObject, error) {
	a, err := require(ctx, PermContentRead)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, apperr.Invalid("请填写备注")
	}
	body := strings.TrimSpace(req.Body.Body)
	if body == "" || utf8.RuneCountInString(body) > 2000 {
		return nil, apperr.Invalid("备注不能为空且不超过 2000 字")
	}
	typ := string(req.Body.TargetType)
	if typ != "report" && typ != "briefing" && typ != "subscription" {
		return nil, apperr.Invalid("targetType 不正确")
	}
	id, err := parseID(req.Body.TargetId, "内容")
	if err != nil {
		return nil, err
	}
	n, err := s.Q.CreateContentNote(ctx, db.CreateContentNoteParams{TargetType: typ, TargetID: id, AdminID: &a.ID, Body: body})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "content.note", typ, id.String(), map[string]any{"length": utf8.RuneCountInString(body)})
	return gen.CreateContentNote201JSONResponse{
		Id: n.ID, TargetType: req.Body.TargetType, TargetId: id.String(), Body: n.Body, AdminName: a.Name, CreatedAt: n.CreatedAt,
	}, nil
}

func (s *Server) ListContentIssues(ctx context.Context, req gen.ListContentIssuesRequestObject) (gen.ListContentIssuesResponseObject, error) {
	p := req.Params
	lim, off := page(p.Cursor, p.Limit)
	days := 14
	if p.Days != nil && *p.Days >= 1 && *p.Days <= 90 {
		days = *p.Days
	}
	since := s.now().AddDate(0, 0, -days)
	rows, err := s.Q.AdminContentIssues(ctx, db.AdminContentIssuesParams{
		Limit: int32(lim), Offset: int32(off), Kind: enumPtr(p.Kind), TargetType: enumPtr(p.TargetType),
		Resolved: p.Resolved != nil && *p.Resolved, Since: since, CostlyMicros: costlyReportMicros,
	})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	counts, err := s.Q.AdminContentIssueCounts(ctx, db.AdminContentIssueCountsParams{Since: since, CostlyMicros: costlyReportMicros})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	open := map[string]int{}
	for _, c := range counts {
		open[c.Kind] = int(c.Open)
	}
	items := make([]gen.ContentIssue, 0, len(rows))
	for _, r := range rows {
		detail := r.Detail
		if r.Kind == "costly" {
			var micros int64
			_, _ = fmt.Sscan(r.Detail, &micros)
			detail = fmt.Sprintf("%.2f", cny(micros))
		}
		it := gen.ContentIssue{
			TargetType: gen.ContentIssueTargetType(r.TargetType), TargetId: r.TargetID.String(), Kind: gen.ContentIssueKind(r.Kind),
			Title: r.Title, UserId: r.UserID.String(), Nickname: r.Nickname, PhoneMasked: maskPhone(r.Phone), Detail: detail,
			CreatedAt: r.CreatedAt, ResolvedAt: r.ResolvedAt, ResolutionNote: r.ResolutionNote,
		}
		if r.ResolvedByName != "" {
			it.ResolvedBy = &r.ResolvedByName
		}
		items = append(items, it)
	}
	return gen.ListContentIssues200JSONResponse{Items: items, OpenCounts: open, NextCursor: nextCursor(len(rows), lim, off)}, nil
}

func issueTarget(b *gen.ContentIssueResolve) (string, uuid.UUID, string, error) {
	if b == nil {
		return "", uuid.Nil, "", apperr.Invalid("参数不正确")
	}
	typ, kind := string(b.TargetType), string(b.Kind)
	if typ != "report" && typ != "briefing" {
		return "", uuid.Nil, "", apperr.Invalid("targetType 不正确")
	}
	switch kind {
	case "failed", "empty", "few_sources", "unsatisfied", "disliked", "costly":
	default:
		return "", uuid.Nil, "", apperr.Invalid("kind 不正确")
	}
	id, err := parseID(b.TargetId, "内容")
	return typ, id, kind, err
}

func (s *Server) ResolveContentIssue(ctx context.Context, req gen.ResolveContentIssueRequestObject) (gen.ResolveContentIssueResponseObject, error) {
	a, err := require(ctx, PermContentWrite)
	if err != nil {
		return nil, err
	}
	typ, id, kind, err := issueTarget(req.Body)
	if err != nil {
		return nil, err
	}
	note := strings.TrimSpace(deref(req.Body.Note))
	if utf8.RuneCountInString(note) > 500 {
		return nil, apperr.Invalid("处理说明不超过 500 字")
	}
	if err := s.Q.ResolveContentIssue(ctx, db.ResolveContentIssueParams{TargetType: typ, TargetID: id, Kind: kind, Note: note, ResolvedBy: &a.ID}); err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "content.issue_resolve", typ, id.String(), map[string]any{"kind": kind, "note": note})
	return gen.ResolveContentIssue204Response{}, nil
}

func (s *Server) ReopenContentIssue(ctx context.Context, req gen.ReopenContentIssueRequestObject) (gen.ReopenContentIssueResponseObject, error) {
	if _, err := require(ctx, PermContentWrite); err != nil {
		return nil, err
	}
	typ, id, kind, err := issueTarget(req.Body)
	if err != nil {
		return nil, err
	}
	if _, err := s.Q.ReopenContentIssue(ctx, db.ReopenContentIssueParams{TargetType: typ, TargetID: id, Kind: kind}); err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "content.issue_reopen", typ, id.String(), map[string]any{"kind": kind})
	return gen.ReopenContentIssue204Response{}, nil
}
