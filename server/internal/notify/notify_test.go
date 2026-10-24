package notify_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/notify"
	"github.com/hwq830/zeusai/server/internal/platform/testdb"
	"github.com/hwq830/zeusai/server/internal/push"
)

func TestQuietUntil(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	at := func(h, m int) time.Time { return time.Date(2026, 10, 24, h, m, 0, 0, sh) }
	cases := []struct {
		now        time.Time
		start, end int
		quiet      bool
		until      time.Time
	}{
		{at(23, 30), 22 * 60, 8 * 60, true, time.Date(2026, 10, 25, 8, 0, 0, 0, sh)},
		{at(6, 0), 22 * 60, 8 * 60, true, at(8, 0)},
		{at(8, 0), 22 * 60, 8 * 60, false, time.Time{}},
		{at(12, 0), 22 * 60, 8 * 60, false, time.Time{}},
		{at(13, 0), 12 * 60, 14 * 60, true, at(14, 0)},
		{at(13, 0), 600, 600, false, time.Time{}},
	}
	for _, c := range cases {
		until, quiet := notify.QuietUntil(c.now.UTC(), sh, c.start, c.end)
		if quiet != c.quiet || !until.Equal(c.until) {
			t.Errorf("QuietUntil(%v, %d, %d) = %v %v, want %v %v", c.now, c.start, c.end, until, quiet, c.until, c.quiet)
		}
	}
}

func TestValidDeepLink(t *testing.T) {
	for l, want := range map[string]bool{
		"": true, "zeus://report/1": true, "zeus://messages": true, "zeus://report/": false,
		"https://zeus.ai/x": true, "http://zeus.ai": false, "javascript:alert(1)": false, "zeus://unknown": false,
	} {
		if notify.ValidDeepLink(l) != want {
			t.Errorf("ValidDeepLink(%q) != %v", l, want)
		}
	}
}

type env struct {
	t    *testing.T
	q    *db.Queries
	svc  *notify.Service
	sent *push.Log
	now  time.Time
}

// noon Beijing time, outside the default quiet hours.
var noon = time.Date(2026, 10, 24, 4, 0, 0, 0, time.UTC)

func setup(t *testing.T) *env {
	q := db.New(testdb.New(t))
	e := &env{t: t, q: q, sent: &push.Log{}, now: noon}
	e.svc = &notify.Service{Q: q, Sender: e.sent, Now: func() time.Time { return e.now }}
	return e
}

func (e *env) user() db.User {
	e.t.Helper()
	u, err := e.q.CreateUser(context.Background(), db.CreateUserParams{Nickname: "u", Timezone: "Asia/Shanghai"})
	if err != nil {
		e.t.Fatal(err)
	}
	return u
}

func (e *env) messages(u uuid.UUID) []db.Message {
	e.t.Helper()
	ms, err := e.q.ListMessages(context.Background(), db.ListMessagesParams{UserID: u, Limit: 100})
	if err != nil {
		e.t.Fatal(err)
	}
	return ms
}

func TestNotifyRoutesThroughPrefs(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	u := e.user()

	// Defaults: report pushes, with badge = unread count and the message id in the payload.
	if err := e.svc.Notify(ctx, u.ID, push.Notification{Category: push.CategoryReport, Title: "报告已完成", DeepLink: "zeus://report/1"}); err != nil {
		t.Fatal(err)
	}
	ms := e.messages(u.ID)
	if len(ms) != 1 || ms[0].PushStatus != "sent" || ms[0].PushedAt == nil {
		t.Fatalf("messages = %+v", ms)
	}
	got := e.sent.Notifications()
	if len(got) != 1 || got[0].MessageID != ms[0].ID.String() || got[0].Badge == nil || *got[0].Badge != 1 {
		t.Fatalf("pushes = %+v", got)
	}

	// Marketing is off by default: saved, not pushed.
	_ = e.svc.Notify(ctx, u.ID, push.Notification{Category: push.CategoryMarketing, Title: "新模板上线"})
	if ms := e.messages(u.ID); ms[0].PushStatus != "off" || len(e.sent.Notifications()) != 1 {
		t.Fatalf("marketing default = %s", ms[0].PushStatus)
	}

	// Turn report pushes off and marketing on.
	p, _ := e.svc.Prefs(ctx, u.ID)
	if !p.Briefing || !p.Report || p.Marketing || p.QuietHours.Start != "22:00" || p.QuietHours.End != "08:00" {
		t.Fatalf("default prefs = %+v", p)
	}
	p.Report, p.Marketing = false, true
	if _, err := e.svc.UpdatePrefs(ctx, u.ID, p); err != nil {
		t.Fatal(err)
	}
	row, _ := e.q.GetNotificationPrefs(ctx, u.ID)
	if row.MarketingOptInAt == nil {
		t.Fatal("marketing opt-in time not recorded")
	}
	_ = e.svc.Notify(ctx, u.ID, push.Notification{Category: push.CategoryReport, Title: "报告2"})
	if ms := e.messages(u.ID); ms[0].PushStatus != "off" {
		t.Fatalf("report off = %s", ms[0].PushStatus)
	}

	// Marketing cap: 1 per day.
	_ = e.svc.Notify(ctx, u.ID, push.Notification{Category: push.CategoryMarketing, Title: "活动1"})
	_ = e.svc.Notify(ctx, u.ID, push.Notification{Category: push.CategoryMarketing, Title: "活动2"})
	ms = e.messages(u.ID)
	if ms[1].PushStatus != "sent" || ms[0].PushStatus != "capped" {
		t.Fatalf("cap: %s %s", ms[1].PushStatus, ms[0].PushStatus)
	}

	// Quiet hours: service is deferred to 08:00, briefing is not.
	e.now = time.Date(2026, 10, 24, 15, 0, 0, 0, time.UTC) // 23:00 Beijing
	_ = e.svc.Notify(ctx, u.ID, push.Notification{Category: push.CategoryBriefing, Title: "早报"})
	_ = e.svc.Notify(ctx, u.ID, push.Notification{Title: "你的反馈有新回复"})
	ms = e.messages(u.ID)
	if ms[1].PushStatus != "sent" || ms[0].PushStatus != "deferred" || ms[0].Category != "service" {
		t.Fatalf("quiet: briefing %s service %s", ms[1].PushStatus, ms[0].PushStatus)
	}
	if want := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC); !ms[0].PushAfter.Equal(want) {
		t.Fatalf("push_after = %v, want %v", ms[0].PushAfter, want)
	}
	before := len(e.sent.Notifications())
	if n, _ := e.svc.FlushDeferred(ctx); n != 0 {
		t.Fatalf("flushed %d before due", n)
	}
	e.now = time.Date(2026, 10, 25, 0, 1, 0, 0, time.UTC)
	if n, _ := e.svc.FlushDeferred(ctx); n != 1 {
		t.Fatalf("flushed %d", n)
	}
	if ms := e.messages(u.ID); ms[0].PushStatus != "sent" || len(e.sent.Notifications()) != before+1 {
		t.Fatalf("after flush = %s", ms[0].PushStatus)
	}

	// Inbox operations.
	unread, _ := e.svc.UnreadCount(ctx, u.ID)
	if unread != 7 {
		t.Fatalf("unread = %d", unread)
	}
	list, _ := e.svc.List(ctx, u.ID, []string{"marketing"}, 20, 0)
	if len(list) != 3 {
		t.Fatalf("marketing list = %d", len(list))
	}
	if err := e.svc.MarkOpened(ctx, u.ID, list[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.MarkAllRead(ctx, u.ID, []string{"marketing"}); err != nil {
		t.Fatal(err)
	}
	if unread, _ = e.svc.UnreadCount(ctx, u.ID); unread != 4 {
		t.Fatalf("unread after read-all = %d", unread)
	}
	if err := e.svc.Delete(ctx, u.ID, list[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.MarkRead(ctx, e.user().ID, list[1].ID); err == nil {
		t.Fatal("another user marked the message read")
	}
	if all, _ := e.svc.List(ctx, u.ID, nil, 20, 0); len(all) != 6 {
		t.Fatalf("list after delete = %d", len(all))
	}
}

func TestCampaignBatch(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	users := []db.User{e.user(), e.user(), e.user()}
	// users[0] opted into marketing; users[2] runs an old app version.
	_, _ = e.svc.UpdatePrefs(ctx, users[0].ID, notify.Prefs{Briefing: true, Report: true, Marketing: true,
		QuietHours: notify.QuietHours{Enabled: true, Start: "22:00", End: "08:00"}})
	for i, v := range []string{"1.2.0", "1.10.1", "1.1.9"} {
		if err := e.q.UpsertDevice(ctx, db.UpsertDeviceParams{PushToken: "tok" + v, UserID: users[i].ID, Platform: "ios", ApnsEnv: "sandbox", AppVersion: v}); err != nil {
			t.Fatal(err)
		}
	}

	aud := notify.Audience{Type: "filter", MinAppVersion: "1.2"}
	if n, err := e.svc.EstimateAudience(ctx, aud); err != nil || n != 2 {
		t.Fatalf("estimate = %d %v", n, err)
	}
	raw, _ := json.Marshal(aud)
	c, err := e.q.CreateCampaign(ctx, db.CreateCampaignParams{Name: "双十一", Category: "marketing", Title: "新模板", Body: "试试看",
		Deeplink: "zeus://templates", Audience: raw})
	if err != nil {
		t.Fatal(err)
	}
	// Not approved yet: nothing to start.
	if ok, _ := e.svc.StartCampaign(ctx, c.ID); ok {
		t.Fatal("started a draft")
	}
	_, _ = e.q.SubmitCampaign(ctx, db.SubmitCampaignParams{ID: c.ID, AudienceEstimate: 2})
	_, _ = e.q.ApproveCampaign(ctx, db.ApproveCampaignParams{ID: c.ID})
	if ok, err := e.svc.StartCampaign(ctx, c.ID); !ok || err != nil {
		t.Fatalf("start = %v %v", ok, err)
	}
	_, done, err := e.svc.SendCampaignBatch(ctx, c.ID, uuid.Nil)
	if err != nil || !done {
		t.Fatalf("batch = %v %v", done, err)
	}
	// Re-running the batch must not duplicate messages.
	_, _, _ = e.svc.SendCampaignBatch(ctx, c.ID, uuid.Nil)
	st, _ := e.q.CampaignStats(ctx, &c.ID)
	if st.Messages != 2 || st.Pushed != 1 || st.PushSkipped != 1 {
		t.Fatalf("stats = %+v", st)
	}
	if len(e.messages(users[2].ID)) != 0 {
		t.Fatal("old app version was targeted")
	}

	// Recall hides it from the message center.
	if n, _ := e.q.RecallCampaignMessages(ctx, &c.ID); n != 2 {
		t.Fatalf("recalled %d", n)
	}
	if len(e.messages(users[0].ID)) != 0 {
		t.Fatal("recalled message still listed")
	}
}
