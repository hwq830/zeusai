package adminapi

import (
	"context"
	"slices"
	"strconv"

	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
)

func (s *Server) ListExternalCalls(ctx context.Context, req gen.ListExternalCallsRequestObject) (gen.ListExternalCallsResponseObject, error) {
	p := req.Params
	limit := 50
	if p.Limit != nil {
		limit = min(100, max(1, *p.Limit))
	}
	q := db.ListExternalCallsParams{PageLimit: int32(limit + 1), Provider: strPtr(p.Provider), Model: strPtr(p.Model), TaskID: p.TaskId, UserID: p.UserId, Since: p.From, Until: p.To}
	if p.Kind != nil {
		if !slices.Contains([]string{"model", "search"}, string(*p.Kind)) {
			return nil, apperr.Invalid("调用类型无效")
		}
		v := string(*p.Kind)
		q.Kind = &v
	}
	if p.Status != nil {
		if !slices.Contains([]string{"running", "success", "empty", "failed", "timeout", "cancelled", "skipped", "unknown"}, string(*p.Status)) {
			return nil, apperr.Invalid("调用状态无效")
		}
		v := string(*p.Status)
		q.Status = &v
	}
	if p.Cursor != nil && *p.Cursor != "" {
		id, err := strconv.ParseInt(*p.Cursor, 10, 64)
		if err != nil || id <= 0 {
			return nil, apperr.Invalid("分页参数无效")
		}
		q.BeforeSeq = &id
	}
	if p.From != nil && p.To != nil && !p.From.Before(*p.To) {
		return nil, apperr.Invalid("开始时间必须早于结束时间")
	}
	rows, err := s.Q.ListExternalCalls(ctx, q)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	out := gen.ExternalCallList{Items: []gen.ExternalCall{}}
	if len(rows) > limit {
		v := strconv.FormatInt(rows[limit-1].Seq, 10)
		out.NextCursor = &v
		rows = rows[:limit]
	}
	intPtr := func(n *int32) *int {
		if n == nil {
			return nil
		}
		v := int(*n)
		return &v
	}
	for _, r := range rows {
		item := gen.ExternalCall{Id: r.ID, GroupId: r.GroupID, Attempt: int(r.Attempt), Kind: r.Kind, Provider: r.Provider, Model: r.Model, Protocol: r.Protocol,
			Purpose: r.Purpose, Origin: r.Origin, TaskId: r.TaskID, UserId: r.UserID, Region: r.Region, Dispatched: r.Dispatched, Status: r.DisplayStatus,
			StartedAt: r.StartedAt, DeadlineAt: r.DeadlineAt, FinishedAt: r.FinishedAt, DurationMs: r.DurationMs,
			InputTokens: intPtr(r.InputTokens), OutputTokens: intPtr(r.OutputTokens), CachedTokens: intPtr(r.CachedTokens), ResultCount: intPtr(r.ResultCount), HttpStatus: intPtr(r.HttpStatus),
		}
		if r.ErrorCode != "" {
			item.ErrorCode = &r.ErrorCode
		}
		if r.DisplayStatus == "unknown" && r.Status == "running" && r.ErrorCode == "" {
			code := "completion_not_recorded"
			item.ErrorCode = &code
		}
		if r.RequestID != "" {
			item.RequestId = &r.RequestID
		}
		if r.QueryText != "" {
			item.Query = &r.QueryText
		}
		if r.ResponseModel != "" {
			item.ResponseModel = &r.ResponseModel
		}
		out.Items = append(out.Items, item)
	}
	return gen.ListExternalCalls200JSONResponse(out), nil
}
