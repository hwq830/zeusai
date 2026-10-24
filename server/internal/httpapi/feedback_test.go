package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/db"
)

// upload posts raw bytes as a feedback screenshot.
func (h *harness) upload(data []byte, wantStatus int) string {
	h.t.Helper()
	req, _ := http.NewRequest("POST", h.url+"/v1/feedback/attachments", bytes.NewReader(data))
	req.Header.Set("Content-Type", "image/jpeg")
	req.Header.Set("Authorization", "Bearer "+h.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		h.t.Fatalf("upload: status %d, want %d: %s", resp.StatusCode, wantStatus, raw)
	}
	h.conform(req, resp, raw)
	var out struct{ ID string }
	_ = json.Unmarshal(raw, &out)
	return out.ID
}

type ticketT struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Content     string `json:"content"`
	Status      string `json:"status"`
	Unread      bool   `json:"unread"`
	Attachments []struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	} `json:"attachments"`
	Replies []struct {
		Content string `json:"content"`
	} `json:"replies"`
}

func TestFeedback(t *testing.T) {
	h := setup(t)
	s := h.login(randomPhone())
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{1}, 100)...)

	h.upload([]byte("not an image"), 400)
	att := h.upload(jpeg, 201)

	var e apiError
	h.do("POST", "/v1/feedback", map[string]any{"type": "bug", "content": "短"}, 400, &e)
	var tk ticketT
	h.do("POST", "/v1/feedback", map[string]any{
		"type": "bug", "content": "简报推送时间不对，晚了一小时", "attachmentIds": []string{att}, "contact": "wx: zeus",
		"client": map[string]string{"appVersion": "1.0.0", "os": "iOS 18.1", "device": "iPhone16,2"},
	}, 201, &tk)
	if tk.Status != "new" || tk.Unread || len(tk.Attachments) != 1 || tk.Attachments[0].ID != att {
		t.Fatalf("ticket: %+v", tk)
	}
	// The screenshot link works and an upload cannot be claimed twice.
	resp, err := http.Get(tk.Attachments[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Equal(got, jpeg) {
		t.Fatalf("attachment: %d", resp.StatusCode)
	}
	h.do("POST", "/v1/feedback", map[string]any{"type": "bug", "content": "再提交一次同一张图", "attachmentIds": []string{att}}, 400, &e)

	// Another user can neither see the feedback nor claim the upload.
	h.login(randomPhone())
	h.do("GET", "/v1/feedback/"+tk.ID, nil, 404, &e)
	h.token = s.AccessToken

	// An official reply marks the feedback unread until the user opens it.
	fid := uuid.MustParse(tk.ID)
	if _, err := h.q.AdminCreateFeedbackReply(context.Background(), db.AdminCreateFeedbackReplyParams{FeedbackID: fid, Content: "已修复，下个版本生效"}); err != nil {
		t.Fatal(err)
	}
	var list struct {
		Items []ticketT `json:"items"`
	}
	h.do("GET", "/v1/feedback", nil, 200, &list)
	if len(list.Items) != 1 || !list.Items[0].Unread || list.Items[0].Status != "in_progress" {
		t.Fatalf("list: %+v", list)
	}
	h.do("GET", "/v1/feedback/"+tk.ID, nil, 200, &tk)
	if len(tk.Replies) != 1 || tk.Replies[0].Content != "已修复，下个版本生效" {
		t.Fatalf("detail: %+v", tk)
	}
	h.do("GET", "/v1/feedback/"+tk.ID, nil, 200, &tk)
	if tk.Unread {
		t.Fatal("still unread after opening")
	}

	// Blocked words are flagged for operators, not rejected.
	h.do("POST", "/v1/feedback", map[string]any{"type": "other", "content": "这里有" + blockedTerm + "内容"}, 201, &tk)
	f, err := h.q.AdminGetFeedback(context.Background(), uuid.MustParse(tk.ID))
	if err != nil || f.FlaggedReason == "" {
		t.Fatalf("flagged: %q %v", f.FlaggedReason, err)
	}
}

func TestFeedbackDailyLimit(t *testing.T) {
	h := setup(t)
	h.login(randomPhone())
	for i := 0; i < 20; i++ {
		h.do("POST", "/v1/feedback", map[string]any{"type": "suggestion", "content": "希望增加更多模板 " + strings.Repeat("!", i)}, 201, nil)
	}
	var e apiError
	h.do("POST", "/v1/feedback", map[string]any{"type": "suggestion", "content": "第二十一条反馈"}, 429, &e)
	if e.Code != "rate_limited" {
		t.Fatalf("error: %+v", e)
	}
}

func TestReportFeedback(t *testing.T) {
	h := setup(t)
	h.login(randomPhone())
	var tk struct {
		ID       string `json:"id"`
		ReportID string `json:"reportId"`
	}
	h.do("POST", "/v1/tasks", map[string]any{"kind": "report", "topic": "AI 眼镜市场", "depth": "quick"}, 202, &tk)
	h.streamEvents(tk.ID, 0)

	var e apiError
	path := "/v1/reports/" + tk.ReportID
	h.do("PUT", path+"/feedback", map[string]any{"rating": "great"}, 400, &e)
	h.do("PUT", path+"/feedback", map[string]any{"rating": "unsatisfied", "reasons": []string{"nope"}}, 400, &e)
	h.do("PUT", path+"/feedback", map[string]any{"rating": "unsatisfied", "reasons": []string{"shallow", "bad_sources", "shallow"}, "comment": "数据太旧"}, 204, nil)

	var rep struct {
		Feedback *struct {
			Rating  string   `json:"rating"`
			Reasons []string `json:"reasons"`
			Comment string   `json:"comment"`
		} `json:"feedback"`
	}
	h.do("GET", path, nil, 200, &rep)
	if rep.Feedback == nil || rep.Feedback.Rating != "unsatisfied" || len(rep.Feedback.Reasons) != 2 || rep.Feedback.Comment != "数据太旧" {
		t.Fatalf("feedback: %+v", rep.Feedback)
	}
	// Re-rating overwrites, and reasons are dropped for a satisfied rating.
	h.do("PUT", path+"/feedback", map[string]any{"rating": "satisfied", "reasons": []string{"slow"}}, 204, nil)
	rep.Feedback = nil
	h.do("GET", path, nil, 200, &rep)
	if rep.Feedback == nil || rep.Feedback.Rating != "satisfied" || len(rep.Feedback.Reasons) != 0 || rep.Feedback.Comment != "" {
		t.Fatalf("re-rated: %+v", rep.Feedback)
	}

	h.login(randomPhone())
	h.do("PUT", path+"/feedback", map[string]any{"rating": "satisfied"}, 404, &e)
}
