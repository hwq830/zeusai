package adminapi_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/orch"
	"github.com/hwq830/zeusai/server/internal/quota"
	"github.com/hwq830/zeusai/server/internal/report"
	"github.com/hwq830/zeusai/server/internal/schedule"
)

func (f *fakeOrch) StartReport(_ context.Context, run orch.ReportRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, run)
	return nil
}

func (f *fakeOrch) StartBriefing(_ context.Context, run orch.BriefingRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.briefings = append(f.briefings, run)
	return nil
}

func TestContentManagement(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	h.createOwner("owner@zeus.test", "correct-horse-battery")
	h.createAdmin("viewer@zeus.test", "correct-horse-battery", "viewer")

	// ---- fixtures: a finished report with usage, feedback and a share; a subscription with today's issue
	phone := "+8613800005678"
	u, err := h.q.CreateUser(ctx, db.CreateUserParams{Nickname: "小李", Phone: &phone, Timezone: "Asia/Shanghai"})
	if err != nil {
		t.Fatal(err)
	}
	tk, err := h.q.CreateTask(ctx, db.CreateTaskParams{UserID: u.ID, Kind: "report", Title: "储能行业", Input: []byte(`{"topic":"储能行业","depth":"deep"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.q.UpdateTaskProgress(ctx, db.UpdateTaskProgressParams{ID: tk.ID, UserID: u.ID, Status: "done"}); err != nil {
		t.Fatal(err)
	}
	if err := h.q.RecordModelUsage(ctx, db.RecordModelUsageParams{UserID: &u.ID, TaskID: &tk.ID, Provider: "qwen", Model: "qwen-max",
		Tier: "strong", InputTokens: 3000, OutputTokens: 1000, CostMicros: 3_500_000, BilledTokens: 40_000}); err != nil {
		t.Fatal(err)
	}
	rep, err := h.q.CreateReport(ctx, db.CreateReportParams{UserID: u.ID, TaskID: &tk.ID, Title: "储能行业", Topic: "储能行业", Depth: "deep", Focuses: []string{"政策"}, Language: "zh-Hans"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.q.SetTaskLinks(ctx, db.SetTaskLinksParams{ID: tk.ID, UserID: u.ID, ReportID: &rep.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.q.CompleteReport(ctx, db.CompleteReportParams{ID: rep.ID, UserID: u.ID, Title: "储能行业", Summary: "摘要", Markdown: "# 储能\n正文",
		Sources: []byte(`[{"url":"https://www.example.com/a","title":"来源 A"}]`), SourceCount: 1, WordCount: 800}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.q.UpsertReportFeedback(ctx, db.UpsertReportFeedbackParams{ID: rep.ID, UserID: u.ID, Rating: "unsatisfied", Reasons: []string{"shallow"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.q.CreateShare(ctx, db.CreateShareParams{UserID: u.ID, ReportID: &rep.ID, Token: "tokc1", Scope: "full"}); err != nil {
		t.Fatal(err)
	}
	sched := schedule.Schedule{Freq: "weekly", Weekdays: []int{1, 3}, Time: "07:30", TZ: "Asia/Shanghai"}
	sc, _ := json.Marshal(sched)
	next := time.Now().Add(time.Hour)
	sub, err := h.q.CreateSubscription(ctx, db.CreateSubscriptionParams{UserID: u.ID, Name: "储能日报", FieldValues: []byte(`{}`),
		Topics: []string{"储能"}, Schedule: sc, Length: "standard", PushEnabled: true, NextRunAt: &next})
	if err != nil {
		t.Fatal(err)
	}
	btk, err := h.q.CreateTask(ctx, db.CreateTaskParams{UserID: u.ID, Kind: "briefing", Title: "储能日报", Input: []byte(`{}`), SubscriptionID: &sub.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.q.UpdateTaskProgress(ctx, db.UpdateTaskProgressParams{ID: btk.ID, UserID: u.ID, Status: "done", Plan: []byte(`[{"index":1,"title":"要闻","tool":"search"}]`)}); err != nil {
		t.Fatal(err)
	}
	if err := h.q.RecordModelUsage(ctx, db.RecordModelUsageParams{UserID: &u.ID, TaskID: &btk.ID, Provider: "qwen", Model: "qwen-plus",
		Tier: "fast", CostMicros: 100_000, BilledTokens: 5_000}); err != nil {
		t.Fatal(err)
	}
	today := schedule.LocalDate(time.Now(), sched.Location())
	b, err := h.q.StartBriefing(ctx, db.StartBriefingParams{UserID: u.ID, SubscriptionID: sub.ID, TaskID: &btk.ID, Title: "储能日报", Date: today, Language: "zh-Hans"})
	if err != nil {
		t.Fatal(err)
	}
	sections := `[{"key":"news","title":"要闻","items":[{"id":"i1","title":"条目一","summary":"s1","sources":[{"url":"https://a.cn/1","title":"A"}]},{"id":"i2","title":"条目二","summary":"s2","sources":[]}]},
	              {"key":"cal","title":"我的日程","private":true,"items":[{"id":"p1","title":"和张总开会","summary":"10:00","sources":[]}]}]`
	if _, err := h.q.CompleteBriefing(ctx, db.CompleteBriefingParams{ID: b.ID, UserID: u.ID, Headline: "今日要点", Sections: []byte(sections), ItemCount: 3, SourceCount: 1}); err != nil {
		t.Fatal(err)
	}
	if err := h.q.UpsertBriefingFeedback(ctx, db.UpsertBriefingFeedbackParams{ID: b.ID, UserID: u.ID, ItemID: "i2", Rating: "meh", Comment: "太旧了"}); err != nil {
		t.Fatal(err)
	}

	// ---- a viewer sees lists but not bodies
	h.login("viewer@zeus.test", "correct-horse-battery")
	var rp gen.ReportPage
	h.do("GET", "/admin/v1/reports", nil, 200, &rp)
	if len(rp.Items) != 1 {
		t.Fatalf("viewer reports = %+v", rp)
	}
	h.do("GET", "/admin/v1/reports/"+rep.ID.String(), nil, 403, nil)
	h.do("GET", "/admin/v1/briefings/"+b.ID.String(), nil, 403, nil)

	h.login("owner@zeus.test", "correct-horse-battery")

	// ---- report list and filters
	h.do("GET", "/admin/v1/reports?q=储能&rating=unsatisfied&minCostCny=3&shared=true&depth=deep", nil, 200, &rp)
	if len(rp.Items) != 1 {
		t.Fatalf("filtered reports = %+v", rp)
	}
	r0 := rp.Items[0]
	if r0.CostCny != 3.5 || r0.Shares != 1 || deref(r0.Rating) != "unsatisfied" || r0.PhoneMasked == phone || r0.Hidden {
		t.Fatalf("report row = %+v", r0)
	}
	h.do("GET", "/admin/v1/reports?rating=satisfied", nil, 200, &rp)
	if len(rp.Items) != 0 {
		t.Fatalf("satisfied filter = %+v", rp)
	}

	// ---- report detail is audited
	var rd gen.AdminReportDetail
	h.do("GET", "/admin/v1/reports/"+rep.ID.String(), nil, 200, &rd)
	if rd.Markdown == "" || len(rd.Sources) != 1 || deref(rd.Sources[0].Domain) != "example.com" || len(rd.Tasks) != 1 || len(rd.Tasks[0].Calls) != 1 ||
		rd.Feedback == nil || len(rd.Shares) != 1 || rd.Refunded {
		t.Fatalf("report detail = %+v", rd)
	}
	var audit gen.AuditPage
	h.do("GET", "/admin/v1/audit-log", nil, 200, &audit)
	if len(audit.Items) == 0 || audit.Items[0].Action != "content.view" {
		t.Fatalf("audit = %+v", audit.Items)
	}

	// ---- regenerate: new report, console task, not charged
	q := &quota.Service{Q: h.q}
	before, err := q.TokenUsage(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	var run gen.ContentRunResult
	h.do("POST", "/admin/v1/reports/"+rep.ID.String()+"/regenerate", nil, 202, &run)
	if run.ReportId == nil || len(h.orch.reports) != 1 || h.orch.reports[0].ReportID.String() != *run.ReportId {
		t.Fatalf("regenerate = %+v, orch = %+v", run, h.orch.reports)
	}
	newTask := uuid.MustParse(run.TaskId)
	if err := h.q.RecordModelUsage(ctx, db.RecordModelUsageParams{UserID: &u.ID, TaskID: &newTask, Provider: "qwen", Model: "qwen-max",
		Tier: "strong", CostMicros: 1, BilledTokens: 99_000}); err != nil {
		t.Fatal(err)
	}
	after, err := q.TokenUsage(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if after.Used != before.Used {
		t.Fatalf("console re-run charged the user: %d → %d", before.Used, after.Used)
	}
	h.do("GET", "/admin/v1/reports/"+rep.ID.String(), nil, 200, &rd)
	if len(rd.Regenerations) != 1 {
		t.Fatalf("regenerations = %+v", rd.Regenerations)
	}
	var nr gen.AdminReportDetail
	h.do("GET", "/admin/v1/reports/"+*run.ReportId, nil, 200, &nr)
	if !nr.Report.AdminRun || deref(nr.Report.RegeneratedFrom) != rep.ID.String() || nr.Topic != "储能行业" || nr.Report.Depth != "deep" {
		t.Fatalf("new report = %+v", nr.Report)
	}

	// ---- refund once
	var rf gen.RefundResult
	h.do("POST", "/admin/v1/reports/"+rep.ID.String()+"/refund", nil, 200, &rf)
	if rf.Tokens != 40_000 || rf.Grant == nil {
		t.Fatalf("refund = %+v", rf)
	}
	h.do("POST", "/admin/v1/reports/"+rep.ID.String()+"/refund", nil, 409, nil)
	tok, _ := q.TokenUsage(ctx, u)
	if tok.Granted != 40_000 {
		t.Fatalf("granted = %d", tok.Granted)
	}

	// ---- take down: shares revoked, user notified, app API shows a placeholder
	h.do("POST", "/admin/v1/reports/"+rep.ID.String()+"/hide", map[string]any{"reason": ""}, 400, nil)
	h.do("POST", "/admin/v1/reports/"+rep.ID.String()+"/hide", map[string]any{"reason": "含不实信息"}, 200, &rd)
	if !rd.Report.Hidden || rd.HiddenReason != "含不实信息" || rd.Shares[0].RevokedAt == nil {
		t.Fatalf("hidden report = %+v", rd)
	}
	if n := len(h.pushes.Notifications()); n == 0 {
		t.Fatal("no takedown notice")
	}
	row, err := h.q.GetReport(ctx, db.GetReportParams{ID: rep.ID, UserID: u.ID})
	if err != nil {
		t.Fatal(err)
	}
	if ar := report.FromRow(row); !ar.TakenDown || ar.Summary != "" || ar.HTMLURL != "" {
		t.Fatalf("app report = %+v", ar)
	}
	h.do("GET", "/admin/v1/reports?status=hidden", nil, 200, &rp)
	if len(rp.Items) != 1 {
		t.Fatalf("hidden filter = %+v", rp)
	}
	h.do("POST", "/admin/v1/reports/"+rep.ID.String()+"/unhide", nil, 200, &rd)
	if rd.Report.Hidden {
		t.Fatal("still hidden")
	}

	// ---- subscriptions
	var sp gen.SubscriptionPage
	h.do("GET", "/admin/v1/subscriptions?userId="+u.ID.String(), nil, 200, &sp)
	if len(sp.Items) != 1 || sp.Items[0].ScheduleText != "每周一、三 07:30" || deref(sp.Items[0].LastStatus) != "ready" || sp.Items[0].RecentReady != 1 {
		t.Fatalf("subscriptions = %+v", sp)
	}
	var as gen.AdminSubscription
	h.do("POST", "/admin/v1/subscriptions/"+sub.ID.String()+"/pause", nil, 200, &as)
	if as.Status != "paused" || deref(as.PausedReason) != "admin" || !h.orch.schedules[sub.ID] {
		t.Fatalf("paused = %+v", as)
	}
	h.do("POST", "/admin/v1/subscriptions/"+sub.ID.String()+"/pause", nil, 409, nil)
	h.do("POST", "/admin/v1/subscriptions/"+sub.ID.String()+"/resume", nil, 200, &as)
	if as.Status != "active" || as.NextRunAt == nil || h.orch.schedules[sub.ID] {
		t.Fatalf("resumed = %+v", as)
	}

	// ---- briefings: list, masked detail, reveal (audited)
	var bp gen.BriefingPage
	h.do("GET", "/admin/v1/briefings?disliked=true&from="+today.Format(time.DateOnly), nil, 200, &bp)
	if len(bp.Items) != 1 || bp.Items[0].Meh != 1 || bp.Items[0].TemplateName != "" {
		t.Fatalf("briefings = %+v", bp)
	}
	var bd gen.AdminBriefingDetail
	h.do("GET", "/admin/v1/briefings/"+b.ID.String(), nil, 200, &bd)
	if len(bd.Sections) != 2 || !bd.Sections[1].Masked || len(bd.Sections[1].Items) != 0 || bd.PrivateRevealed ||
		bd.Sections[0].Items[1].Meh != 1 || bd.Sections[0].Items[1].Comments[0] != "太旧了" || bd.Task == nil || bd.Task.Steps == nil || len(*bd.Task.Steps) != 1 || bd.Subscription == nil {
		t.Fatalf("briefing detail = %+v", bd)
	}
	h.do("GET", "/admin/v1/briefings/"+b.ID.String()+"?revealReason=工单123", nil, 200, &bd)
	if !bd.PrivateRevealed || bd.Sections[1].Items[0].Title != "和张总开会" {
		t.Fatalf("revealed = %+v", bd.Sections)
	}
	h.do("GET", "/admin/v1/audit-log", nil, 200, &audit)
	if audit.Items[0].Action != "content.reveal_private" {
		t.Fatalf("audit = %+v", audit.Items[0])
	}

	// ---- hide one item and put it back
	h.do("POST", "/admin/v1/briefings/"+b.ID.String()+"/items/i1/hide", map[string]any{"reason": "来源不可靠"}, 200, &bd)
	if len(bd.Sections[0].Items) != 1 || bd.Sections[0].Items[0].Id != "i2" || len(bd.HiddenItems) != 1 || bd.Briefing.ItemCount != 2 {
		t.Fatalf("after hide item = %+v", bd)
	}
	h.do("POST", "/admin/v1/briefings/"+b.ID.String()+"/items/nope/hide", map[string]any{"reason": "x"}, 404, nil)
	h.do("POST", "/admin/v1/briefings/"+b.ID.String()+"/items/i1/unhide", nil, 200, &bd)
	if len(bd.Sections[0].Items) != 2 || bd.Sections[0].Items[0].Id != "i1" || len(bd.HiddenItems) != 0 || bd.Briefing.ItemCount != 3 {
		t.Fatalf("after unhide item = %+v", bd)
	}

	// ---- regenerate today's issue: revision kept, console task, push flag passed on
	h.do("POST", "/admin/v1/briefings/"+b.ID.String()+"/regenerate", map[string]any{"push": true}, 202, &run)
	if len(h.orch.briefings) != 1 || !h.orch.briefings[0].Push || h.orch.briefings[0].SubscriptionID != sub.ID {
		t.Fatalf("briefing runs = %+v", h.orch.briefings)
	}
	h.do("GET", "/admin/v1/briefings/"+b.ID.String(), nil, 200, &bd)
	if len(bd.Revisions) != 1 || bd.Revisions[0].Headline != "今日要点" || !bd.Revisions[0].Sections[1].Masked {
		t.Fatalf("revisions = %+v", bd.Revisions)
	}

	// ---- take down the issue
	h.do("POST", "/admin/v1/briefings/"+b.ID.String()+"/hide", map[string]any{"reason": "违规", "notify": false}, 200, &bd)
	if !bd.Briefing.Hidden {
		t.Fatal("briefing not hidden")
	}
	h.do("GET", "/admin/v1/briefings?status=hidden", nil, 200, &bp)
	if len(bp.Items) != 1 {
		t.Fatalf("hidden briefings = %+v", bp)
	}
	h.do("POST", "/admin/v1/briefings/"+b.ID.String()+"/refund", nil, 200, &rf)
	if rf.Tokens != 5_000 {
		t.Fatalf("briefing refund = %+v", rf)
	}

	// ---- notes
	var note gen.ContentNote
	h.do("POST", "/admin/v1/content/notes", map[string]any{"targetType": "report", "targetId": rep.ID.String(), "body": "用户反馈 #12 已回复"}, 201, &note)
	var notes []gen.ContentNote
	h.do("GET", "/admin/v1/content/notes?targetType=report&targetId="+rep.ID.String(), nil, 200, &notes)
	if len(notes) != 1 || notes[0].Body != "用户反馈 #12 已回复" {
		t.Fatalf("notes = %+v", notes)
	}

	// ---- exception queue: unsatisfied + costly report, disliked briefing; resolve one
	var ip gen.ContentIssuePage
	h.do("GET", "/admin/v1/content/issues", nil, 200, &ip)
	kinds := map[string]bool{}
	for _, it := range ip.Items {
		kinds[string(it.TargetType)+"/"+string(it.Kind)] = true
	}
	for _, k := range []string{"report/unsatisfied", "report/costly", "report/few_sources", "briefing/disliked", "briefing/few_sources"} {
		if !kinds[k] {
			t.Fatalf("issues missing %s: %+v", k, ip.Items)
		}
	}
	if ip.OpenCounts["unsatisfied"] != 1 {
		t.Fatalf("open counts = %+v", ip.OpenCounts)
	}
	body := map[string]any{"targetType": "report", "targetId": rep.ID.String(), "kind": "unsatisfied", "note": "已重新生成"}
	h.do("POST", "/admin/v1/content/issues/resolve", body, 204, nil)
	ip = gen.ContentIssuePage{} // decoding into a used map keeps old keys
	h.do("GET", "/admin/v1/content/issues?kind=unsatisfied", nil, 200, &ip)
	if len(ip.Items) != 0 || ip.OpenCounts["unsatisfied"] != 0 {
		t.Fatalf("after resolve = %+v", ip)
	}
	h.do("GET", "/admin/v1/content/issues?kind=unsatisfied&resolved=true", nil, 200, &ip)
	if len(ip.Items) != 1 || deref(ip.Items[0].ResolutionNote) != "已重新生成" {
		t.Fatalf("resolved = %+v", ip)
	}
	h.do("POST", "/admin/v1/content/issues/reopen", body, 204, nil)
	h.do("GET", "/admin/v1/content/issues?kind=unsatisfied", nil, 200, &ip)
	if len(ip.Items) != 1 {
		t.Fatalf("after reopen = %+v", ip)
	}
}
