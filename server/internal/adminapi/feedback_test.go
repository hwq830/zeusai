package adminapi_test

import (
	"context"
	"strings"
	"testing"

	"github.com/hwq830/zeusai/server/internal/adminapi"
	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/db"
)

func TestFeedbackConsole(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	h.createOwner("owner@zeus.test", "correct-horse-battery")
	h.login("owner@zeus.test", "correct-horse-battery")

	phone := "+8613800005678"
	u, err := h.q.CreateUser(ctx, db.CreateUserParams{Nickname: "小李", Phone: &phone, Timezone: "Asia/Shanghai"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := h.q.CreateFeedback(ctx, db.CreateFeedbackParams{UserID: u.ID, Type: "bug", Content: "报告里的图表显示不全",
		Contact: "wx: li", Client: []byte(`{"appVersion":"1.0.0","build":"42","os":"iOS 18.1"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.blob.Put(ctx, "feedback/shot.jpg", []byte{0xFF, 0xD8, 0xFF}, "image/jpeg"); err != nil {
		t.Fatal(err)
	}
	a, err := h.q.CreateFeedbackAttachment(ctx, db.CreateFeedbackAttachmentParams{UserID: u.ID, BlobKey: "feedback/shot.jpg", SizeBytes: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.q.AttachFeedbackAttachment(ctx, db.AttachFeedbackAttachmentParams{ID: a.ID, UserID: u.ID, FeedbackID: &f.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.q.CreateFeedback(ctx, db.CreateFeedbackParams{UserID: u.ID, Type: "suggestion", Content: "希望支持英文简报", Client: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}

	// Ratings: two reports created today, one satisfied and one unsatisfied; one unrated.
	for i, rating := range []string{"satisfied", "unsatisfied", ""} {
		r, err := h.q.CreateReport(ctx, db.CreateReportParams{UserID: u.ID, Title: "报告", Topic: "t", Depth: "quick", Focuses: []string{}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.q.CompleteReport(ctx, db.CompleteReportParams{ID: r.ID, UserID: u.ID, Title: "报告", Sources: []byte(`[]`)}); err != nil {
			t.Fatal(err)
		}
		if rating == "" {
			continue
		}
		reasons := []string{}
		if i == 1 {
			reasons = []string{"shallow", "slow"}
		}
		if _, err := h.q.UpsertReportFeedback(ctx, db.UpsertReportFeedbackParams{ID: r.ID, UserID: u.ID, Rating: rating, Reasons: reasons}); err != nil {
			t.Fatal(err)
		}
	}

	var dash gen.Dashboard
	h.do("GET", "/admin/v1/dashboard", nil, 200, &dash)
	sat := dash.Satisfaction
	if sat.Reports7d.Completed != 3 || sat.Reports7d.Satisfied != 1 || sat.Reports7d.Unsatisfied != 1 || sat.OpenFeedback != 2 || len(sat.UnsatisfiedReasons30d) != 2 {
		t.Fatalf("satisfaction = %+v", sat)
	}

	var rp gen.ReportFeedbackPage
	h.do("GET", "/admin/v1/report-feedback?rating=unsatisfied&reason=slow", nil, 200, &rp)
	if len(rp.Items) != 1 || rp.Items[0].PhoneMasked != "138****5678" || len(rp.Items[0].Reasons) != 2 {
		t.Fatalf("report feedback = %+v", rp.Items)
	}

	var page gen.FeedbackPage
	h.do("GET", "/admin/v1/feedback?status=new&type=bug", nil, 200, &page)
	if len(page.Items) != 1 || page.Items[0].AppVersion == nil || *page.Items[0].AppVersion != "1.0.0 (42)" || page.Items[0].AttachmentCount != 1 {
		t.Fatalf("feedback list = %+v", page.Items)
	}
	path := "/admin/v1/feedback/" + f.ID.String()
	var d gen.FeedbackDetail
	h.do("GET", path, nil, 200, &d)
	if len(d.Attachments) != 1 || !strings.HasPrefix(d.Attachments[0].Url, "https://api.zeus.test/files/") || d.Client["os"] != "iOS 18.1" {
		t.Fatalf("detail = %+v", d)
	}

	// A reply moves new feedback to in progress and pushes a deep link to the user.
	h.do("POST", path+"/replies", map[string]string{"content": "   "}, 400, nil)
	h.do("POST", path+"/replies", map[string]string{"content": "感谢反馈，已在 1.0.1 修复"}, 201, &d)
	if d.Status != "in_progress" || len(d.Replies) != 1 || d.LastReplyAt == nil || d.HandledBy == nil || *d.HandledBy != "owner@zeus.test" {
		t.Fatalf("after reply = %+v", d)
	}
	if ns := h.pushes.Notifications(); len(ns) != 1 || ns[0].DeepLink != "zeus://feedback/"+f.ID.String() {
		t.Fatalf("push = %+v", ns)
	}
	h.do("PATCH", path, map[string]any{}, 400, nil)
	h.do("PATCH", path, map[string]any{"status": "resolved", "adminNote": "1.0.1 已修"}, 200, &d)
	if d.Status != "resolved" || d.AdminNote != "1.0.1 已修" {
		t.Fatalf("after patch = %+v", d)
	}

	// Viewers read but cannot handle feedback.
	hash, _ := adminapi.HashPassword("viewer-password-1")
	if _, err := h.q.CreateAdminUser(ctx, db.CreateAdminUserParams{Email: "viewer@zeus.test", PasswordHash: hash, Role: "viewer"}); err != nil {
		t.Fatal(err)
	}
	h.login("viewer@zeus.test", "viewer-password-1")
	h.do("GET", path, nil, 200, &d)
	h.do("POST", path+"/replies", map[string]string{"content": "hi"}, 403, nil)
	h.do("PATCH", path, map[string]any{"status": "closed"}, 403, nil)

	var audit gen.AuditPage
	h.login("owner@zeus.test", "correct-horse-battery")
	h.do("GET", "/admin/v1/audit-log", nil, 200, &audit)
	actions := map[string]bool{}
	for _, e := range audit.Items {
		actions[e.Action] = true
	}
	if !actions["feedback.reply"] || !actions["feedback.update"] {
		t.Fatalf("audit = %+v", actions)
	}
}
