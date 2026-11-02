package adminapi_test

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/calllog"
	"github.com/hwq830/zeusai/server/internal/db"
)

func TestExternalCallListFiltersPaginationAndUnknownUsage(t *testing.T) {
	h := setup(t)
	h.do("GET", "/admin/v1/calls", nil, 401, nil)
	h.createOwner("calls@zeus.test", "correct-horse-battery")
	h.login("calls@zeus.test", "correct-horse-battery")
	ctx := context.Background()
	u, err := h.q.CreateUser(ctx, db.CreateUserParams{Nickname: "calls", Timezone: "Asia/Shanghai"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := h.q.CreateTask(ctx, db.CreateTaskParams{UserID: u.ID, Kind: "report", Title: "calls", Input: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	r := calllog.New(h.q, nil)
	group := uuid.New()
	b := calllog.Begin{GroupID: group, Attempt: 1, Kind: "model", Provider: "qwen", Model: "qwen3.8-max", Dispatched: true, Scope: calllog.Scope{UserID: &u.ID, TaskID: &task.ID, Purpose: "report.write"}}
	first, err := r.Start(ctx, b, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	first.Finish(ctx, calllog.End{Status: "timeout", ErrorCode: "deadline_exceeded"})
	b.Provider = "deepseek"
	b.Model = "deepseek-chat"
	b.Attempt = 2
	second, err := r.Start(ctx, b, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	second.Finish(ctx, calllog.End{Status: "success", UsageKnown: true, InputTokens: 100, OutputTokens: 30, RequestID: "upstream-id", ResponseModel: "deepseek-flash"})
	search, err := r.Start(ctx, calllog.Begin{Kind: "search", Provider: "tavily", Protocol: "tavily", Dispatched: true, Scope: b.Scope, Query: "local news", Region: "TW"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	zero := 0
	search.Finish(ctx, calllog.End{Status: "empty", ResultCount: &zero})
	var page gen.ExternalCallList
	h.do("GET", "/admin/v1/calls?limit=2", nil, 200, &page)
	if len(page.Items) != 2 || page.NextCursor == nil || page.Items[0].Kind != "search" || page.Items[0].ResultCount == nil || *page.Items[0].ResultCount != 0 {
		t.Fatalf("page %+v", page)
	}
	var next gen.ExternalCallList
	h.do("GET", "/admin/v1/calls?limit=2&cursor="+*page.NextCursor, nil, 200, &next)
	if len(next.Items) != 1 || next.Items[0].Status != "timeout" || next.Items[0].InputTokens != nil || next.Items[0].OutputTokens != nil {
		t.Fatalf("unknown timeout usage %+v", next)
	}
	var filtered gen.ExternalCallList
	h.do("GET", "/admin/v1/calls?kind=model&provider=deepseek&model=deepseek-flash&status=success&taskId="+task.ID.String()+"&userId="+u.ID.String(), nil, 200, &filtered)
	if len(filtered.Items) != 1 || filtered.Items[0].GroupId != group || *filtered.Items[0].OutputTokens != 30 {
		t.Fatalf("filters %+v", filtered)
	}
	for _, query := range []string{"cursor=bad", "kind=bad", "status=bad", "taskId=bad", "from=2026-11-03T00:00:00Z&to=2026-11-02T00:00:00Z"} {
		h.do("GET", "/admin/v1/calls?"+query, nil, 400, nil)
	}
	var future gen.ExternalCallList
	h.do("GET", "/admin/v1/calls?from="+url.QueryEscape(time.Now().Add(time.Hour).UTC().Format(time.RFC3339)), nil, 200, &future)
	if len(future.Items) != 0 {
		t.Fatal("date filter ignored")
	}
	// A process that dies before finishing leaves a queryable unknown result, not zero usage.
	id := uuid.New()
	old := time.Now().Add(-time.Hour)
	if err := h.q.StartExternalCall(ctx, db.StartExternalCallParams{ID: id, GroupID: uuid.New(), Attempt: 1, Kind: "model", Provider: "crashed", Model: "m", Purpose: "report.write", Origin: "app", Dispatched: true, StartedAt: old, DeadlineAt: old.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	var unknown gen.ExternalCallList
	h.do("GET", "/admin/v1/calls?status=unknown", nil, 200, &unknown)
	if len(unknown.Items) != 1 || unknown.Items[0].Id != id || unknown.Items[0].FinishedAt != nil {
		t.Fatalf("unfinished attempt not visible: %+v", unknown)
	}
}
