package notify

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/i18n"
	"github.com/hwq830/zeusai/server/internal/push"
)

const (
	MaxAudienceUserIDs = 10000
	CampaignBatchSize  = 500
)

// Audience is a campaign's targeting rule, stored as campaigns.audience.
//   - all: every user who is not suspended
//   - users: exactly UserIDs
//   - filter: users matching every set condition
type Audience struct {
	Type             string     `json:"type"`
	UserIDs          []string   `json:"userIds,omitempty"`
	RegisteredAfter  *time.Time `json:"registeredAfter,omitempty"`
	RegisteredBefore *time.Time `json:"registeredBefore,omitempty"`
	// Last opened the app within / not within this many days.
	ActiveWithinDays int    `json:"activeWithinDays,omitempty"`
	InactiveForDays  int    `json:"inactiveForDays,omitempty"`
	MinAppVersion    string `json:"minAppVersion,omitempty"`
	// Has an active briefing subscription made from this template.
	TemplateID string `json:"templateId,omitempty"`
	// Display language zh or en; empty matches everyone.
	Language string `json:"language,omitempty"`
	// Has a push device on ios or android; MinAppVersion is then compared on that platform's devices.
	Platform string `json:"platform,omitempty"`
}

func (a Audience) Validate() error {
	switch a.Type {
	case "all":
	case "users":
		if len(a.UserIDs) == 0 || len(a.UserIDs) > MaxAudienceUserIDs {
			return apperr.Invalid("用户 ID 列表需要 1–10000 个")
		}
		for _, id := range a.UserIDs {
			if _, err := uuid.Parse(id); err != nil {
				return apperr.Invalid("用户 ID 格式不正确：" + id)
			}
		}
	case "filter":
		if a.ActiveWithinDays < 0 || a.InactiveForDays < 0 || a.ActiveWithinDays > 365 || a.InactiveForDays > 365 {
			return apperr.Invalid("活跃天数需在 0–365 之间")
		}
		if a.MinAppVersion != "" {
			if _, ok := parseVersion(a.MinAppVersion); !ok {
				return apperr.Invalid("App 版本格式应为 1.2.3")
			}
		}
		if a.TemplateID != "" {
			if _, err := uuid.Parse(a.TemplateID); err != nil {
				return apperr.Invalid("模板 ID 格式不正确")
			}
		}
		if a.Language != "" && !i18n.Valid(a.Language) {
			return apperr.Invalid("语言只能是 zh、zh-Hant 或 en")
		}
		if a.Platform != "" && a.Platform != "ios" && a.Platform != "android" {
			return apperr.Invalid("平台只能是 ios 或 android")
		}
	default:
		return apperr.Invalid("目标人群类型不正确")
	}
	return nil
}

func parseVersion(v string) ([]int32, bool) {
	parts := strings.Split(v, ".")
	out := make([]int32, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		out[i] = int32(n)
	}
	return out, true
}

func (a Audience) params(now time.Time) db.CampaignAudiencePageParams {
	var p db.CampaignAudiencePageParams
	switch a.Type {
	case "users":
		p.UserIds = make([]uuid.UUID, 0, len(a.UserIDs))
		for _, id := range a.UserIDs {
			p.UserIds = append(p.UserIds, uuid.MustParse(id))
		}
	case "filter":
		p.RegisteredAfter, p.RegisteredBefore = a.RegisteredAfter, a.RegisteredBefore
		if a.ActiveWithinDays > 0 {
			p.SeenAfter = ptr(now.AddDate(0, 0, -a.ActiveWithinDays))
		}
		if a.InactiveForDays > 0 {
			p.NotSeenAfter = ptr(now.AddDate(0, 0, -a.InactiveForDays))
		}
		if a.MinAppVersion != "" {
			p.MinAppVersion, _ = parseVersion(a.MinAppVersion)
		}
		if a.TemplateID != "" {
			p.TemplateID = ptr(uuid.MustParse(a.TemplateID))
		}
		if a.Language != "" {
			p.UiLanguage = ptr(a.Language)
		}
		if a.Platform != "" {
			p.Platform = ptr(a.Platform)
		}
	}
	return p
}

func (s *Service) EstimateAudience(ctx context.Context, a Audience) (int, error) {
	if err := a.Validate(); err != nil {
		return 0, err
	}
	p := a.params(s.now())
	n, err := s.Q.CountCampaignAudience(ctx, db.CountCampaignAudienceParams{
		UserIds: p.UserIds, RegisteredAfter: p.RegisteredAfter, RegisteredBefore: p.RegisteredBefore, SeenAfter: p.SeenAfter,
		NotSeenAfter: p.NotSeenAfter, MinAppVersion: p.MinAppVersion, TemplateID: p.TemplateID, UiLanguage: p.UiLanguage, Platform: p.Platform,
	})
	if err != nil {
		return 0, apperr.Internal(err)
	}
	return int(n), nil
}

// Content is what an operator writes for a campaign.
type Content struct {
	Name     string
	Category string
	Title    string
	Body     string
	// Optional English push for English-language users.
	TitleEn  string
	BodyEn   string
	ImageURL string
	DeepLink string
}

func (c Content) Validate() error {
	if n := utf8.RuneCountInString(strings.TrimSpace(c.Name)); n == 0 || n > 60 {
		return apperr.Invalid("活动名称需在 1–60 字之间")
	}
	if c.Category != push.CategoryMarketing && c.Category != push.CategoryService {
		return apperr.Invalid("活动类别只能是运营或服务通知")
	}
	if n := utf8.RuneCountInString(strings.TrimSpace(c.Title)); n == 0 || n > 40 {
		return apperr.Invalid("推送标题需在 1–40 字之间")
	}
	if utf8.RuneCountInString(c.Body) > 200 {
		return apperr.Invalid("推送正文不能超过 200 字")
	}
	if utf8.RuneCountInString(c.TitleEn) > 80 {
		return apperr.Invalid("英文标题不能超过 80 个字符")
	}
	if utf8.RuneCountInString(c.BodyEn) > 400 {
		return apperr.Invalid("英文正文不能超过 400 个字符")
	}
	if strings.TrimSpace(c.TitleEn) == "" && strings.TrimSpace(c.BodyEn) != "" {
		return apperr.Invalid("填写英文正文时也要填写英文标题")
	}
	if c.ImageURL != "" && !strings.HasPrefix(c.ImageURL, "https://") {
		return apperr.Invalid("图片地址必须是 https 链接")
	}
	if !ValidDeepLink(c.DeepLink) {
		return apperr.Invalid("跳转链接不支持，请使用 zeus:// 页面或 https 链接")
	}
	return nil
}

func ParseAudience(raw []byte) (Audience, error) {
	var a Audience
	if err := json.Unmarshal(raw, &a); err != nil {
		return a, err
	}
	return a, nil
}

// StartCampaign moves an approved campaign to sending; false means it was cancelled meanwhile.
func (s *Service) StartCampaign(ctx context.Context, id uuid.UUID) (bool, error) {
	_, err := s.Q.StartCampaignSending(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Service) FinishCampaign(ctx context.Context, id uuid.UUID) error {
	return s.Q.FinishCampaign(ctx, id)
}

// SendCampaignBatch sends to the next page of the audience after the given user id. It stops
// (done) when the audience is exhausted or the campaign is no longer sending (cancelled).
// Retrying a batch is safe: a user the campaign already reached is skipped.
func (s *Service) SendCampaignBatch(ctx context.Context, id, after uuid.UUID) (next uuid.UUID, done bool, err error) {
	c, err := s.Q.GetCampaign(ctx, id)
	if err != nil {
		return after, false, err
	}
	if c.Status != "sending" {
		return after, true, nil
	}
	a, err := ParseAudience(c.Audience)
	if err != nil {
		return after, false, err
	}
	p := a.params(s.now())
	p.AfterID, p.PageSize = after, CampaignBatchSize
	ids, err := s.Q.CampaignAudiencePage(ctx, p)
	if err != nil {
		return after, false, err
	}
	for _, row := range ids {
		if err := ctx.Err(); err != nil {
			return after, false, err
		}
		uid := row.ID
		title, body := c.Title, c.Body
		switch l := i18n.Parse(row.UiLanguage); {
		case l == i18n.EN && c.TitleEn != "":
			title, body = c.TitleEn, c.BodyEn
		case l == i18n.ZHHant:
			title, body = i18n.Hant(title), i18n.Hant(body)
		}
		m, err := s.Q.CreateCampaignMessage(ctx, db.CreateCampaignMessageParams{
			UserID: uid, Category: c.Category, Title: title, Body: body, ImageUrl: c.ImageUrl, Deeplink: c.Deeplink, CampaignID: &c.ID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			after = uid
			continue
		}
		if err != nil {
			return after, false, err
		}
		s.deliver(ctx, m, false)
		after = uid
	}
	return after, len(ids) < CampaignBatchSize, nil
}
