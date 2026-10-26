package briefing

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/i18n"
	"github.com/hwq830/zeusai/server/internal/orch"
	"github.com/hwq830/zeusai/server/internal/remoteconfig"
	briefingtemplate "github.com/hwq830/zeusai/web/briefing-template"
)

// TemplateData converts an issue for the shared template, with every section it holds.
func TemplateData(b Briefing) briefingtemplate.Data {
	d := briefingtemplate.Data{Title: b.Title, Date: b.Date, Headline: b.Headline, Lang: string(i18n.Parse(b.Language))}
	for _, s := range b.Sections {
		ts := briefingtemplate.Section{Title: s.Title}
		for _, it := range s.Items {
			ti := briefingtemplate.Item{ID: it.ID, Title: it.Title, Summary: it.Summary}
			for _, src := range it.Sources {
				ti.Sources = append(ti.Sources, briefingtemplate.Source{URL: src.URL, Title: src.Title, SiteName: src.SiteName})
			}
			ts.Items = append(ts.Items, ti)
		}
		d.Sections = append(d.Sections, ts)
	}
	return d
}

// IssueFromRow converts a stored issue, sections included.
func IssueFromRow(r db.Briefing) Briefing { return briefingFromRow(r, true) }

// PrintHTML renders an issue for PDF printing.
func PrintHTML(r db.Briefing) ([]byte, error) {
	return briefingtemplate.Render(TemplateData(IssueFromRow(r)))
}

// PDFKey names the stored PDF for the issue's current content, so a same-day re-run (which
// regenerates the sections) makes the old file stale.
func PDFKey(r db.Briefing) string {
	h := sha256.New()
	h.Write([]byte(r.Title + "\x00" + r.Headline + "\x00" + r.Language + "\x00"))
	h.Write(r.Sections)
	return fmt.Sprintf("briefings/%s/%s-%x.pdf", r.UserID, r.ID, h.Sum(nil)[:6])
}

func (s *Service) readyRow(ctx context.Context, userID uuid.UUID, id string) (db.Briefing, error) {
	bid, err := parseID(id, "简报")
	if err != nil {
		return db.Briefing{}, err
	}
	r, err := s.Q.GetBriefing(ctx, db.GetBriefingParams{ID: bid, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Briefing{}, apperr.NotFound("简报不存在")
	}
	if err != nil {
		return db.Briefing{}, apperr.Internal(err)
	}
	if r.Status != "ready" {
		return db.Briefing{}, apperr.Conflict("简报还没有生成完")
	}
	return r, nil
}

// HTML renders a finished issue, every section included, for the App.
func (s *Service) HTML(ctx context.Context, userID uuid.UUID, id string) ([]byte, error) {
	r, err := s.readyRow(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	b, err := PrintHTML(r)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return b, nil
}

type FileLink struct {
	Status    string     `json:"status"` // ready | generating
	URL       string     `json:"url,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

const pdfLinkTTL = 15 * time.Minute

// PDF returns a download link, or starts printing and returns status=generating.
func (s *Service) PDF(ctx context.Context, userID uuid.UUID, id string) (FileLink, error) {
	if s.Blob == nil {
		return FileLink{}, apperr.Unavailable("PDF 导出即将开放")
	}
	if !s.Config.Bool(remoteconfig.Context{UserID: userID.String()}, remoteconfig.KeyReportPDFEnabled, true) {
		return FileLink{}, apperr.Unavailable("PDF 导出暂时不可用，请稍后再试")
	}
	r, err := s.readyRow(ctx, userID, id)
	if err != nil {
		return FileLink{}, err
	}
	key := PDFKey(r)
	if r.PdfStatus == "ready" && r.PdfKey != nil && *r.PdfKey == key {
		u, exp, err := s.Blob.SignedURL(ctx, key, pdfLinkTTL)
		if err != nil {
			return FileLink{}, apperr.Internal(err)
		}
		return FileLink{Status: "ready", URL: u, ExpiresAt: &exp}, nil
	}
	if err := s.Q.SetBriefingPDF(ctx, db.SetBriefingPDFParams{ID: r.ID, UserID: userID, PdfStatus: "generating", PdfKey: r.PdfKey}); err != nil {
		return FileLink{}, apperr.Internal(err)
	}
	// Idempotent while a print is running; after a failure or a re-run it starts anew.
	if err := s.Orch.StartBriefingPDF(ctx, orch.BriefingPDFRun{UserID: userID, BriefingID: r.ID, Key: key}); err != nil {
		return FileLink{}, apperr.Internal(err)
	}
	return FileLink{Status: "generating"}, nil
}
