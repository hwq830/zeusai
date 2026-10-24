package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/notify"
	"github.com/hwq830/zeusai/server/internal/orch"
	"github.com/hwq830/zeusai/server/internal/push"
)

// Operator campaigns: operators write and submit, an owner approves, the orchestrator sends
// (workflows.CampaignWorkflow) through the message center with each user's preferences and caps.

func (s *Server) campaign(ctx context.Context, c db.Campaign) (gen.Campaign, error) {
	out := gen.Campaign{
		Id: c.ID.String(), Name: c.Name, Category: gen.CampaignCategory(c.Category), Title: c.Title, Body: c.Body,
		ImageUrl: c.ImageUrl, Deeplink: c.Deeplink, SendAt: c.SendAt, Status: gen.CampaignStatus(c.Status),
		AudienceEstimate: int(c.AudienceEstimate), RejectReason: c.RejectReason,
		SubmittedAt: c.SubmittedAt, ApprovedAt: c.ApprovedAt, StartedAt: c.StartedAt, FinishedAt: c.FinishedAt,
		CancelledAt: c.CancelledAt, RecalledAt: c.RecalledAt, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
	a, err := notify.ParseAudience(c.Audience)
	if err != nil {
		return out, apperr.Internal(err)
	}
	out.Audience = recode[gen.CampaignAudience](a)
	out.CreatedBy = s.adminEmail(ctx, c.CreatedBy)
	out.ApprovedBy = s.adminEmail(ctx, c.ApprovedBy)
	st, err := s.Q.CampaignStats(ctx, &c.ID)
	if err != nil {
		return out, apperr.Internal(err)
	}
	out.Stats = gen.CampaignStats{Messages: int(st.Messages), Pushed: int(st.Pushed), PushFailed: int(st.PushFailed),
		PushSkipped: int(st.PushSkipped), PushPending: int(st.PushPending), Read: int(st.Read), Opened: int(st.Opened)}
	return out, nil
}

func (s *Server) adminEmail(ctx context.Context, id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	a, err := s.Q.GetAdminUser(ctx, *id)
	if err != nil {
		return nil
	}
	return &a.Email
}

func (s *Server) loadCampaign(ctx context.Context, id string) (db.Campaign, error) {
	cid, err := parseID(id, "推送活动")
	if err != nil {
		return db.Campaign{}, err
	}
	c, err := s.Q.GetCampaign(ctx, cid)
	if err != nil {
		return c, notFound(err, "推送活动")
	}
	return c, nil
}

// wrongState turns a no-row status transition into a conflict that names the current status.
func (s *Server) wrongState(ctx context.Context, id uuid.UUID, err error, action string) error {
	if !errors.Is(err, pgx.ErrNoRows) {
		return apperr.Internal(err)
	}
	c, gerr := s.Q.GetCampaign(ctx, id)
	if gerr != nil {
		return notFound(gerr, "推送活动")
	}
	return apperr.Conflict("当前状态（" + c.Status + "）不能" + action)
}

func campaignParams(in gen.CampaignInput) (notify.Content, notify.Audience, error) {
	c := notify.Content{
		Name: strings.TrimSpace(in.Name), Category: string(in.Category), Title: strings.TrimSpace(in.Title), Body: strings.TrimSpace(in.Body),
		ImageURL: strings.TrimSpace(deref(in.ImageUrl)), DeepLink: strings.TrimSpace(deref(in.Deeplink)),
	}
	if err := c.Validate(); err != nil {
		return c, notify.Audience{}, err
	}
	a := recode[notify.Audience](in.Audience)
	return c, a, a.Validate()
}

func (s *Server) ListCampaigns(ctx context.Context, req gen.ListCampaignsRequestObject) (gen.ListCampaignsResponseObject, error) {
	lim, off := page(req.Params.Cursor, req.Params.Limit)
	params := db.ListCampaignsParams{Limit: int32(lim), Offset: int32(off)}
	if req.Params.Status != nil {
		v := string(*req.Params.Status)
		params.Status = &v
	}
	rows, err := s.Q.ListCampaigns(ctx, params)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	items := make([]gen.Campaign, 0, len(rows))
	for _, c := range rows {
		out, err := s.campaign(ctx, c)
		if err != nil {
			return nil, err
		}
		items = append(items, out)
	}
	return gen.ListCampaigns200JSONResponse{Items: items, NextCursor: nextCursor(len(rows), lim, off)}, nil
}

func (s *Server) CreateCampaign(ctx context.Context, req gen.CreateCampaignRequestObject) (gen.CreateCampaignResponseObject, error) {
	a, err := require(ctx, PermCampaigns)
	if err != nil {
		return nil, err
	}
	content, aud, err := campaignParams(*req.Body)
	if err != nil {
		return nil, err
	}
	n, err := s.Notify.EstimateAudience(ctx, aud)
	if err != nil {
		return nil, err
	}
	c, err := s.Q.CreateCampaign(ctx, db.CreateCampaignParams{
		Name: content.Name, Category: content.Category, Title: content.Title, Body: content.Body, ImageUrl: content.ImageURL,
		Deeplink: content.DeepLink, Audience: mustJSON(aud), SendAt: req.Body.SendAt, AudienceEstimate: int32(n), CreatedBy: &a.ID,
	})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "campaign.create", "campaign", c.ID.String(), map[string]any{"name": c.Name})
	out, err := s.campaign(ctx, c)
	if err != nil {
		return nil, err
	}
	return gen.CreateCampaign201JSONResponse(out), nil
}

func (s *Server) EstimateCampaignAudience(ctx context.Context, req gen.EstimateCampaignAudienceRequestObject) (gen.EstimateCampaignAudienceResponseObject, error) {
	n, err := s.Notify.EstimateAudience(ctx, recode[notify.Audience](req.Body))
	if err != nil {
		return nil, err
	}
	return gen.EstimateCampaignAudience200JSONResponse{Count: n}, nil
}

func (s *Server) GetCampaign(ctx context.Context, req gen.GetCampaignRequestObject) (gen.GetCampaignResponseObject, error) {
	c, err := s.loadCampaign(ctx, req.CampaignId)
	if err != nil {
		return nil, err
	}
	out, err := s.campaign(ctx, c)
	if err != nil {
		return nil, err
	}
	return gen.GetCampaign200JSONResponse(out), nil
}

func (s *Server) UpdateCampaign(ctx context.Context, req gen.UpdateCampaignRequestObject) (gen.UpdateCampaignResponseObject, error) {
	if _, err := require(ctx, PermCampaigns); err != nil {
		return nil, err
	}
	old, err := s.loadCampaign(ctx, req.CampaignId)
	if err != nil {
		return nil, err
	}
	content, aud, err := campaignParams(*req.Body)
	if err != nil {
		return nil, err
	}
	n, err := s.Notify.EstimateAudience(ctx, aud)
	if err != nil {
		return nil, err
	}
	c, err := s.Q.UpdateCampaignDraft(ctx, db.UpdateCampaignDraftParams{
		ID: old.ID, Name: content.Name, Category: content.Category, Title: content.Title, Body: content.Body, ImageUrl: content.ImageURL,
		Deeplink: content.DeepLink, Audience: mustJSON(aud), SendAt: req.Body.SendAt, AudienceEstimate: int32(n),
	})
	if err != nil {
		return nil, s.wrongState(ctx, old.ID, err, "修改，只有草稿可以修改")
	}
	s.audit(ctx, "campaign.update", "campaign", c.ID.String(), map[string]any{"name": c.Name})
	out, err := s.campaign(ctx, c)
	if err != nil {
		return nil, err
	}
	return gen.UpdateCampaign200JSONResponse(out), nil
}

func (s *Server) DeleteCampaign(ctx context.Context, req gen.DeleteCampaignRequestObject) (gen.DeleteCampaignResponseObject, error) {
	if _, err := require(ctx, PermCampaigns); err != nil {
		return nil, err
	}
	c, err := s.loadCampaign(ctx, req.CampaignId)
	if err != nil {
		return nil, err
	}
	n, err := s.Q.DeleteCampaignDraft(ctx, c.ID)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if n == 0 {
		return nil, apperr.Conflict("只有草稿可以删除")
	}
	s.audit(ctx, "campaign.delete", "campaign", c.ID.String(), map[string]any{"name": c.Name})
	return gen.DeleteCampaign204Response{}, nil
}

func (s *Server) SubmitCampaign(ctx context.Context, req gen.SubmitCampaignRequestObject) (gen.SubmitCampaignResponseObject, error) {
	if _, err := require(ctx, PermCampaigns); err != nil {
		return nil, err
	}
	old, err := s.loadCampaign(ctx, req.CampaignId)
	if err != nil {
		return nil, err
	}
	if old.SendAt != nil && !old.SendAt.After(s.now()) {
		return nil, apperr.Invalid("定时发送时间已过，请修改后再提交")
	}
	aud, err := notify.ParseAudience(old.Audience)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	n, err := s.Notify.EstimateAudience(ctx, aud)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, apperr.Invalid("目标人群为空")
	}
	c, err := s.Q.SubmitCampaign(ctx, db.SubmitCampaignParams{ID: old.ID, AudienceEstimate: int32(n)})
	if err != nil {
		return nil, s.wrongState(ctx, old.ID, err, "提交")
	}
	s.audit(ctx, "campaign.submit", "campaign", c.ID.String(), map[string]any{"audience": n})
	out, err := s.campaign(ctx, c)
	if err != nil {
		return nil, err
	}
	return gen.SubmitCampaign200JSONResponse(out), nil
}

func (s *Server) ApproveCampaign(ctx context.Context, req gen.ApproveCampaignRequestObject) (gen.ApproveCampaignResponseObject, error) {
	a, err := require(ctx, PermCampaignsApprove)
	if err != nil {
		return nil, err
	}
	old, err := s.loadCampaign(ctx, req.CampaignId)
	if err != nil {
		return nil, err
	}
	if old.SendAt != nil && !old.SendAt.After(s.now()) {
		return nil, apperr.Invalid("定时发送时间已过，请驳回后修改时间")
	}
	c, err := s.Q.ApproveCampaign(ctx, db.ApproveCampaignParams{ID: old.ID, ApprovedBy: &a.ID})
	if err != nil {
		return nil, s.wrongState(ctx, old.ID, err, "审批")
	}
	run := orch.CampaignRun{CampaignID: c.ID}
	if c.SendAt != nil {
		run.SendAt = *c.SendAt
	}
	if err := s.Orch.StartCampaign(ctx, run); err != nil {
		if rerr := s.Q.RevertCampaignApproval(ctx, c.ID); rerr != nil {
			s.Log.Error("campaign approval not reverted", "campaign", c.ID, "err", rerr)
		}
		return nil, apperr.Unavailable("发送服务暂时不可用，请稍后再试").WithCause(err)
	}
	s.audit(ctx, "campaign.approve", "campaign", c.ID.String(), map[string]any{"audience": c.AudienceEstimate, "sendAt": c.SendAt})
	out, err := s.campaign(ctx, c)
	if err != nil {
		return nil, err
	}
	return gen.ApproveCampaign200JSONResponse(out), nil
}

func (s *Server) RejectCampaign(ctx context.Context, req gen.RejectCampaignRequestObject) (gen.RejectCampaignResponseObject, error) {
	if _, err := require(ctx, PermCampaignsApprove); err != nil {
		return nil, err
	}
	old, err := s.loadCampaign(ctx, req.CampaignId)
	if err != nil {
		return nil, err
	}
	reason := strings.TrimSpace(req.Body.Reason)
	if reason == "" || utf8.RuneCountInString(reason) > 200 {
		return nil, apperr.Invalid("驳回原因需在 1–200 字之间")
	}
	c, err := s.Q.RejectCampaign(ctx, db.RejectCampaignParams{ID: old.ID, RejectReason: reason})
	if err != nil {
		return nil, s.wrongState(ctx, old.ID, err, "驳回")
	}
	s.audit(ctx, "campaign.reject", "campaign", c.ID.String(), map[string]any{"reason": reason})
	out, err := s.campaign(ctx, c)
	if err != nil {
		return nil, err
	}
	return gen.RejectCampaign200JSONResponse(out), nil
}

func (s *Server) CancelCampaign(ctx context.Context, req gen.CancelCampaignRequestObject) (gen.CancelCampaignResponseObject, error) {
	if _, err := require(ctx, PermCampaigns); err != nil {
		return nil, err
	}
	old, err := s.loadCampaign(ctx, req.CampaignId)
	if err != nil {
		return nil, err
	}
	c, err := s.Q.CancelCampaign(ctx, old.ID)
	if err != nil {
		return nil, s.wrongState(ctx, old.ID, err, "取消")
	}
	// The workflow also stops on its own at the next batch once the status is no longer sending.
	if err := s.Orch.CancelCampaign(ctx, c.ID); err != nil {
		s.Log.Warn("cancel campaign workflow", "campaign", c.ID, "err", err)
	}
	s.audit(ctx, "campaign.cancel", "campaign", c.ID.String(), nil)
	out, err := s.campaign(ctx, c)
	if err != nil {
		return nil, err
	}
	return gen.CancelCampaign200JSONResponse(out), nil
}

func (s *Server) RecallCampaign(ctx context.Context, req gen.RecallCampaignRequestObject) (gen.RecallCampaignResponseObject, error) {
	if _, err := require(ctx, PermCampaigns); err != nil {
		return nil, err
	}
	old, err := s.loadCampaign(ctx, req.CampaignId)
	if err != nil {
		return nil, err
	}
	c, err := s.Q.RecallCampaign(ctx, old.ID)
	if err != nil {
		return nil, s.wrongState(ctx, old.ID, err, "撤回")
	}
	if err := s.Orch.CancelCampaign(ctx, c.ID); err != nil {
		s.Log.Warn("cancel campaign workflow", "campaign", c.ID, "err", err)
	}
	n, err := s.Q.RecallCampaignMessages(ctx, &c.ID)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "campaign.recall", "campaign", c.ID.String(), map[string]any{"messages": n})
	out, err := s.campaign(ctx, c)
	if err != nil {
		return nil, err
	}
	return gen.RecallCampaign200JSONResponse(out), nil
}

func (s *Server) TestSendCampaign(ctx context.Context, req gen.TestSendCampaignRequestObject) (gen.TestSendCampaignResponseObject, error) {
	if _, err := require(ctx, PermCampaigns); err != nil {
		return nil, err
	}
	c, err := s.loadCampaign(ctx, req.CampaignId)
	if err != nil {
		return nil, err
	}
	if len(req.Body.UserIds) == 0 || len(req.Body.UserIds) > 10 {
		return nil, apperr.Invalid("测试用户需要 1–10 个")
	}
	type result = struct {
		Status gen.CampaignTestResultResultsStatus `json:"status"`
		UserId string                              `json:"userId"`
	}
	var results []result
	for _, raw := range req.Body.UserIds {
		r := result{UserId: raw, Status: "not_found"}
		if uid, err := uuid.Parse(strings.TrimSpace(raw)); err == nil {
			if _, err := s.Q.GetUser(ctx, uid); err == nil {
				err := s.Notify.TestSend(ctx, uid, push.Notification{
					Category: c.Category, Title: c.Title, Body: c.Body, Image: c.ImageUrl, DeepLink: c.Deeplink,
				})
				switch {
				case errors.Is(err, push.ErrNoDevice):
					r.Status = "no_device"
				case err != nil:
					r.Status = "failed"
					s.Log.Warn("campaign test push", "campaign", c.ID, "user", uid, "err", err)
				default:
					r.Status = "sent"
				}
			}
		}
		results = append(results, r)
	}
	s.audit(ctx, "campaign.test_send", "campaign", c.ID.String(), map[string]any{"users": req.Body.UserIds})
	return gen.TestSendCampaign200JSONResponse{Results: results}, nil
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
