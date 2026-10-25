package httpapi

import (
	"context"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/httpapi/gen"
	"github.com/hwq830/zeusai/server/internal/quota"
)

func (s *Server) tokenQuota(u db.User, t quota.Tokens) gen.TokenQuota {
	est := s.Quota.ReportEstimates(u)
	rng := func(depth string) gen.TokenRange {
		e := est[depth]
		return gen.TokenRange{Min: e.Min, Max: e.Max}
	}
	out := gen.TokenQuota{
		Used: t.Used, Limit: t.Limit, Remaining: t.Remaining(), ResetsAt: t.ResetsAt,
		ReportMinTokens: s.Quota.ReportMinTokens(u),
	}
	out.ReportEstimates.Quick, out.ReportEstimates.Standard, out.ReportEstimates.Deep = rng("quick"), rng("standard"), rng("deep")
	return out
}

func (s *Server) GetMyUsage(ctx context.Context, _ gen.GetMyUsageRequestObject) (gen.GetMyUsageResponseObject, error) {
	u, err := s.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	t, err := s.Quota.TokenUsage(ctx, u)
	if err != nil {
		return nil, err
	}
	out := gen.TokenUsage{Quota: s.tokenQuota(u, t), Tasks: []gen.TaskTokenUsage{}}
	days, err := s.Q.DailyBilledTokens(ctx, db.DailyBilledTokensParams{Tz: t.PeriodStart.Location().String(), UserID: &u.ID, Since: t.PeriodStart})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	out.Daily = make([]struct {
		Date   openapi_types.Date `json:"date"`
		Tokens int64              `json:"tokens"`
	}, len(days))
	for i, d := range days {
		out.Daily[i].Date, out.Daily[i].Tokens = openapi_types.Date{Time: d.Day}, d.Tokens
	}
	tasks, err := s.Q.TaskBilledTokensSince(ctx, db.TaskBilledTokensSinceParams{UserID: u.ID, CreatedAt: t.PeriodStart, Limit: 100})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	for _, r := range tasks {
		out.Tasks = append(out.Tasks, gen.TaskTokenUsage{
			TaskId: r.ID.String(), Kind: gen.TaskKind(r.Kind), Title: r.Title, Tokens: r.Tokens, Counted: r.Status != "failed",
			ReportId: idString(r.ReportID), BriefingId: idString(r.BriefingID), CreatedAt: r.CreatedAt,
		})
	}
	return gen.GetMyUsage200JSONResponse(out), nil
}

func idString(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	v := id.String()
	return &v
}
