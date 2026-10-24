// Package feedback owns in-app feedback: screenshot uploads, submitting feedback, and the
// user's view of their feedback with official replies. Operators handle feedback in adminapi.
package feedback

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/blob"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/moderation"
)

const (
	MaxAttachmentBytes = 5 << 20
	MaxAttachments     = 3
	MaxPerDay          = 20
	// Uploads not attached to a submitted feedback within this window are removed.
	StaleUploadAge = 24 * time.Hour
	linkTTL        = time.Hour
)

var Types = map[string]bool{"bug": true, "suggestion": true, "other": true}

type Attachment struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

type Reply struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
}

type Ticket struct {
	ID          string       `json:"id"`
	Type        string       `json:"type"`
	Content     string       `json:"content"`
	Contact     string       `json:"contact,omitempty"`
	Status      string       `json:"status"`
	Unread      bool         `json:"unread"`
	Attachments []Attachment `json:"attachments,omitempty"`
	Replies     []Reply      `json:"replies,omitempty"`
	LastReplyAt *time.Time   `json:"lastReplyAt,omitempty"`
	CreatedAt   time.Time    `json:"createdAt"`
	UpdatedAt   time.Time    `json:"updatedAt"`
}

// Unread reports whether an official reply arrived after the user last opened the feedback.
func Unread(f db.Feedback) bool {
	return f.LastReplyAt != nil && (f.UserReadAt == nil || f.UserReadAt.Before(*f.LastReplyAt))
}

func FromRow(f db.Feedback) Ticket {
	return Ticket{
		ID: f.ID.String(), Type: f.Type, Content: f.Content, Contact: f.Contact, Status: f.Status,
		Unread: Unread(f), LastReplyAt: f.LastReplyAt, CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt,
	}
}

type Input struct {
	Type          string          `json:"type"`
	Content       string          `json:"content"`
	AttachmentIDs []string        `json:"attachmentIds,omitempty"`
	Contact       string          `json:"contact,omitempty"`
	ReportID      string          `json:"reportId,omitempty"`
	TaskID        string          `json:"taskId,omitempty"`
	Client        json.RawMessage `json:"client,omitempty"`
}

type Service struct {
	Pool interface {
		Begin(ctx context.Context) (pgx.Tx, error)
	}
	Q    *db.Queries
	Blob blob.Store         // nil: screenshots unavailable
	Mod  moderation.Checker // nil: nothing is flagged
	Now  func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

var jpegMagic = []byte{0xFF, 0xD8, 0xFF}

// Upload stores one JPEG screenshot and returns its id.
func (s *Service) Upload(ctx context.Context, userID uuid.UUID, body io.Reader) (string, error) {
	if s.Blob == nil {
		return "", apperr.Unavailable("暂时无法上传截图")
	}
	b, err := io.ReadAll(io.LimitReader(body, MaxAttachmentBytes+1))
	if err != nil {
		return "", apperr.Invalid("上传失败，请重试").WithCause(err)
	}
	if len(b) > MaxAttachmentBytes {
		return "", apperr.Invalid("图片不能超过 5 MB")
	}
	if !bytes.HasPrefix(b, jpegMagic) {
		return "", apperr.Invalid("只支持 JPEG 图片")
	}
	s.cleanStale(ctx, userID)
	key := "feedback/" + userID.String() + "/" + uuid.NewString() + ".jpg"
	if err := s.Blob.Put(ctx, key, b, "image/jpeg"); err != nil {
		return "", apperr.Internal(err)
	}
	a, err := s.Q.CreateFeedbackAttachment(ctx, db.CreateFeedbackAttachmentParams{UserID: userID, BlobKey: key, SizeBytes: int32(len(b))})
	if err != nil {
		_ = s.Blob.Delete(ctx, key)
		return "", apperr.Internal(err)
	}
	return a.ID.String(), nil
}

// cleanStale removes this user's abandoned uploads. Best effort: failures leave them for next time.
func (s *Service) cleanStale(ctx context.Context, userID uuid.UUID) {
	keys, err := s.Q.DeleteStaleFeedbackAttachments(ctx, db.DeleteStaleFeedbackAttachmentsParams{UserID: userID, CreatedAt: s.now().Add(-StaleUploadAge)})
	if err != nil {
		return
	}
	for _, k := range keys {
		_ = s.Blob.Delete(ctx, k)
	}
}

func optionalID(s string) (uuid.UUID, bool) {
	id, err := uuid.Parse(strings.TrimSpace(s))
	return id, err == nil
}

// Create submits feedback and claims its uploaded screenshots.
func (s *Service) Create(ctx context.Context, userID uuid.UUID, in Input) (Ticket, error) {
	in.Content = strings.TrimSpace(in.Content)
	in.Contact = strings.TrimSpace(in.Contact)
	switch n := utf8.RuneCountInString(in.Content); {
	case !Types[in.Type]:
		return Ticket{}, apperr.Invalid("请选择反馈类型")
	case n < 5:
		return Ticket{}, apperr.Invalid("请再多描述一点（至少 5 个字）")
	case n > 1000:
		return Ticket{}, apperr.Invalid("内容不能超过 1000 字")
	case utf8.RuneCountInString(in.Contact) > 100:
		return Ticket{}, apperr.Invalid("联系方式太长")
	case len(in.AttachmentIDs) > MaxAttachments:
		return Ticket{}, apperr.Invalid("最多 3 张截图")
	}
	n, err := s.Q.CountFeedbackSince(ctx, db.CountFeedbackSinceParams{UserID: userID, CreatedAt: s.now().Add(-24 * time.Hour)})
	if err != nil {
		return Ticket{}, apperr.Internal(err)
	}
	if n >= MaxPerDay {
		return Ticket{}, apperr.RateLimited("今天提交的反馈太多了，明天再来吧")
	}

	params := db.CreateFeedbackParams{UserID: userID, Type: in.Type, Content: in.Content, Contact: in.Contact, Client: json.RawMessage("{}")}
	if len(in.Client) > 0 && len(in.Client) <= 2048 && json.Valid(in.Client) && bytes.HasPrefix(bytes.TrimSpace(in.Client), []byte("{")) {
		params.Client = in.Client
	}
	// Context links are kept only when they belong to the user.
	if id, ok := optionalID(in.ReportID); ok {
		if _, err := s.Q.GetReport(ctx, db.GetReportParams{ID: id, UserID: userID}); err == nil {
			params.ReportID = &id
		}
	}
	if id, ok := optionalID(in.TaskID); ok {
		if _, err := s.Q.GetTask(ctx, db.GetTaskParams{ID: id, UserID: userID}); err == nil {
			params.TaskID = &id
		}
	}
	if s.Mod != nil {
		if v, err := s.Mod.Check(ctx, moderation.Input, in.Content); err == nil && v.Blocked {
			params.FlaggedReason = v.Reason
			if params.FlaggedReason == "" {
				params.FlaggedReason = "命中关键词"
			}
		}
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Ticket{}, apperr.Internal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.Q.WithTx(tx)
	f, err := q.CreateFeedback(ctx, params)
	if err != nil {
		return Ticket{}, apperr.Internal(err)
	}
	for i, raw := range in.AttachmentIDs {
		id, ok := optionalID(raw)
		if !ok {
			return Ticket{}, apperr.Invalid("截图已失效，请重新上传")
		}
		n, err := q.AttachFeedbackAttachment(ctx, db.AttachFeedbackAttachmentParams{ID: id, UserID: userID, FeedbackID: &f.ID, Position: int32(i)})
		if err != nil {
			return Ticket{}, apperr.Internal(err)
		}
		if n == 0 {
			return Ticket{}, apperr.Invalid("截图已失效，请重新上传")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Ticket{}, apperr.Internal(err)
	}
	return s.detail(ctx, f)
}

func (s *Service) List(ctx context.Context, userID uuid.UUID, limit, offset int) ([]Ticket, error) {
	rows, err := s.Q.ListFeedback(ctx, db.ListFeedbackParams{UserID: userID, Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	out := make([]Ticket, 0, len(rows))
	for _, r := range rows {
		out = append(out, FromRow(r))
	}
	return out, nil
}

// Get returns one feedback with screenshots and replies, and marks the replies as read.
func (s *Service) Get(ctx context.Context, userID uuid.UUID, id string) (Ticket, error) {
	fid, err := uuid.Parse(id)
	if err != nil {
		return Ticket{}, apperr.NotFound("反馈不存在")
	}
	f, err := s.Q.GetFeedback(ctx, db.GetFeedbackParams{ID: fid, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Ticket{}, apperr.NotFound("反馈不存在")
	}
	if err != nil {
		return Ticket{}, apperr.Internal(err)
	}
	t, err := s.detail(ctx, f)
	if err != nil {
		return Ticket{}, err
	}
	if t.Unread {
		if err := s.Q.MarkFeedbackRead(ctx, db.MarkFeedbackReadParams{ID: fid, UserID: userID}); err != nil {
			return Ticket{}, apperr.Internal(err)
		}
	}
	return t, nil
}

func (s *Service) detail(ctx context.Context, f db.Feedback) (Ticket, error) {
	t := FromRow(f)
	atts, err := Attachments(ctx, s.Q, s.Blob, f.ID)
	if err != nil {
		return Ticket{}, err
	}
	t.Attachments = atts
	replies, err := s.Q.ListFeedbackReplies(ctx, f.ID)
	if err != nil {
		return Ticket{}, apperr.Internal(err)
	}
	t.Replies = make([]Reply, 0, len(replies))
	for _, r := range replies {
		t.Replies = append(t.Replies, Reply{ID: r.ID.String(), Content: r.Content, CreatedAt: r.CreatedAt})
	}
	return t, nil
}

// Attachments lists a feedback's screenshots with short-lived links; shared with adminapi.
func Attachments(ctx context.Context, q *db.Queries, store blob.Store, feedbackID uuid.UUID) ([]Attachment, error) {
	rows, err := q.ListFeedbackAttachments(ctx, &feedbackID)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	out := make([]Attachment, 0, len(rows))
	if store == nil {
		return out, nil
	}
	for _, a := range rows {
		u, _, err := store.SignedURL(ctx, a.BlobKey, linkTTL)
		if err != nil {
			return nil, apperr.Internal(err)
		}
		out = append(out, Attachment{ID: a.ID.String(), URL: u})
	}
	return out, nil
}
