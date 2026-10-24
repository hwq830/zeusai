package adminapi

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/feedback"
	"github.com/hwq830/zeusai/server/internal/push"
)

func appVersion(client json.RawMessage) *string {
	var c struct {
		AppVersion string `json:"appVersion"`
		Build      string `json:"build"`
	}
	if json.Unmarshal(client, &c) != nil || c.AppVersion == "" {
		return nil
	}
	v := c.AppVersion
	if c.Build != "" {
		v += " (" + c.Build + ")"
	}
	return &v
}

func feedbackSummary(f db.Feedback, nickname string, phone *string, attachments, replies int32) gen.FeedbackSummary {
	return gen.FeedbackSummary{
		Id: f.ID.String(), UserId: f.UserID.String(), Nickname: nickname, PhoneMasked: maskPhone(phone),
		Type: gen.FeedbackType(f.Type), Content: f.Content, Status: gen.FeedbackStatus(f.Status), Flagged: f.FlaggedReason != "",
		AppVersion: appVersion(f.Client), AttachmentCount: int(attachments), ReplyCount: int(replies),
		CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt,
	}
}

func (s *Server) ListFeedback(ctx context.Context, req gen.ListFeedbackRequestObject) (gen.ListFeedbackResponseObject, error) {
	p := req.Params
	lim, off := page(p.Cursor, p.Limit)
	params := db.AdminListFeedbackParams{Limit: int32(lim), Offset: int32(off), Q: strPtr(p.Q)}
	if p.Status != nil {
		v := string(*p.Status)
		params.Status = &v
	}
	if p.Type != nil {
		v := string(*p.Type)
		params.Type = &v
	}
	if p.UserId != nil {
		id, err := parseID(*p.UserId, "用户")
		if err != nil {
			return nil, err
		}
		params.UserID = &id
	}
	rows, err := s.Q.AdminListFeedback(ctx, params)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	items := make([]gen.FeedbackSummary, 0, len(rows))
	for _, r := range rows {
		f := db.Feedback{
			ID: r.ID, UserID: r.UserID, Type: r.Type, Content: r.Content, Client: r.Client, Status: r.Status,
			FlaggedReason: r.FlaggedReason, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		}
		items = append(items, feedbackSummary(f, r.Nickname, r.Phone, r.AttachmentCount, r.ReplyCount))
	}
	return gen.ListFeedback200JSONResponse{Items: items, NextCursor: nextCursor(len(rows), lim, off)}, nil
}

// feedbackDetail loads everything the console's detail page shows.
func (s *Server) feedbackDetail(ctx context.Context, id uuid.UUID) (gen.FeedbackDetail, error) {
	r, err := s.Q.AdminGetFeedback(ctx, id)
	if err != nil {
		return gen.FeedbackDetail{}, notFound(err, "反馈")
	}
	atts, err := feedback.Attachments(ctx, s.Q, s.Blob, id)
	if err != nil {
		return gen.FeedbackDetail{}, err
	}
	replies, err := s.Q.ListFeedbackReplies(ctx, id)
	if err != nil {
		return gen.FeedbackDetail{}, apperr.Internal(err)
	}
	f := db.Feedback{
		ID: r.ID, UserID: r.UserID, Type: r.Type, Content: r.Content, Client: r.Client, Status: r.Status,
		FlaggedReason: r.FlaggedReason, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	sum := feedbackSummary(f, r.Nickname, r.Phone, int32(len(atts)), int32(len(replies)))
	d := gen.FeedbackDetail{
		Id: sum.Id, UserId: sum.UserId, Nickname: sum.Nickname, PhoneMasked: sum.PhoneMasked, Type: sum.Type,
		Content: sum.Content, Status: sum.Status, Flagged: sum.Flagged, AppVersion: sum.AppVersion,
		AttachmentCount: sum.AttachmentCount, ReplyCount: sum.ReplyCount, CreatedAt: sum.CreatedAt, UpdatedAt: sum.UpdatedAt,
		Contact: r.Contact, Client: jsonObject(r.Client), ReportId: idStr(r.ReportID), TaskId: idStr(r.TaskID),
		FlaggedReason: r.FlaggedReason, AdminNote: r.AdminNote, HandledBy: r.HandlerEmail,
		LastReplyAt: r.LastReplyAt, UserReadAt: r.UserReadAt,
	}
	if d.Client == nil {
		d.Client = map[string]any{}
	}
	type attachment = struct {
		Id  string `json:"id"`
		Url string `json:"url"`
	}
	type reply = struct {
		AdminName *string   `json:"adminName,omitempty"`
		Content   string    `json:"content"`
		CreatedAt time.Time `json:"createdAt"`
		Id        string    `json:"id"`
	}
	d.Attachments = make([]attachment, 0, len(atts))
	for _, a := range atts {
		d.Attachments = append(d.Attachments, attachment{Id: a.ID, Url: a.URL})
	}
	d.Replies = make([]reply, 0, len(replies))
	for _, rp := range replies {
		name := deref(rp.AdminName)
		if name == "" {
			name = deref(rp.AdminEmail)
		}
		d.Replies = append(d.Replies, reply{AdminName: strPtr(&name), Content: rp.Content, CreatedAt: rp.CreatedAt, Id: rp.ID.String()})
	}
	return d, nil
}

func (s *Server) GetFeedback(ctx context.Context, req gen.GetFeedbackRequestObject) (gen.GetFeedbackResponseObject, error) {
	id, err := parseID(req.FeedbackId, "反馈")
	if err != nil {
		return nil, err
	}
	d, err := s.feedbackDetail(ctx, id)
	if err != nil {
		return nil, err
	}
	return gen.GetFeedback200JSONResponse(d), nil
}

func (s *Server) UpdateFeedback(ctx context.Context, req gen.UpdateFeedbackRequestObject) (gen.UpdateFeedbackResponseObject, error) {
	a, err := require(ctx, PermFeedback)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.FeedbackId, "反馈")
	if err != nil {
		return nil, err
	}
	b := req.Body
	params := db.AdminUpdateFeedbackParams{ID: id, HandledBy: &a.ID}
	detail := map[string]any{}
	if b.Status != nil {
		v := string(*b.Status)
		params.Status = &v
		detail["status"] = v
	}
	if b.AdminNote != nil {
		if utf8.RuneCountInString(*b.AdminNote) > 2000 {
			return nil, apperr.Invalid("备注不能超过 2000 字")
		}
		params.AdminNote = b.AdminNote
		detail["adminNote"] = true
	}
	if len(detail) == 0 {
		return nil, apperr.Invalid("没有要修改的内容")
	}
	if _, err := s.Q.AdminUpdateFeedback(ctx, params); err != nil {
		return nil, notFound(err, "反馈")
	}
	s.audit(ctx, "feedback.update", "feedback", id.String(), detail)
	d, err := s.feedbackDetail(ctx, id)
	if err != nil {
		return nil, err
	}
	return gen.UpdateFeedback200JSONResponse(d), nil
}

func (s *Server) ReplyFeedback(ctx context.Context, req gen.ReplyFeedbackRequestObject) (gen.ReplyFeedbackResponseObject, error) {
	a, err := require(ctx, PermFeedback)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.FeedbackId, "反馈")
	if err != nil {
		return nil, err
	}
	content := strings.TrimSpace(req.Body.Content)
	if content == "" || utf8.RuneCountInString(content) > 1000 {
		return nil, apperr.Invalid("回复内容需在 1–1000 字之间")
	}
	f, err := s.Q.AdminGetFeedback(ctx, id)
	if err != nil {
		return nil, notFound(err, "反馈")
	}
	if _, err := s.Q.AdminCreateFeedbackReply(ctx, db.AdminCreateFeedbackReplyParams{FeedbackID: id, AdminID: &a.ID, Content: content}); err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "feedback.reply", "feedback", id.String(), map[string]any{"length": utf8.RuneCountInString(content)})
	if s.Push != nil {
		// The reply is saved either way; a failed push only means the user sees it next time they open the app.
		if err := s.Push.Notify(ctx, f.UserID, push.Notification{
			Category: push.CategoryService, Title: "你的反馈有新回复", Body: clipRunes(content, 60), DeepLink: "zeus://feedback/" + id.String(),
		}); err != nil {
			s.Log.Warn("feedback reply push failed", "feedback", id, "err", err)
		}
	}
	d, err := s.feedbackDetail(ctx, id)
	if err != nil {
		return nil, err
	}
	return gen.ReplyFeedback201JSONResponse(d), nil
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func (s *Server) ListReportFeedback(ctx context.Context, req gen.ListReportFeedbackRequestObject) (gen.ListReportFeedbackResponseObject, error) {
	p := req.Params
	lim, off := page(p.Cursor, p.Limit)
	params := db.AdminListReportFeedbackParams{Limit: int32(lim), Offset: int32(off)}
	if p.Rating != nil {
		v := string(*p.Rating)
		params.Rating = &v
	}
	if p.Reason != nil {
		v := string(*p.Reason)
		params.Reason = &v
	}
	rows, err := s.Q.AdminListReportFeedback(ctx, params)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	items := make([]gen.ReportFeedbackItem, 0, len(rows))
	for _, r := range rows {
		reasons := make([]gen.ReportFeedbackReason, 0, len(r.Reasons))
		for _, x := range r.Reasons {
			reasons = append(reasons, gen.ReportFeedbackReason(x))
		}
		items = append(items, gen.ReportFeedbackItem{
			ReportId: r.ReportID.String(), ReportTitle: r.ReportTitle, ReportDepth: r.ReportDepth, TaskId: idStr(r.TaskID),
			UserId: r.UserID.String(), Nickname: r.Nickname, PhoneMasked: maskPhone(r.Phone),
			Rating: gen.ReportFeedbackItemRating(r.Rating), Reasons: reasons, Comment: r.Comment,
			ReportCreatedAt: r.ReportCreatedAt, UpdatedAt: r.UpdatedAt,
		})
	}
	return gen.ListReportFeedback200JSONResponse{Items: items, NextCursor: nextCursor(len(rows), lim, off)}, nil
}

// satisfaction fills the dashboard's feedback block. Windows count reports and briefings by
// creation date, so a late rating still lands on the day the report was made.
func (s *Server) satisfaction(ctx context.Context) (gen.Satisfaction, error) {
	var out gen.Satisfaction
	week, month30 := s.dayStart(6), s.dayStart(29)
	for _, w := range []struct {
		since time.Time
		dst   *gen.ReportRatingCounts
	}{{week, &out.Reports7d}, {month30, &out.Reports30d}} {
		r, err := s.Q.ReportSatisfactionSince(ctx, w.since)
		if err != nil {
			return out, apperr.Internal(err)
		}
		*w.dst = gen.ReportRatingCounts{Completed: int(r.Completed), Satisfied: int(r.Satisfied), Unsatisfied: int(r.Unsatisfied)}
	}
	b, err := s.Q.BriefingUsefulnessSince(ctx, month30)
	if err != nil {
		return out, apperr.Internal(err)
	}
	out.Briefings30d.Useful, out.Briefings30d.Meh = int(b.Useful), int(b.Meh)
	statuses, err := s.Q.CountFeedbackByStatus(ctx)
	if err != nil {
		return out, apperr.Internal(err)
	}
	for _, st := range statuses {
		if st.Status == "new" || st.Status == "in_progress" {
			out.OpenFeedback += int(st.N)
		}
	}
	reasons, err := s.Q.UnsatisfiedReasonsSince(ctx, month30)
	if err != nil {
		return out, apperr.Internal(err)
	}
	type reasonCount = struct {
		Count  int                      `json:"count"`
		Reason gen.ReportFeedbackReason `json:"reason"`
	}
	out.UnsatisfiedReasons30d = make([]reasonCount, 0, len(reasons))
	for _, r := range reasons {
		out.UnsatisfiedReasons30d = append(out.UnsatisfiedReasons30d, reasonCount{Count: int(r.N), Reason: gen.ReportFeedbackReason(r.Reason)})
	}
	return out, nil
}
