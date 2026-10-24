// Package report owns research reports: creating them (quota, task, workflow), reading them,
// rendering the shared HTML template, follow-up questions and H5 share links.
package report

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hwq830/zeusai/prompts"
	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/blob"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/events"
	"github.com/hwq830/zeusai/server/internal/moderation"
	"github.com/hwq830/zeusai/server/internal/orch"
	"github.com/hwq830/zeusai/server/internal/quota"
	reporttemplate "github.com/hwq830/zeusai/web/report-template"
)

// MaxConcurrentTasks matches briefing.MaxConcurrentTasks.
const MaxConcurrentTasks = 2

var DepthLabels = map[string]string{"quick": "快速报告", "standard": "标准报告", "deep": "深度报告"}

type Report struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Topic       string     `json:"topic"`
	Depth       string     `json:"depth"`
	Status      string     `json:"status"`
	Summary     string     `json:"summary,omitempty"`
	TaskID      string     `json:"taskId"`
	SourceCount int        `json:"sourceCount"`
	WordCount   int        `json:"wordCount,omitempty"`
	HTMLURL     string     `json:"htmlUrl,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	Feedback    *Feedback  `json:"feedback,omitempty"`
}

// Feedback is the user's satisfaction rating of a report.
type Feedback struct {
	Rating  string   `json:"rating"`
	Reasons []string `json:"reasons,omitempty"`
	Comment string   `json:"comment,omitempty"`
}

var feedbackReasons = map[string]bool{"inaccurate": true, "shallow": true, "bad_sources": true, "off_topic": true, "slow": true, "other": true}

func FromRow(r db.Report) Report {
	out := Report{
		ID: r.ID.String(), Title: r.Title, Topic: r.Topic, Depth: r.Depth, Status: r.Status, Summary: r.Summary,
		SourceCount: int(r.SourceCount), WordCount: int(r.WordCount), CreatedAt: r.CreatedAt, CompletedAt: r.CompletedAt,
	}
	if r.TaskID != nil {
		out.TaskID = r.TaskID.String()
	}
	if r.Status == "done" {
		out.HTMLURL = "/v1/reports/" + out.ID + "/html"
	}
	return out
}

type Input struct {
	Topic                string   `json:"topic"`
	Depth                string   `json:"depth,omitempty"`
	Focuses              []string `json:"focuses,omitempty"`
	SourceBriefingItemID *string  `json:"sourceBriefingItemId,omitempty"`
}

type Share struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	Title     string    `json:"title"`
	Summary   string    `json:"summary,omitempty"`
	Scope     string    `json:"scope"`
	CreatedAt time.Time `json:"createdAt"`
}

type Service struct {
	Q             *db.Queries
	Quota         *quota.Service
	Orch          orch.Client
	Mod           moderation.Checker
	Blob          blob.Store // nil: PDF export unavailable
	PublicBaseURL string
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// Subtitle is the progress-page line under the title, e.g. "标准报告 · 侧重市场规模、竞品对比".
func Subtitle(depth string, focuses []string) string {
	s := DepthLabels[depth]
	if len(focuses) > 0 {
		s += " · 侧重" + strings.Join(focuses, "、")
	}
	return s
}

func (s *Service) checkConcurrency(ctx context.Context, userID uuid.UUID) error {
	n, err := s.Q.CountActiveTasks(ctx, userID)
	if err != nil {
		return apperr.Internal(err)
	}
	if n >= MaxConcurrentTasks {
		return apperr.RateLimited("同时进行的任务太多，请等当前任务完成")
	}
	return nil
}

// Create starts a research report and returns its task.
func (s *Service) Create(ctx context.Context, u db.User, in Input) (db.Task, error) {
	in.Topic = strings.TrimSpace(in.Topic)
	if in.Topic == "" || utf8.RuneCountInString(in.Topic) > 500 {
		return db.Task{}, apperr.Invalid("调研题目不能为空且不超过 500 字")
	}
	if in.Depth == "" {
		in.Depth = "standard"
	}
	if DepthLabels[in.Depth] == "" {
		return db.Task{}, apperr.Invalid("depth 应为 quick / standard / deep")
	}
	if len(in.Focuses) > 6 {
		return db.Task{}, apperr.Invalid("侧重角度最多 6 个")
	}
	if err := moderation.Require(ctx, s.Mod, append([]string{in.Topic}, in.Focuses...)...); err != nil {
		return db.Task{}, err
	}
	if err := s.Quota.CheckReport(ctx, u); err != nil {
		return db.Task{}, err
	}
	if err := s.checkConcurrency(ctx, u.ID); err != nil {
		return db.Task{}, err
	}
	input, _ := json.Marshal(in)
	title := clip(in.Topic, 30)
	t, err := s.Q.CreateTask(ctx, db.CreateTaskParams{
		UserID: u.ID, Kind: "report", Title: title, Subtitle: Subtitle(in.Depth, in.Focuses), Input: input, PromptVersion: prompts.Version,
	})
	if err != nil {
		return db.Task{}, apperr.Internal(err)
	}
	r, err := s.Q.CreateReport(ctx, db.CreateReportParams{
		UserID: u.ID, TaskID: &t.ID, Title: title, Topic: in.Topic, Depth: in.Depth, Focuses: orEmpty(in.Focuses),
		SourceBriefingItemID: in.SourceBriefingItemID,
	})
	if err != nil {
		return db.Task{}, apperr.Internal(err)
	}
	if err := s.Q.SetTaskLinks(ctx, db.SetTaskLinksParams{ID: t.ID, UserID: u.ID, ReportID: &r.ID}); err != nil {
		return db.Task{}, apperr.Internal(err)
	}
	t.ReportID = &r.ID
	if err := s.Orch.StartReport(ctx, orch.ReportRun{UserID: u.ID, TaskID: t.ID, ReportID: r.ID}); err != nil {
		_ = s.Q.SetReportStatus(ctx, db.SetReportStatusParams{ID: r.ID, UserID: u.ID, Status: "failed"})
		_ = s.Q.UpdateTaskProgress(ctx, db.UpdateTaskProgressParams{ID: t.ID, UserID: u.ID, Status: "failed"})
		return db.Task{}, apperr.Unavailable("任务启动失败，请稍后再试").WithCause(err)
	}
	return t, nil
}

func (s *Service) getRow(ctx context.Context, userID uuid.UUID, id string) (db.Report, error) {
	rid, err := uuid.Parse(id)
	if err != nil {
		return db.Report{}, apperr.NotFound("报告不存在")
	}
	r, err := s.Q.GetReport(ctx, db.GetReportParams{ID: rid, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return r, apperr.NotFound("报告不存在")
	}
	if err != nil {
		return r, apperr.Internal(err)
	}
	return r, nil
}

func (s *Service) Get(ctx context.Context, userID uuid.UUID, id string) (Report, error) {
	r, err := s.getRow(ctx, userID, id)
	if err != nil {
		return Report{}, err
	}
	out := FromRow(r)
	f, err := s.Q.GetReportFeedback(ctx, db.GetReportFeedbackParams{ReportID: r.ID, UserID: userID})
	switch {
	case err == nil:
		out.Feedback = &Feedback{Rating: f.Rating, Reasons: f.Reasons, Comment: f.Comment}
	case !errors.Is(err, pgx.ErrNoRows):
		return Report{}, apperr.Internal(err)
	}
	return out, nil
}

// Rate records the user's satisfaction with a finished report; the last rating wins.
// Reasons only apply to unsatisfied ratings.
func (s *Service) Rate(ctx context.Context, userID uuid.UUID, id string, f Feedback) error {
	if f.Rating != "satisfied" && f.Rating != "unsatisfied" {
		return apperr.Invalid("rating 应为 satisfied / unsatisfied")
	}
	f.Comment = strings.TrimSpace(f.Comment)
	if utf8.RuneCountInString(f.Comment) > 500 {
		return apperr.Invalid("补充说明不能超过 500 字")
	}
	reasons := []string{}
	if f.Rating == "unsatisfied" {
		seen := map[string]bool{}
		for _, r := range f.Reasons {
			if !feedbackReasons[r] {
				return apperr.Invalid("未知的原因：" + r)
			}
			if !seen[r] {
				seen[r] = true
				reasons = append(reasons, r)
			}
		}
	}
	r, err := s.getRow(ctx, userID, id)
	if err != nil {
		return err
	}
	if r.Status != "done" {
		return apperr.Conflict("报告完成后才能评价")
	}
	if _, err := s.Q.UpsertReportFeedback(ctx, db.UpsertReportFeedbackParams{
		ID: r.ID, UserID: userID, Rating: f.Rating, Reasons: reasons, Comment: f.Comment,
	}); err != nil {
		return apperr.Internal(err)
	}
	return nil
}

type ListParams struct {
	Status, Q     string
	Limit, Offset int
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *Service) List(ctx context.Context, userID uuid.UUID, p ListParams) ([]Report, error) {
	rows, err := s.Q.ListReports(ctx, db.ListReportsParams{UserID: userID, Status: strPtr(p.Status), Q: strPtr(p.Q), Limit: int32(p.Limit), Offset: int32(p.Offset)})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	out := make([]Report, 0, len(rows))
	for _, r := range rows {
		out = append(out, FromRow(r))
	}
	return out, nil
}

func (s *Service) Delete(ctx context.Context, userID uuid.UUID, id string) error {
	r, err := s.getRow(ctx, userID, id)
	if err != nil {
		return err
	}
	if r.Status == "running" && r.TaskID != nil {
		_ = s.Orch.CancelTask(ctx, *r.TaskID)
	}
	if _, err := s.Q.DeleteReport(ctx, db.DeleteReportParams{ID: r.ID, UserID: userID}); err != nil {
		return apperr.Internal(err)
	}
	if r.PdfKey != nil && s.Blob != nil {
		_ = s.Blob.Delete(ctx, *r.PdfKey)
	}
	return nil
}

func templateData(r db.Report, full bool) reporttemplate.Data {
	d := reporttemplate.Data{Title: r.Title, Summary: r.Summary, DepthLabel: DepthLabels[r.Depth], SourceCount: int(r.SourceCount)}
	if r.CompletedAt != nil {
		d.CompletedAt = r.CompletedAt.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02")
	}
	if full {
		d.Markdown = r.Markdown
		var srcs []events.Source
		_ = json.Unmarshal(r.Sources, &srcs)
		for i, src := range srcs {
			d.Sources = append(d.Sources, reporttemplate.Source{Index: i + 1, URL: src.URL, Title: src.Title, SiteName: src.SiteName})
		}
	}
	return d
}

// PrintHTML renders a report for PDF printing.
func PrintHTML(r db.Report) ([]byte, error) { return reporttemplate.Render(templateData(r, true)) }

// PDFKey names the stored PDF for the report's current content, so a follow-up (which
// appends to the markdown) makes the old file stale.
func PDFKey(r db.Report) string {
	h := sha256.Sum256([]byte(r.Title + "\x00" + r.Markdown))
	return fmt.Sprintf("reports/%s/%s-%x.pdf", r.UserID, r.ID, h[:6])
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
	r, err := s.getRow(ctx, userID, id)
	if err != nil {
		return FileLink{}, err
	}
	if r.Status != "done" {
		return FileLink{}, apperr.Conflict("报告还没有生成完")
	}
	key := PDFKey(r)
	if r.PdfStatus == "ready" && r.PdfKey != nil && *r.PdfKey == key {
		u, exp, err := s.Blob.SignedURL(ctx, key, pdfLinkTTL)
		if err != nil {
			return FileLink{}, apperr.Internal(err)
		}
		return FileLink{Status: "ready", URL: u, ExpiresAt: &exp}, nil
	}
	if err := s.Q.SetReportPDF(ctx, db.SetReportPDFParams{ID: r.ID, UserID: userID, PdfStatus: "generating", PdfKey: r.PdfKey}); err != nil {
		return FileLink{}, apperr.Internal(err)
	}
	// Idempotent while a print is running; after a failure or a follow-up it starts anew.
	if err := s.Orch.StartReportPDF(ctx, orch.ReportPDFRun{UserID: userID, ReportID: r.ID, Key: key}); err != nil {
		return FileLink{}, apperr.Internal(err)
	}
	return FileLink{Status: "generating"}, nil
}

// HTML renders a finished report for the App's WebView.
func (s *Service) HTML(ctx context.Context, userID uuid.UUID, id string) ([]byte, error) {
	r, err := s.getRow(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if r.Status != "done" {
		return nil, apperr.Conflict("报告还没有生成完")
	}
	b, err := reporttemplate.Render(templateData(r, true))
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return b, nil
}

func (s *Service) Followup(ctx context.Context, userID uuid.UUID, id, question string) (db.Task, error) {
	question = strings.TrimSpace(question)
	if question == "" || utf8.RuneCountInString(question) > 1000 {
		return db.Task{}, apperr.Invalid("问题不能为空且不超过 1000 字")
	}
	if err := moderation.Require(ctx, s.Mod, question); err != nil {
		return db.Task{}, err
	}
	r, err := s.getRow(ctx, userID, id)
	if err != nil {
		return db.Task{}, err
	}
	if r.Status != "done" {
		return db.Task{}, apperr.Conflict("报告生成完后才能追问")
	}
	if err := s.checkConcurrency(ctx, userID); err != nil {
		return db.Task{}, err
	}
	input, _ := json.Marshal(map[string]string{"question": question})
	t, err := s.Q.CreateTask(ctx, db.CreateTaskParams{
		UserID: userID, Kind: "followup", Title: clip(question, 30), Subtitle: "追问 · " + clip(r.Title, 20), Input: input,
		ReportID: &r.ID, PromptVersion: prompts.Version,
	})
	if err != nil {
		return db.Task{}, apperr.Internal(err)
	}
	if err := s.Orch.StartFollowup(ctx, orch.FollowupRun{UserID: userID, TaskID: t.ID, ReportID: r.ID, Question: question}); err != nil {
		_ = s.Q.UpdateTaskProgress(ctx, db.UpdateTaskProgressParams{ID: t.ID, UserID: userID, Status: "failed"})
		return db.Task{}, apperr.Unavailable("任务启动失败，请稍后再试").WithCause(err)
	}
	return t, nil
}

func (s *Service) CreateShare(ctx context.Context, userID uuid.UUID, id, scope string) (Share, error) {
	if scope != "full" && scope != "summary" {
		return Share{}, apperr.Invalid("scope 应为 full / summary")
	}
	r, err := s.getRow(ctx, userID, id)
	if err != nil {
		return Share{}, err
	}
	if r.Status != "done" {
		return Share{}, apperr.Conflict("报告生成完后才能分享")
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	token := base64.RawURLEncoding.EncodeToString(b)
	sh, err := s.Q.CreateShare(ctx, db.CreateShareParams{UserID: userID, ReportID: r.ID, Token: token, Scope: scope})
	if err != nil {
		return Share{}, apperr.Internal(err)
	}
	return Share{
		ID: sh.ID.String(), URL: strings.TrimRight(s.PublicBaseURL, "/") + "/s/" + token, Title: r.Title,
		Summary: r.Summary, Scope: scope, CreatedAt: sh.CreatedAt,
	}, nil
}

func (s *Service) RevokeShare(ctx context.Context, userID uuid.UUID, id string) error {
	sid, err := uuid.Parse(id)
	if err != nil {
		return apperr.NotFound("分享不存在")
	}
	n, err := s.Q.RevokeShare(ctx, db.RevokeShareParams{ID: sid, UserID: userID})
	if err != nil {
		return apperr.Internal(err)
	}
	if n == 0 {
		return apperr.NotFound("分享不存在")
	}
	return nil
}

// SharedHTML renders the public H5 page for a share token.
func (s *Service) SharedHTML(ctx context.Context, token string) ([]byte, error) {
	row, err := s.Q.GetSharedReport(ctx, token)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.NotFound("分享已失效")
	}
	if err != nil {
		return nil, apperr.Internal(err)
	}
	_ = s.Q.IncrementShareViews(ctx, token)
	r := db.Report{ID: row.ID, Title: row.Title, Depth: row.Depth, Summary: row.Summary, Markdown: row.Markdown,
		Sources: row.Sources, SourceCount: row.SourceCount, CompletedAt: row.CompletedAt}
	d := templateData(r, row.Scope == "full")
	d.Share = true
	d.AppURL = strings.TrimRight(s.PublicBaseURL, "/") + "/app"
	return reporttemplate.Render(d)
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
