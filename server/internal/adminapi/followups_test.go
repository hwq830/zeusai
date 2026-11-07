package adminapi_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/adminapi"
	"github.com/hwq830/zeusai/server/internal/db"
)

type adminFollowupItem struct {
	Id              string     `json:"id"`
	Question        string     `json:"question"`
	Answer          string     `json:"answer"`
	AnswerTruncated bool       `json:"answerTruncated"`
	AttachedAt      *time.Time `json:"attachedAt"`
	CreatedAt       time.Time  `json:"createdAt"`
}

type adminFollowupPage struct {
	Items      []adminFollowupItem `json:"items"`
	NextCursor *string             `json:"nextCursor"`
}

type globalFollowupItem struct {
	Id              string     `json:"id"`
	ReportId        string     `json:"reportId"`
	ReportTitle     string     `json:"reportTitle"`
	ReportHidden    bool       `json:"reportHidden"`
	UserId          string     `json:"userId"`
	Question        string     `json:"question"`
	Answer          string     `json:"answer"`
	AnswerTruncated bool       `json:"answerTruncated"`
	AttachedAt      *time.Time `json:"attachedAt"`
	CreatedAt       time.Time  `json:"createdAt"`
}

type globalFollowupPage struct {
	Items      []globalFollowupItem `json:"items"`
	NextCursor *string              `json:"nextCursor"`
}

type followupFixture struct {
	items []db.ReportFollowup
}

func seedFollowups(t *testing.T, h *harness) followupFixture {
	t.Helper()
	ctx := context.Background()
	u1, err := h.q.CreateUser(ctx, db.CreateUserParams{Nickname: "甲", Timezone: "Asia/Shanghai"})
	if err != nil {
		t.Fatal(err)
	}
	u2, err := h.q.CreateUser(ctx, db.CreateUserParams{Nickname: "乙", Timezone: "Asia/Shanghai"})
	if err != nil {
		t.Fatal(err)
	}
	visible, err := h.q.CreateReport(ctx, db.CreateReportParams{UserID: u1.ID, Title: "可见报告", Topic: "t", Depth: "quick", Focuses: []string{}, Language: "zh"})
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := h.q.CreateReport(ctx, db.CreateReportParams{UserID: u2.ID, Title: "已下架报告", Topic: "t", Depth: "quick", Focuses: []string{}, Language: "zh"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := h.q.AdminSetReportHidden(ctx, db.AdminSetReportHiddenParams{ID: hidden.ID, HiddenAt: &now, Reason: "测试"}); err != nil {
		t.Fatal(err)
	}

	long := strings.Repeat("长答案", 250) // 750 runes, well past the 500-rune list cap
	create := func(reportID, userID uuid.UUID, question, answer string, attached bool, at time.Time) db.ReportFollowup {
		row, err := h.q.CreateReportFollowup(ctx, db.CreateReportFollowupParams{
			ReportID: reportID, UserID: userID, Question: question, Answer: answer, Sources: []byte(`[]`),
		})
		if err != nil {
			t.Fatal(err)
		}
		var attachedAt *time.Time
		if attached {
			attachedAt = &at
		}
		if _, err := h.pool.Exec(ctx, "UPDATE report_followups SET created_at = $2, attached_at = $3 WHERE id = $1",
			row.ID, at, attachedAt); err != nil {
			t.Fatal(err)
		}
		row.CreatedAt, row.AttachedAt = at, attachedAt
		return row
	}
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	older := create(visible.ID, u1.ID, "第一个问题：市场规模？", long, false, base)
	attached := create(visible.ID, u1.ID, "第二个问题：已加入报告", "短答案", true, base.Add(time.Hour))
	hiddenRow := create(hidden.ID, u2.ID, "第三个问题：下架报告", "下架报告里的答案", true, base.Add(2*time.Hour))
	literal := create(visible.ID, u1.ID, "增长 100% 的原因", "含百分号的问题", false, base.Add(3*time.Hour))
	return followupFixture{items: []db.ReportFollowup{older, attached, hiddenRow, literal}}
}

func TestAdminReportFollowups(t *testing.T) {
	h := setup(t)
	h.createOwner("owner@zeus.test", "correct-horse-battery")
	h.login("owner@zeus.test", "correct-horse-battery")
	f := seedFollowups(t, h)
	reportID := f.items[1].ReportID.String()

	var page adminFollowupPage
	h.do("GET", "/admin/v1/reports/"+reportID+"/followups?limit=2", nil, 200, &page)
	if len(page.Items) != 2 || page.NextCursor == nil {
		t.Fatalf("first page: %+v", page)
	}
	if page.Items[0].Id != f.items[3].ID.String() || page.Items[1].Id != f.items[1].ID.String() {
		t.Fatalf("newest first: %+v", page.Items)
	}
	if page.Items[0].AnswerTruncated || page.Items[1].AttachedAt == nil {
		t.Fatalf("flags: %+v", page.Items)
	}
	h.do("GET", "/admin/v1/reports/"+reportID+"/followups?limit=2&cursor="+*page.NextCursor, nil, 200, &page)
	if len(page.Items) != 1 || page.NextCursor != nil {
		t.Fatalf("second page: %+v", page)
	}
	if page.Items[0].Id != f.items[0].ID.String() || !page.Items[0].AnswerTruncated || len([]rune(page.Items[0].Answer)) != 500 {
		t.Fatalf("truncated answer: %+v", page.Items[0])
	}

	// Admins can read any user's report, and a missing report is a 404.
	h.do("GET", "/admin/v1/reports/"+f.items[2].ReportID.String()+"/followups", nil, 200, &page)
	if len(page.Items) != 1 {
		t.Fatalf("other user's report: %+v", page.Items)
	}
	h.do("GET", "/admin/v1/reports/"+uuid.NewString()+"/followups", nil, 404, nil)
}

func TestAdminFollowupsList(t *testing.T) {
	h := setup(t)
	h.createOwner("owner@zeus.test", "correct-horse-battery")
	h.login("owner@zeus.test", "correct-horse-battery")
	f := seedFollowups(t, h)

	var page globalFollowupPage
	h.do("GET", "/admin/v1/followups", nil, 200, &page)
	if len(page.Items) != 4 || page.NextCursor != nil {
		t.Fatalf("all: %+v", page)
	}
	if page.Items[0].Id != f.items[3].ID.String() || page.Items[3].Id != f.items[0].ID.String() {
		t.Fatalf("newest first: %+v", page.Items)
	}
	if page.Items[0].ReportHidden || !page.Items[1].ReportHidden || page.Items[2].ReportHidden {
		t.Fatalf("reportHidden: %+v", page.Items)
	}
	if page.Items[1].ReportTitle != "已下架报告" || page.Items[1].UserId != f.items[2].UserID.String() {
		t.Fatalf("row metadata: %+v", page.Items[1])
	}
	if !page.Items[3].AnswerTruncated || len([]rune(page.Items[3].Answer)) != 500 {
		t.Fatalf("truncated: %+v", page.Items[3])
	}

	// attached is three-state.
	h.do("GET", "/admin/v1/followups?attached=true", nil, 200, &page)
	if len(page.Items) != 2 {
		t.Fatalf("attached=true: %+v", page.Items)
	}
	h.do("GET", "/admin/v1/followups?attached=false", nil, 200, &page)
	if len(page.Items) != 2 {
		t.Fatalf("attached=false: %+v", page.Items)
	}

	// q searches the question and the answer, and %/_ are literal.
	h.do("GET", "/admin/v1/followups?q="+url.QueryEscape("市场规模"), nil, 200, &page)
	if len(page.Items) != 1 || page.Items[0].Id != f.items[0].ID.String() {
		t.Fatalf("question search: %+v", page.Items)
	}
	h.do("GET", "/admin/v1/followups?q="+url.QueryEscape("下架报告里的答案"), nil, 200, &page)
	if len(page.Items) != 1 || page.Items[0].Id != f.items[2].ID.String() {
		t.Fatalf("answer search: %+v", page.Items)
	}
	h.do("GET", "/admin/v1/followups?q=100%25", nil, 200, &page)
	if len(page.Items) != 1 || page.Items[0].Id != f.items[3].ID.String() {
		t.Fatalf("literal %%: %+v", page.Items)
	}

	// userId filter, and pagination.
	h.do("GET", "/admin/v1/followups?userId="+f.items[2].UserID.String(), nil, 200, &page)
	if len(page.Items) != 1 || page.Items[0].Id != f.items[2].ID.String() {
		t.Fatalf("userId: %+v", page.Items)
	}
	h.do("GET", "/admin/v1/followups?limit=2", nil, 200, &page)
	if len(page.Items) != 2 || page.NextCursor == nil {
		t.Fatalf("page 1: %+v", page)
	}
	h.do("GET", "/admin/v1/followups?limit=2&cursor="+*page.NextCursor, nil, 200, &page)
	if len(page.Items) != 2 || page.NextCursor != nil {
		t.Fatalf("page 2: %+v", page)
	}
}

func TestAdminFollowupsRequiresContentRead(t *testing.T) {
	h := setup(t)
	h.createOwner("owner@zeus.test", "correct-horse-battery")
	hash, err := adminapi.HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.q.CreateAdminUser(context.Background(), db.CreateAdminUserParams{
		Email: "viewer@zeus.test", PasswordHash: hash, Role: "viewer",
	}); err != nil {
		t.Fatal(err)
	}
	h.login("viewer@zeus.test", "correct-horse-battery")
	h.do("GET", "/admin/v1/followups", nil, 403, nil)
}
