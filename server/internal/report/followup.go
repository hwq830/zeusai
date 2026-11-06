package report

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/events"
	"github.com/hwq830/zeusai/server/internal/i18n"
)

// Followup is one answered question about a report. The answer only becomes part of the
// report body when the user asks to attach it.
type Followup struct {
	ID         string     `json:"id"`
	Question   string     `json:"question"`
	Answer     string     `json:"answer"`
	AttachedAt *time.Time `json:"attachedAt"`
	CreatedAt  time.Time  `json:"createdAt"`
}

// followupSource is one report source cited by an answer. Index is the citation number the
// answer used ([Index]); the slice order is the independent appendix numbering.
type followupSource struct {
	Index    int    `json:"index"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	SiteName string `json:"siteName,omitempty"`
}

var followupCiteRe = regexp.MustCompile(`\[(\d{1,3})\]`)

// FollowupSourcesJSON collects the report sources an answer cites, in first-citation order,
// so the appendix can renumber them independently of the report body.
func FollowupSourcesJSON(answer string, sources []events.Source) json.RawMessage {
	seen := map[int]bool{}
	out := make([]followupSource, 0, 3)
	for _, match := range followupCiteRe.FindAllStringSubmatch(answer, -1) {
		n, err := strconv.Atoi(match[1])
		if err != nil || n < 1 || n > len(sources) || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, followupSource{
			Index: n, Title: sources[n-1].Title, URL: sources[n-1].URL, SiteName: sources[n-1].SiteName,
		})
	}
	raw, _ := json.Marshal(out)
	return raw
}

// ListFollowups returns a report's follow-ups, newest first. A report the user cannot see
// (deleted or taken down) is a 404, so the history cannot leak a hidden report.
func (s *Service) ListFollowups(ctx context.Context, userID uuid.UUID, reportID string) ([]Followup, error) {
	r, err := s.visibleReport(ctx, userID, reportID)
	if err != nil {
		return nil, err
	}
	rows, err := s.Q.ListReportFollowups(ctx, db.ListReportFollowupsParams{ReportID: r.ID, UserID: userID})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	out := make([]Followup, 0, len(rows))
	for _, row := range rows {
		out = append(out, followupFromRow(row))
	}
	return out, nil
}

// AttachFollowup appends one follow-up as an appendix and marks it attached. It is
// idempotent: an already attached follow-up returns its current state without appending
// again, and the guarded UPDATE makes concurrent attaches append at most once.
func (s *Service) AttachFollowup(ctx context.Context, userID uuid.UUID, reportID, followupID string) (Followup, error) {
	r, err := s.visibleReport(ctx, userID, reportID)
	if err != nil {
		return Followup{}, err
	}
	fid, err := uuid.Parse(followupID)
	if err != nil {
		return Followup{}, apperr.NotFound("追问不存在")
	}
	lang := i18n.Parse(r.Language)

	if s.Pool == nil {
		// Tests and read-only wiring: no transaction, same guard.
		return s.attachFollowup(ctx, s.Q, lang, r, fid, userID)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Followup{}, apperr.Internal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	attached, err := s.attachFollowup(ctx, s.Q.WithTx(tx), lang, r, fid, userID)
	if err != nil {
		return Followup{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Followup{}, apperr.Internal(err)
	}
	return attached, nil
}

func (s *Service) attachFollowup(ctx context.Context, q *db.Queries, lang i18n.Lang, r db.Report, fid, userID uuid.UUID) (Followup, error) {
	row, err := q.AttachReportFollowup(ctx, db.AttachReportFollowupParams{ID: fid, ReportID: r.ID, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		// Already attached (or not this report's): read the current state either way.
		existing, gerr := q.GetReportFollowup(ctx, db.GetReportFollowupParams{ID: fid, ReportID: r.ID, UserID: userID})
		if errors.Is(gerr, pgx.ErrNoRows) {
			return Followup{}, apperr.NotFound("追问不存在")
		}
		if gerr != nil {
			return Followup{}, apperr.Internal(gerr)
		}
		return followupFromRow(existing), nil
	}
	if err != nil {
		return Followup{}, apperr.Internal(err)
	}
	var sources []followupSource
	if len(row.Sources) > 0 {
		_ = json.Unmarshal(row.Sources, &sources)
	}
	if err := q.AppendReportMarkdown(ctx, db.AppendReportMarkdownParams{
		ID: r.ID, UserID: userID, Markdown: followupAppendix(lang, row.Question, row.Answer, sources),
	}); err != nil {
		return Followup{}, apperr.Internal(err)
	}
	return followupFromRow(row), nil
}

// visibleReport is getRow plus the follow-up visibility rule: hidden reports look deleted.
func (s *Service) visibleReport(ctx context.Context, userID uuid.UUID, reportID string) (db.Report, error) {
	r, err := s.getRow(ctx, userID, reportID)
	if err != nil {
		return r, err
	}
	if r.HiddenAt != nil {
		return r, apperr.NotFound("报告不存在")
	}
	return r, nil
}

func followupFromRow(row db.ReportFollowup) Followup {
	return Followup{ID: row.ID.String(), Question: row.Question, Answer: row.Answer, AttachedAt: row.AttachedAt, CreatedAt: row.CreatedAt}
}

// followupAppendix renders the appendix appended to the report body. Citations are renamed
// to [追问n] with their own numbering, so the report's [n] references stay untouched.
func followupAppendix(l i18n.Lang, question, answer string, sources []followupSource) string {
	marker := l.Pick("追问%d", "follow-up %d")
	position := make(map[int]int, len(sources))
	for i, source := range sources {
		position[source.Index] = i + 1
	}
	answer = followupCiteRe.ReplaceAllStringFunc(answer, func(match string) string {
		n, err := strconv.Atoi(match[1 : len(match)-1])
		if err != nil {
			return match
		}
		pos, ok := position[n]
		if !ok {
			return match
		}
		return "[" + fmt.Sprintf(marker, pos) + "]"
	})
	var b strings.Builder
	fmt.Fprintf(&b, "\n\n## %s%s\n\n%s\n", l.Pick("附录 · 追问：", "Appendix · Follow-up: "), strings.TrimSpace(question), strings.TrimSpace(answer))
	if len(sources) > 0 {
		fmt.Fprintf(&b, "\n### %s\n", l.Pick("追问来源", "Follow-up sources"))
		for i, source := range sources {
			title := markdownText(source.Title)
			if title == "" {
				title = markdownText(source.URL)
			}
			if strings.HasPrefix(source.URL, "http://") || strings.HasPrefix(source.URL, "https://") {
				fmt.Fprintf(&b, "%d. [%s](%s)\n", i+1, title, source.URL)
			} else {
				fmt.Fprintf(&b, "%d. %s\n", i+1, title)
			}
		}
	}
	return b.String()
}

// markdownText keeps a title on one line and out of link syntax.
func markdownText(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(strings.ReplaceAll(s, "[", "\\["), "]", "\\]")
}
