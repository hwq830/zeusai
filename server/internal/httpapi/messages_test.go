package httpapi_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/notify"
	"github.com/hwq830/zeusai/server/internal/orch"
)

type messageT struct {
	ID         string `json:"id"`
	Category   string `json:"category"`
	Title      string `json:"title"`
	Deeplink   string `json:"deeplink"`
	CampaignID string `json:"campaignId"`
	Read       bool   `json:"read"`
}

type inboxT struct {
	Items       []messageT `json:"items"`
	UnreadCount int        `json:"unreadCount"`
}

func TestMessageCenter(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	h.login(randomPhone())
	var resp struct{ User struct{ ID string } }
	h.do("GET", "/v1/me", nil, 200, &resp)
	me := resp.User
	uid := uuid.MustParse(me.ID)
	if u, _ := h.q.GetUser(ctx, uid); u.LastSeenAt == nil {
		t.Fatal("last_seen_at not recorded")
	}
	today := time.Now().In(time.FixedZone("CST", 8*3600))
	if n, err := h.q.AnalyticsActiveUsers(ctx, db.AnalyticsActiveUsersParams{FromDate: today, ToDate: today}); err != nil || n < 1 {
		t.Fatalf("daily activity not recorded: %d %v", n, err)
	}

	// Preferences: defaults, then opt into marketing.
	var p map[string]any
	h.do("GET", "/v1/notification-preferences", nil, 200, &p)
	if p["marketing"] != false || p["briefing"] != true {
		t.Fatalf("default prefs = %v", p)
	}
	h.do("PUT", "/v1/notification-preferences", map[string]any{"briefing": true, "report": true, "marketing": false,
		"quietHours": map[string]any{"enabled": true, "start": "25:00", "end": "08:00"}}, 400, nil)
	h.do("PUT", "/v1/notification-preferences", map[string]any{"briefing": true, "report": false, "marketing": true,
		"quietHours": map[string]any{"enabled": false, "start": "23:00", "end": "07:30"}}, 200, &p)
	if p["report"] != false || p["marketing"] != true || p["quietHours"].(map[string]any)["end"] != "07:30" {
		t.Fatalf("prefs = %v", p)
	}
	if err := h.q.UpsertDevice(ctx, db.UpsertDeviceParams{PushToken: "tok-" + me.ID, UserID: uid, Platform: "ios", ApnsEnv: "sandbox", AppVersion: "1.3.0"}); err != nil {
		t.Fatal(err)
	}

	// An approved campaign targeting this user, sent by the orchestrator.
	aud, _ := json.Marshal(notify.Audience{Type: "users", UserIDs: []string{me.ID}})
	c, err := h.q.CreateCampaign(ctx, db.CreateCampaignParams{Name: "新模板", Category: "marketing", Title: "试试英文早报",
		Body: "全新模板上线", Deeplink: "zeus://templates", Audience: aud})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = h.q.SubmitCampaign(ctx, db.SubmitCampaignParams{ID: c.ID, AudienceEstimate: 1})
	_, _ = h.q.ApproveCampaign(ctx, db.ApproveCampaignParams{ID: c.ID})
	if err := h.orch.StartCampaign(ctx, orch.CampaignRun{CampaignID: c.ID}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		got, _ := h.q.GetCampaign(ctx, c.ID)
		if got.Status == "sent" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("campaign status = %s", got.Status)
		}
	}
	ns := h.pushes.Notifications()
	if len(ns) != 1 || ns[0].Title != "试试英文早报" || ns[0].Badge == nil || *ns[0].Badge != 1 {
		t.Fatalf("pushes = %+v", ns)
	}

	var inbox inboxT
	h.do("GET", "/v1/messages?category=marketing", nil, 200, &inbox)
	if len(inbox.Items) != 1 || inbox.Items[0].CampaignID != c.ID.String() || inbox.Items[0].Read || inbox.UnreadCount != 1 {
		t.Fatalf("inbox = %+v", inbox)
	}
	h.do("GET", "/v1/messages?category=report", nil, 200, &inbox)
	if len(inbox.Items) != 0 {
		t.Fatalf("report inbox = %+v", inbox)
	}
	h.do("GET", "/v1/messages?category=nope", nil, 400, nil)

	mid := ns[0].MessageID
	h.do("POST", "/v1/messages/"+mid+"/opened", nil, 204, nil)
	var unread struct{ Count int }
	h.do("GET", "/v1/messages/unread-count", nil, 200, &unread)
	if unread.Count != 0 {
		t.Fatalf("unread = %d", unread.Count)
	}
	st, _ := h.q.CampaignStats(ctx, &c.ID)
	if st.Pushed != 1 || st.Opened != 1 || st.Read != 1 {
		t.Fatalf("stats = %+v", st)
	}
	h.do("POST", "/v1/messages/read-all", nil, 204, nil)
	h.do("DELETE", "/v1/messages/"+mid, nil, 204, nil)
	h.do("DELETE", "/v1/messages/"+mid, nil, 404, nil)
	h.do("POST", "/v1/messages/"+uuid.NewString()+"/read", nil, 404, nil)
	h.do("GET", "/v1/messages", nil, 200, &inbox)
	if len(inbox.Items) != 0 {
		t.Fatalf("after delete = %+v", inbox)
	}
}
