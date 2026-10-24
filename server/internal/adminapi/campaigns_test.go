package adminapi_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/notify"
)

func TestCampaignConsole(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	h.createOwner("owner@zeus.test", "correct-horse-battery")
	h.login("owner@zeus.test", "correct-horse-battery")
	ownerToken := h.token

	var users []db.User
	for i := 0; i < 3; i++ {
		u, err := h.q.CreateUser(ctx, db.CreateUserParams{Nickname: "u", Timezone: "Asia/Shanghai"})
		if err != nil {
			t.Fatal(err)
		}
		users = append(users, u)
	}
	if err := h.q.UpsertDevice(ctx, db.UpsertDeviceParams{PushToken: "tok", UserID: users[0].ID, Platform: "ios", ApnsEnv: "sandbox", AppVersion: "1.2.0"}); err != nil {
		t.Fatal(err)
	}

	var est gen.AudienceEstimate
	h.do("POST", "/admin/v1/campaigns/audience-estimate", map[string]any{"type": "all"}, 200, &est)
	if est.Count != 3 {
		t.Fatalf("all = %d", est.Count)
	}
	h.do("POST", "/admin/v1/campaigns/audience-estimate", map[string]any{"type": "filter", "minAppVersion": "1.1"}, 200, &est)
	if est.Count != 1 {
		t.Fatalf("filter = %d", est.Count)
	}
	h.do("POST", "/admin/v1/campaigns/audience-estimate", map[string]any{"type": "users", "userIds": []string{"bad"}}, 400, nil)

	// An operator drafts and submits; they cannot approve.
	var op gen.AdminWithPassword
	h.do("POST", "/admin/v1/admins", map[string]string{"email": "op@zeus.test", "role": "operator"}, 201, &op)
	h.login("op@zeus.test", op.Password)
	input := map[string]any{
		"name": "新模板推广", "category": "marketing", "title": "英文早报上线", "body": "试试看",
		"deeplink": "zeus://templates", "audience": map[string]any{"type": "users", "userIds": []string{users[0].ID.String(), users[1].ID.String()}},
	}
	bad := map[string]any{"name": "x", "category": "marketing", "title": "t", "body": "", "deeplink": "javascript:alert(1)", "audience": map[string]any{"type": "all"}}
	h.do("POST", "/admin/v1/campaigns", bad, 400, nil)
	var c gen.Campaign
	h.do("POST", "/admin/v1/campaigns", input, 201, &c)
	if c.Status != "draft" || c.AudienceEstimate != 2 || c.CreatedBy == nil || *c.CreatedBy != "op@zeus.test" {
		t.Fatalf("created = %+v", c)
	}
	input["title"] = "英文早报来了"
	h.do("PUT", "/admin/v1/campaigns/"+c.Id, input, 200, &c)
	var test gen.CampaignTestResult
	h.do("POST", "/admin/v1/campaigns/"+c.Id+"/test-send", map[string]any{"userIds": []string{users[0].ID.String(), uuid.NewString()}}, 200, &test)
	if len(test.Results) != 2 || test.Results[0].Status != "sent" || test.Results[1].Status != "not_found" {
		t.Fatalf("test send = %+v", test)
	}
	if ns := h.pushes.Notifications(); len(ns) != 1 || ns[0].Title != "[测试] 英文早报来了" {
		t.Fatalf("test pushes = %+v", ns)
	}
	h.do("POST", "/admin/v1/campaigns/"+c.Id+"/submit", nil, 200, &c)
	if c.Status != "pending_approval" {
		t.Fatalf("submitted = %s", c.Status)
	}
	h.do("PUT", "/admin/v1/campaigns/"+c.Id, input, 409, nil)
	h.do("POST", "/admin/v1/campaigns/"+c.Id+"/approve", nil, 403, nil)

	// The owner rejects, the operator resubmits with a schedule, the owner approves.
	h.token = ownerToken
	h.do("POST", "/admin/v1/campaigns/"+c.Id+"/reject", map[string]string{"reason": "标题再短一点"}, 200, &c)
	if c.Status != "draft" || c.RejectReason != "标题再短一点" {
		t.Fatalf("rejected = %+v", c)
	}
	sendAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	input["sendAt"] = sendAt
	h.do("PUT", "/admin/v1/campaigns/"+c.Id, input, 200, &c)
	h.do("POST", "/admin/v1/campaigns/"+c.Id+"/submit", nil, 200, &c)
	h.do("POST", "/admin/v1/campaigns/"+c.Id+"/approve", nil, 200, &c)
	if c.Status != "scheduled" || c.ApprovedBy == nil || len(h.orch.campaigns) != 1 || !h.orch.campaigns[0].SendAt.Equal(sendAt) {
		t.Fatalf("approved = %+v runs %+v", c, h.orch.campaigns)
	}
	h.do("DELETE", "/admin/v1/campaigns/"+c.Id, nil, 409, nil)

	// What the orchestrator does at send time.
	cid := uuid.MustParse(c.Id)
	svc := &notify.Service{Q: h.q, Sender: h.pushes, Now: func() time.Time { return time.Date(2026, 10, 24, 4, 0, 0, 0, time.UTC) }}
	if ok, _ := svc.StartCampaign(ctx, cid); !ok {
		t.Fatal("not started")
	}
	if _, done, err := svc.SendCampaignBatch(ctx, cid, uuid.Nil); err != nil || !done {
		t.Fatalf("batch %v %v", done, err)
	}
	_ = svc.FinishCampaign(ctx, cid)
	h.do("GET", "/admin/v1/campaigns/"+c.Id, nil, 200, &c)
	// Marketing is opt-in, so both users only get the message-center entry.
	if c.Status != "sent" || c.Stats.Messages != 2 || c.Stats.Pushed != 0 || c.Stats.PushSkipped != 2 {
		t.Fatalf("sent = %s %+v", c.Status, c.Stats)
	}
	h.do("POST", "/admin/v1/campaigns/"+c.Id+"/cancel", nil, 409, nil)
	h.do("POST", "/admin/v1/campaigns/"+c.Id+"/recall", nil, 200, &c)
	if c.Status != "recalled" || c.RecalledAt == nil {
		t.Fatalf("recalled = %+v", c)
	}
	if n, _ := h.q.CountUnreadMessages(ctx, users[0].ID); n != 0 {
		t.Fatalf("unread after recall = %d", n)
	}

	var list gen.CampaignPage
	h.do("GET", "/admin/v1/campaigns?status=recalled", nil, 200, &list)
	if len(list.Items) != 1 {
		t.Fatalf("list = %+v", list)
	}

	// Cancelling a pending campaign; drafts can be deleted.
	var d gen.Campaign
	delete(input, "sendAt")
	h.do("POST", "/admin/v1/campaigns", input, 201, &d)
	h.do("POST", "/admin/v1/campaigns/"+d.Id+"/submit", nil, 200, nil)
	h.do("POST", "/admin/v1/campaigns/"+d.Id+"/cancel", nil, 200, &d)
	if d.Status != "cancelled" || len(h.orch.stopped) != 2 {
		t.Fatalf("cancelled = %s", d.Status)
	}
	h.do("POST", "/admin/v1/campaigns", input, 201, &d)
	h.do("DELETE", "/admin/v1/campaigns/"+d.Id, nil, 204, nil)
	h.do("GET", "/admin/v1/campaigns/"+d.Id, nil, 404, nil)
}
