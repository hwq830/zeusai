package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/briefing"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/events"
)

// readyBriefing stores a finished issue with one public and one private section.
func readyBriefing(t *testing.T, h *harness, userID string, extra string) db.Briefing {
	t.Helper()
	ctx := context.Background()
	uid := uuid.MustParse(userID)
	sub, err := h.q.CreateSubscription(ctx, db.CreateSubscriptionParams{
		UserID: uid, Name: "科技早报", FieldValues: []byte(`{}`), Topics: []string{"AI"},
		Schedule: []byte(`{"freq":"daily","time":"08:00","tz":"Asia/Shanghai"}`), Length: "standard",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := h.q.StartBriefing(ctx, db.StartBriefingParams{UserID: uid, SubscriptionID: sub.ID, Title: "科技早报", Date: time.Now(), Language: "zh"})
	if err != nil {
		t.Fatal(err)
	}
	src := []events.Source{{URL: "https://news.example/1", Title: "原文", SiteName: "新闻站"}}
	sections, _ := json.Marshal([]briefing.Section{
		{Key: "news", Title: "行业动态", Items: []briefing.Item{
			{ID: "n1", Title: "大模型发布", Summary: "**某公司**发布新模型。" + extra, Sources: src},
			{ID: "n2", Title: "芯片出货", Summary: "出货量创新高。", Sources: src},
		}},
		{Key: "weather", Title: "本地天气", Private: true, Items: []briefing.Item{
			{ID: "w1", Title: "朝阳区 晴", Summary: "18°C，适合出门。", Sources: src},
		}},
	})
	b, err = h.q.CompleteBriefing(ctx, db.CompleteBriefingParams{ID: b.ID, UserID: uid, Headline: "今天三件事", Sections: sections, ItemCount: 3, SourceCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type shareResp struct {
	ID             string     `json:"id"`
	URL            string     `json:"url"`
	Title          string     `json:"title"`
	Scope          string     `json:"scope"`
	TargetType     string     `json:"targetType"`
	TargetID       string     `json:"targetId"`
	Status         string     `json:"status"`
	ExpiresAt      *time.Time `json:"expiresAt"`
	ViewCount      int        `json:"viewCount"`
	HiddenSections *int       `json:"hiddenSections"`
}

func sharePath(u string) string { return u[strings.Index(u, "/s/"):] }

func TestBriefingShare(t *testing.T) {
	h := setup(t)
	s := h.login(randomPhone())
	b := readyBriefing(t, h, s.Me.User.ID, "")
	base := "/v1/briefings/" + b.ID.String()

	// Full share: default 30-day expiry, private section hidden from the page.
	var full shareResp
	h.do("POST", base+"/shares", map[string]any{"scope": "full", "channel": "wechat_session", "showName": true}, 201, &full)
	if full.TargetType != "briefing" || full.TargetID != b.ID.String() || full.Status != "active" || full.HiddenSections == nil || *full.HiddenSections != 1 {
		t.Fatalf("full share: %+v", full)
	}
	if full.ExpiresAt == nil || d(time.Until(*full.ExpiresAt)) != 30 {
		t.Fatalf("default expiry: %+v", full.ExpiresAt)
	}
	code, page := h.raw(sharePath(full.URL), false)
	if code != 200 || !strings.Contains(page, "<strong>某公司</strong>") || !strings.Contains(page, "芯片出货") ||
		!strings.Contains(page, "内容由 AI 生成") || !strings.Contains(page, "用同款模板生成我的简报") {
		t.Fatalf("full page %d: %.400s", code, page)
	}
	if strings.Contains(page, "朝阳区") || strings.Contains(page, "本地天气") {
		t.Fatal("private section leaked into the share page")
	}
	if !strings.Contains(page, "的分享") {
		t.Fatal("showName should print the sharer line")
	}

	// Summary share: titles only.
	var sum shareResp
	h.do("POST", base+"/shares", map[string]any{"scope": "summary", "expiresInDays": 0}, 201, &sum)
	if sum.ExpiresAt != nil {
		t.Fatalf("expiresInDays=0 should never expire: %+v", sum)
	}
	if _, page := h.raw(sharePath(sum.URL), false); !strings.Contains(page, "大模型发布") || strings.Contains(page, "某公司") || strings.Contains(page, "的分享") {
		t.Fatalf("summary page: %.400s", page)
	}

	// One item.
	var item shareResp
	h.do("POST", base+"/shares", map[string]any{"scope": "item", "itemId": "n2", "expiresInDays": 7}, 201, &item)
	if item.Title != "芯片出货" || item.ExpiresAt == nil || d(time.Until(*item.ExpiresAt)) != 7 {
		t.Fatalf("item share: %+v", item)
	}
	if _, page := h.raw(sharePath(item.URL), false); !strings.Contains(page, "出货量创新高") || strings.Contains(page, "大模型发布") || strings.Contains(page, "今天三件事") {
		t.Fatalf("item page: %.400s", page)
	}
	var e apiError
	h.do("POST", base+"/shares", map[string]any{"scope": "item", "itemId": "w1"}, 400, &e)
	h.do("POST", base+"/shares", map[string]any{"scope": "item", "itemId": "nope"}, 404, &e)
	h.do("POST", base+"/shares", map[string]any{"scope": "item"}, 400, &e)
	h.do("POST", base+"/shares", map[string]any{"scope": "full", "expiresInDays": 400}, 400, &e)

	// Views count each visitor once a day; the CTA counts and redirects.
	h.raw(sharePath(full.URL), false)
	var mine struct {
		Items []shareResp `json:"items"`
	}
	h.do("GET", "/v1/shares", nil, 200, &mine)
	if len(mine.Items) != 3 || mine.Items[0].ID != item.ID || mine.Items[2].ViewCount != 1 || mine.Items[2].Title != "科技早报" {
		t.Fatalf("my shares: %+v", mine.Items)
	}
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.Get(h.url + sharePath(full.URL) + "/go")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); resp.StatusCode != 302 || !strings.Contains(loc, "/app?") || !strings.Contains(loc, "from=share") {
		t.Fatalf("cta: %d %s", resp.StatusCode, loc)
	}
	clicks, err := h.q.GetShareByToken(context.Background(), strings.TrimPrefix(sharePath(full.URL), "/s/"))
	if err != nil || clicks.CtaClicks != 1 || clicks.Channel != "wechat_session" {
		t.Fatalf("cta click not counted: %+v %v", clicks, err)
	}

	// Expired links say so.
	if _, err := h.q.CreateShare(context.Background(), db.CreateShareParams{UserID: b.UserID, BriefingID: &b.ID, Token: "expired-tok", Scope: "full",
		ExpiresAt: ptrTime(time.Now().Add(-time.Minute))}); err != nil {
		t.Fatal(err)
	}
	if code, page := h.raw("/s/expired-tok", false); code != 404 || !strings.Contains(page, "分享已过期") {
		t.Fatalf("expired: %d %.200s", code, page)
	}

	// Another user can neither share this issue nor revoke these links.
	other := *h
	other.login(randomPhone())
	other.do("POST", base+"/shares", map[string]any{"scope": "full"}, 404, nil)
	other.do("DELETE", "/v1/shares/"+full.ID, nil, 404, nil)

	h.do("DELETE", "/v1/shares/"+full.ID, nil, 204, nil)
	if code, _ := h.raw(sharePath(full.URL), false); code != 404 {
		t.Fatalf("revoked: %d", code)
	}
	h.do("DELETE", "/v1/shares", nil, 204, nil)
	if code, _ := h.raw(sharePath(item.URL), false); code != 404 {
		t.Fatalf("revoke all: %d", code)
	}
	h.do("GET", "/v1/shares", nil, 200, &mine)
	for _, it := range mine.Items {
		if it.Status != "revoked" {
			t.Fatalf("after revoke all: %+v", it)
		}
	}
}

func TestBriefingShareModeration(t *testing.T) {
	h := setup(t)
	s := h.login(randomPhone())
	b := readyBriefing(t, h, s.Me.User.ID, blockedTerm)
	var e apiError
	h.do("POST", "/v1/briefings/"+b.ID.String()+"/shares", map[string]any{"scope": "full"}, 403, &e)
	// The private item and the other item are still shareable on their own.
	h.do("POST", "/v1/briefings/"+b.ID.String()+"/shares", map[string]any{"scope": "item", "itemId": "n2"}, 201, nil)
}

func TestBriefingPDFAndHTML(t *testing.T) {
	h := setup(t)
	s := h.login(randomPhone())
	b := readyBriefing(t, h, s.Me.User.ID, "")
	base := "/v1/briefings/" + b.ID.String()

	// The owner's copy keeps every section.
	code, page := h.raw(base+"/html", true)
	if code != 200 || !strings.Contains(page, "朝阳区") || strings.Contains(page, `class="cta"`) {
		t.Fatalf("html %d: %.300s", code, page)
	}

	var link struct {
		Status string `json:"status"`
		URL    string `json:"url"`
	}
	h.do("GET", base+"/pdf", nil, 200, &link)
	if link.Status != "generating" {
		t.Fatalf("first request: %+v", link)
	}
	for deadline := time.Now().Add(20 * time.Second); link.Status != "ready" && time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		h.do("GET", base+"/pdf", nil, 200, &link)
	}
	if link.Status != "ready" {
		t.Fatalf("PDF never ready: %+v", link)
	}
	code, body := h.raw(strings.TrimPrefix(link.URL, h.url), false)
	if code != 200 || !strings.HasPrefix(body, "%PDF") {
		t.Fatalf("download: %d %.40q", code, body)
	}
}

func TestReportShareExpiry(t *testing.T) {
	h := setup(t)
	h.login(randomPhone())
	var tk struct{ ID, ReportID string }
	h.do("POST", "/v1/tasks", map[string]any{"kind": "report", "topic": "分享有效期", "depth": "quick"}, 202, &tk)
	h.streamEvents(tk.ID, 0)
	var sh shareResp
	h.do("POST", "/v1/reports/"+tk.ReportID+"/shares", map[string]any{"scope": "full"}, 201, &sh)
	if sh.TargetType != "report" || sh.ExpiresAt == nil || d(time.Until(*sh.ExpiresAt)) != 30 {
		t.Fatalf("report share: %+v", sh)
	}
	var e apiError
	h.do("POST", "/v1/reports/"+tk.ReportID+"/shares", map[string]any{"scope": "item", "itemId": "x"}, 400, &e)
}

// d rounds a duration to whole days.
func d(x time.Duration) int { return int((x + 12*time.Hour) / (24 * time.Hour)) }

func ptrTime(t time.Time) *time.Time { return &t }
