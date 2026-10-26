// Package share owns public H5 links to reports and briefings: creating them (scope, expiry,
// daily limit, moderation), listing and revoking them, and serving /s/{token}.
package share

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/briefing"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/moderation"
	"github.com/hwq830/zeusai/server/internal/remoteconfig"
	"github.com/hwq830/zeusai/server/internal/report"
	briefingtemplate "github.com/hwq830/zeusai/web/briefing-template"
	reporttemplate "github.com/hwq830/zeusai/web/report-template"
)

const (
	ScopeFull    = "full"
	ScopeSummary = "summary"
	ScopeItem    = "item"

	maxExpiryDays = 365
)

type Service struct {
	Q   *db.Queries
	Mod moderation.Checker // nil allows everything (dev)
	// Config carries the kill switch, default expiry, daily limit and CTA link; nil uses defaults.
	Config        *remoteconfig.Cache
	PublicBaseURL string
	Now           func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Request is ShareRequest in the contract.
type Request struct {
	Scope  string `json:"scope"`
	ItemID string `json:"itemId,omitempty"`
	// ExpiresInDays nil takes the configured default; 0 never expires.
	ExpiresInDays *int   `json:"expiresInDays,omitempty"`
	Channel       string `json:"channel,omitempty"`
	ShowName      bool   `json:"showName,omitempty"`
}

// Share is Share in the contract.
type Share struct {
	ID             string     `json:"id"`
	URL            string     `json:"url"`
	Title          string     `json:"title"`
	Summary        string     `json:"summary,omitempty"`
	Scope          string     `json:"scope"`
	TargetType     string     `json:"targetType"`
	TargetID       string     `json:"targetId"`
	ItemID         string     `json:"itemId,omitempty"`
	ShowName       bool       `json:"showName"`
	Status         string     `json:"status"`
	ExpiresAt      *time.Time `json:"expiresAt,omitempty"`
	ViewCount      int        `json:"viewCount"`
	HiddenSections *int       `json:"hiddenSections,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	RevokedAt      *time.Time `json:"revokedAt,omitempty"`
}

func (s *Service) url(token string) string {
	return strings.TrimRight(s.PublicBaseURL, "/") + "/s/" + token
}

func status(sh db.Share, now time.Time) string {
	switch {
	case sh.RevokedAt != nil:
		return "revoked"
	case sh.ExpiresAt != nil && !now.Before(*sh.ExpiresAt):
		return "expired"
	}
	return "active"
}

func (s *Service) fromRow(sh db.Share, title, summary string) Share {
	out := Share{
		ID: sh.ID.String(), URL: s.url(sh.Token), Title: title, Summary: summary, Scope: sh.Scope,
		ItemID: sh.ItemID, ShowName: sh.ShowName, Status: status(sh, s.now()), ExpiresAt: sh.ExpiresAt,
		ViewCount: int(sh.ViewCount), CreatedAt: sh.CreatedAt, RevokedAt: sh.RevokedAt,
	}
	if sh.ReportID != nil {
		out.TargetType, out.TargetID = "report", sh.ReportID.String()
	} else if sh.BriefingID != nil {
		out.TargetType, out.TargetID = "briefing", sh.BriefingID.String()
	}
	return out
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// check applies the kill switch, the daily limit and the expiry choice shared by every link.
func (s *Service) check(ctx context.Context, userID uuid.UUID, req Request) (*time.Time, error) {
	rc := remoteconfig.Context{UserID: userID.String()}
	if !s.Config.Bool(rc, remoteconfig.KeyShareEnabled, true) {
		return nil, apperr.Unavailable("分享功能暂时不可用，请稍后再试")
	}
	if utf8.RuneCountInString(req.Channel) > 32 {
		return nil, apperr.Invalid("参数不正确")
	}
	days := s.Config.Int(rc, remoteconfig.KeyShareExpiryDays, 30)
	if req.ExpiresInDays != nil {
		days = *req.ExpiresInDays
	}
	if days < 0 || days > maxExpiryDays {
		return nil, apperr.Invalid("有效期应为 0（永久）到 365 天")
	}
	if limit := s.Config.Int(rc, remoteconfig.KeyShareDailyLimit, 50); limit > 0 {
		n, err := s.Q.CountSharesSince(ctx, db.CountSharesSinceParams{UserID: userID, CreatedAt: s.now().Add(-24 * time.Hour)})
		if err != nil {
			return nil, apperr.Internal(err)
		}
		if int(n) >= limit {
			return nil, apperr.RateLimited("今天分享次数太多了，明天再试吧")
		}
	}
	if days == 0 {
		return nil, nil
	}
	exp := s.now().Add(time.Duration(days) * 24 * time.Hour)
	return &exp, nil
}

// review runs model output through moderation once more before it becomes public.
func (s *Service) review(ctx context.Context, texts ...string) error {
	if s.Mod == nil {
		return nil
	}
	for _, t := range texts {
		if strings.TrimSpace(t) == "" {
			continue
		}
		v, err := s.Mod.Check(ctx, moderation.Output, t)
		if err != nil {
			return apperr.Unavailable("内容审核服务暂时不可用，请稍后再试").WithCause(err)
		}
		if v.Blocked {
			return apperr.Forbidden("这份内容暂时不能公开分享")
		}
	}
	return nil
}

func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func parseID(id, notFound string) (uuid.UUID, error) {
	u, err := uuid.Parse(id)
	if err != nil {
		return uuid.Nil, apperr.NotFound(notFound)
	}
	return u, nil
}

// CreateForReport makes a link to a finished report; scope is full or summary.
func (s *Service) CreateForReport(ctx context.Context, userID uuid.UUID, reportID string, req Request) (Share, error) {
	if req.Scope != ScopeFull && req.Scope != ScopeSummary {
		return Share{}, apperr.Invalid("scope 应为 full / summary")
	}
	rid, err := parseID(reportID, "报告不存在")
	if err != nil {
		return Share{}, err
	}
	r, err := s.Q.GetReport(ctx, db.GetReportParams{ID: rid, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Share{}, apperr.NotFound("报告不存在")
	}
	if err != nil {
		return Share{}, apperr.Internal(err)
	}
	if r.Status != "done" {
		return Share{}, apperr.Conflict("报告生成完后才能分享")
	}
	exp, err := s.check(ctx, userID, req)
	if err != nil {
		return Share{}, err
	}
	if err := s.review(ctx, r.Title, r.Summary); err != nil {
		return Share{}, err
	}
	sh, err := s.Q.CreateShare(ctx, db.CreateShareParams{
		UserID: userID, ReportID: &r.ID, Token: newToken(), Scope: req.Scope, ExpiresAt: exp,
		Channel: req.Channel, ShowName: req.ShowName,
	})
	if err != nil {
		return Share{}, apperr.Internal(err)
	}
	return s.fromRow(sh, r.Title, r.Summary), nil
}

// View is what a briefing link shows: private sections removed, then narrowed to the scope.
type View struct {
	Briefing briefing.Briefing
	Hidden   int             // private sections left out
	Item     *briefing.Item // scope=item
}

// BriefingView narrows an issue to what a link with this scope may show.
func BriefingView(b briefing.Briefing, scope, itemID string) (View, error) {
	v := View{Briefing: b}
	v.Briefing.Sections = nil
	for _, sec := range b.Sections {
		if sec.Private {
			v.Hidden++
			for _, it := range sec.Items {
				if scope == ScopeItem && it.ID == itemID {
					return View{}, apperr.Invalid("这一条含个人信息，不能分享")
				}
			}
			continue
		}
		switch scope {
		case ScopeItem:
			for _, it := range sec.Items {
				if it.ID == itemID {
					it := it
					v.Item = &it
					v.Briefing.Sections = []briefing.Section{{Key: sec.Key, Title: sec.Title, Items: []briefing.Item{it}}}
				}
			}
		case ScopeSummary:
			out := briefing.Section{Key: sec.Key, Title: sec.Title}
			for _, it := range sec.Items {
				out.Items = append(out.Items, briefing.Item{ID: it.ID, Title: it.Title})
			}
			v.Briefing.Sections = append(v.Briefing.Sections, out)
		default:
			v.Briefing.Sections = append(v.Briefing.Sections, sec)
		}
	}
	if scope == ScopeItem {
		if v.Item == nil {
			return View{}, apperr.NotFound("这一条不存在")
		}
		v.Briefing.Headline = ""
	}
	if len(v.Briefing.Sections) == 0 {
		return View{}, apperr.Conflict("这份简报都是个人内容，不能分享")
	}
	return v, nil
}

func (s *Service) readyBriefing(ctx context.Context, id, userID uuid.UUID) (db.Briefing, error) {
	r, err := s.Q.GetBriefing(ctx, db.GetBriefingParams{ID: id, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Briefing{}, apperr.NotFound("简报不存在")
	}
	if err != nil {
		return db.Briefing{}, apperr.Internal(err)
	}
	if r.Status != "ready" {
		return db.Briefing{}, apperr.Conflict("简报生成完后才能分享")
	}
	return r, nil
}

// CreateForBriefing makes a link to a finished issue, or to one item of it.
func (s *Service) CreateForBriefing(ctx context.Context, userID uuid.UUID, briefingID string, req Request) (Share, error) {
	switch req.Scope {
	case ScopeFull, ScopeSummary:
		req.ItemID = ""
	case ScopeItem:
		if req.ItemID == "" {
			return Share{}, apperr.Invalid("单条分享需要 itemId")
		}
	default:
		return Share{}, apperr.Invalid("scope 应为 full / summary / item")
	}
	bid, err := parseID(briefingID, "简报不存在")
	if err != nil {
		return Share{}, err
	}
	row, err := s.readyBriefing(ctx, bid, userID)
	if err != nil {
		return Share{}, err
	}
	v, err := BriefingView(briefing.IssueFromRow(row), req.Scope, req.ItemID)
	if err != nil {
		return Share{}, err
	}
	exp, err := s.check(ctx, userID, req)
	if err != nil {
		return Share{}, err
	}
	texts := []string{v.Briefing.Title, v.Briefing.Headline}
	for _, sec := range v.Briefing.Sections {
		for _, it := range sec.Items {
			texts = append(texts, it.Title+"\n"+it.Summary)
		}
	}
	if err := s.review(ctx, texts...); err != nil {
		return Share{}, err
	}
	sh, err := s.Q.CreateShare(ctx, db.CreateShareParams{
		UserID: userID, BriefingID: &row.ID, ItemID: req.ItemID, Token: newToken(), Scope: req.Scope,
		ExpiresAt: exp, Channel: req.Channel, ShowName: req.ShowName,
	})
	if err != nil {
		return Share{}, apperr.Internal(err)
	}
	title, summary := row.Title, row.Headline
	if v.Item != nil {
		title, summary = v.Item.Title, clip(plain(v.Item.Summary), 80)
	}
	out := s.fromRow(sh, title, summary)
	out.HiddenSections = &v.Hidden
	return out, nil
}

// plain drops the Markdown marks briefing summaries use (bold, links, list bullets).
func plain(md string) string {
	r := strings.NewReplacer("**", "", "__", "", "`", "", "\n- ", " ", "\n* ", " ", "\n", " ")
	s := r.Replace(md)
	// [text](url) → text
	for {
		i := strings.Index(s, "](")
		if i < 0 {
			break
		}
		j := strings.Index(s[i:], ")")
		k := strings.LastIndex(s[:i], "[")
		if j < 0 || k < 0 {
			break
		}
		s = s[:k] + s[k+1:i] + s[i+j+1:]
	}
	return strings.TrimSpace(s)
}

// List returns the user's links, newest first, including expired and revoked ones.
func (s *Service) List(ctx context.Context, userID uuid.UUID, limit, offset int) ([]Share, error) {
	rows, err := s.Q.ListShares(ctx, db.ListSharesParams{UserID: userID, Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	out := make([]Share, 0, len(rows))
	for _, r := range rows {
		sh := db.Share{
			ID: r.ID, UserID: r.UserID, ReportID: r.ReportID, Token: r.Token, Scope: r.Scope, ViewCount: r.ViewCount,
			CreatedAt: r.CreatedAt, RevokedAt: r.RevokedAt, BriefingID: r.BriefingID, ItemID: r.ItemID,
			ExpiresAt: r.ExpiresAt, Channel: r.Channel, ShowName: r.ShowName,
		}
		out = append(out, s.fromRow(sh, r.TargetTitle, r.TargetSummary))
	}
	return out, nil
}

func (s *Service) Revoke(ctx context.Context, userID uuid.UUID, id string) error {
	sid, err := parseID(id, "分享不存在")
	if err != nil {
		return err
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

func (s *Service) RevokeAll(ctx context.Context, userID uuid.UUID) error {
	if _, err := s.Q.RevokeAllShares(ctx, userID); err != nil {
		return apperr.Internal(err)
	}
	return nil
}

// Visitor identifies a page view well enough to count each person once a day.
type Visitor struct {
	IP        string
	UserAgent string
}

func (v Visitor) key() string {
	h := sha256.Sum256([]byte(v.IP + "\x00" + v.UserAgent))
	return hex.EncodeToString(h[:12])
}

func (v Visitor) inWechat() bool { return strings.Contains(v.UserAgent, "MicroMessenger") }

func (s *Service) live(ctx context.Context, token string) (db.Share, error) {
	sh, err := s.Q.GetShareByToken(ctx, token)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Share{}, apperr.NotFound("分享已失效")
	}
	if err != nil {
		return db.Share{}, apperr.Internal(err)
	}
	if status(sh, s.now()) == "expired" {
		return db.Share{}, apperr.NotFound("分享已过期")
	}
	return sh, nil
}

// Page renders the public H5 page for a share token and counts the visit.
func (s *Service) Page(ctx context.Context, token string, v Visitor) ([]byte, error) {
	sh, err := s.live(ctx, token)
	if err != nil {
		return nil, err
	}
	sharedBy := ""
	if sh.ShowName {
		if u, err := s.Q.GetUser(ctx, sh.UserID); err == nil {
			sharedBy = u.Nickname
		}
	}
	cta := s.url(token) + "/go"
	var page []byte
	switch {
	case sh.ReportID != nil:
		r, err := s.Q.GetReport(ctx, db.GetReportParams{ID: *sh.ReportID, UserID: sh.UserID})
		if err != nil || r.Status != "done" {
			return nil, apperr.NotFound("分享已失效")
		}
		d := report.TemplateData(r, sh.Scope == ScopeFull)
		d.Share, d.SharedBy, d.AppURL = true, sharedBy, cta
		page, err = reporttemplate.Render(d)
		if err != nil {
			return nil, apperr.Internal(err)
		}
	case sh.BriefingID != nil:
		r, err := s.Q.GetBriefing(ctx, db.GetBriefingParams{ID: *sh.BriefingID, UserID: sh.UserID})
		if err != nil || r.Status != "ready" {
			return nil, apperr.NotFound("分享已失效")
		}
		view, err := BriefingView(briefing.IssueFromRow(r), sh.Scope, sh.ItemID)
		if err != nil {
			return nil, apperr.NotFound("分享已失效")
		}
		d := briefing.TemplateData(view.Briefing)
		d.Share, d.SharedBy, d.CTAURL = true, sharedBy, cta
		page, err = briefingtemplate.Render(d)
		if err != nil {
			return nil, apperr.Internal(err)
		}
	default:
		return nil, apperr.NotFound("分享已失效")
	}
	_ = s.Q.RecordShareView(ctx, db.RecordShareViewParams{
		ShareID: sh.ID, Visitor: v.key(), ViewDate: s.now().In(cst), InWechat: v.inWechat(),
	})
	return page, nil
}

var cst = time.FixedZone("CST", 8*3600)

// CTA counts a tap on the page's "make your own" button and returns where it leads: the
// configured landing page (default {public_base_url}/app) with from=share, t={token} and, for a
// briefing made from an official template, template={id}.
func (s *Service) CTA(ctx context.Context, token string) (string, error) {
	base := s.Config.String(remoteconfig.Context{}, remoteconfig.KeyShareCTAURL, "")
	if base == "" {
		base = strings.TrimRight(s.PublicBaseURL, "/") + "/app"
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", apperr.Internal(err)
	}
	q := u.Query()
	q.Set("from", "share")
	sh, err := s.Q.GetShareByToken(ctx, token)
	if err == nil {
		q.Set("t", token)
		_ = s.Q.ClickShareCTA(ctx, sh.ID)
		if sh.BriefingID != nil {
			if tid := s.officialTemplate(ctx, sh); tid != "" {
				q.Set("template", tid)
			}
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (s *Service) officialTemplate(ctx context.Context, sh db.Share) string {
	b, err := s.Q.GetBriefing(ctx, db.GetBriefingParams{ID: *sh.BriefingID, UserID: sh.UserID})
	if err != nil {
		return ""
	}
	sub, err := s.Q.GetSubscription(ctx, db.GetSubscriptionParams{ID: b.SubscriptionID, UserID: sh.UserID})
	if err != nil || sub.TemplateID == nil {
		return ""
	}
	// A nil owner only matches official templates; a custom one is private to its owner.
	t, err := s.Q.GetTemplateForUser(ctx, db.GetTemplateForUserParams{ID: *sub.TemplateID})
	if err != nil {
		return ""
	}
	return t.ID.String()
}
