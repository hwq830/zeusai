package adminapi

import (
	"context"
	"strings"

	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
)

// adminFollowupAnswerLimit caps the answer in list responses; the API returns full answers
// only through the App's own follow-up endpoints, not the console.
const adminFollowupAnswerLimit = 500

// ListReportFollowups is the console's read-only view of one report's follow-ups.
func (s *Server) ListReportFollowups(ctx context.Context, req gen.ListReportFollowupsRequestObject) (gen.ListReportFollowupsResponseObject, error) {
	if _, err := require(ctx, PermContentRead); err != nil {
		return nil, err
	}
	id, err := parseID(req.ReportId, "报告")
	if err != nil {
		return nil, err
	}
	if _, err := s.Q.AdminGetReport(ctx, id); err != nil {
		return nil, notFound(err, "报告")
	}
	lim, off := page(req.Params.Cursor, req.Params.Limit)
	rows, err := s.Q.AdminReportFollowups(ctx, db.AdminReportFollowupsParams{ReportID: id, Limit: int32(lim), Offset: int32(off)})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	items := make([]gen.AdminReportFollowup, 0, len(rows))
	for _, row := range rows {
		answer, truncated := truncateAnswer(row.Answer)
		items = append(items, gen.AdminReportFollowup{
			Id: row.ID.String(), Question: row.Question, Answer: answer, AnswerTruncated: truncated,
			AttachedAt: row.AttachedAt, CreatedAt: row.CreatedAt,
		})
	}
	return gen.ListReportFollowups200JSONResponse{Items: items, NextCursor: nextCursor(len(rows), lim, off)}, nil
}

// ListFollowups is the console's read-only, cross-user follow-up list.
func (s *Server) ListFollowups(ctx context.Context, req gen.ListFollowupsRequestObject) (gen.ListFollowupsResponseObject, error) {
	if _, err := require(ctx, PermContentRead); err != nil {
		return nil, err
	}
	uid, err := optUser(req.Params.UserId)
	if err != nil {
		return nil, err
	}
	lim, off := page(req.Params.Cursor, req.Params.Limit)
	rows, err := s.Q.AdminListFollowups(ctx, db.AdminListFollowupsParams{
		Attached: req.Params.Attached, UserID: uid, Q: followupSearchPattern(req.Params.Q), Lim: int32(lim), Off: int32(off),
	})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	items := make([]gen.AdminFollowup, 0, len(rows))
	for _, row := range rows {
		answer, truncated := truncateAnswer(row.Answer)
		items = append(items, gen.AdminFollowup{
			Id: row.ID.String(), ReportId: row.ReportID.String(), ReportTitle: row.ReportTitle, ReportHidden: row.ReportHidden,
			UserId: row.UserID.String(), Question: row.Question, Answer: answer, AnswerTruncated: truncated,
			AttachedAt: row.AttachedAt, CreatedAt: row.CreatedAt,
		})
	}
	return gen.ListFollowups200JSONResponse{Items: items, NextCursor: nextCursor(len(rows), lim, off)}, nil
}

// truncateAnswer cuts the answer to the list limit and reports whether it was cut.
func truncateAnswer(answer string) (string, bool) {
	runes := []rune(answer)
	if len(runes) <= adminFollowupAnswerLimit {
		return answer, false
	}
	return string(runes[:adminFollowupAnswerLimit]), true
}

// followupSearchPattern turns a keyword into a LIKE pattern with % and _ matched literally.
func followupSearchPattern(q *string) *string {
	if q == nil || strings.TrimSpace(*q) == "" {
		return nil
	}
	var b strings.Builder
	b.WriteByte('%')
	for _, r := range strings.TrimSpace(*q) {
		if r == '%' || r == '_' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('%')
	pattern := b.String()
	return &pattern
}
