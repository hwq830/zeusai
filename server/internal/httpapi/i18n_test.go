package httpapi_test

import (
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hwq830/zeusai/server/internal/events"
	"github.com/hwq830/zeusai/server/internal/i18n"
)

var han = regexp.MustCompile(`\p{Han}`)

// An English-speaking user: the App sends Accept-Language: en. Errors, templates, progress
// steps, pushes and the report page come back in English, and the report language can be
// switched apart from the display language.
func TestEnglishUser(t *testing.T) {
	h := setup(t)
	h.lang = "en-US"
	h.login(randomPhone())

	var me struct {
		User struct {
			UILanguage     string `json:"uiLanguage"`
			ReportLanguage string `json:"reportLanguage"`
		} `json:"user"`
		Plan struct{ Name string } `json:"plan"`
	}
	h.do("GET", "/v1/me", nil, 200, &me)
	if me.User.UILanguage != "en" || me.User.ReportLanguage != "auto" || me.Plan.Name != "Free" {
		t.Fatalf("new English user: %+v", me)
	}

	var e apiError
	h.do("POST", "/v1/tasks", map[string]any{"kind": "report", "topic": ""}, 400, &e)
	if e.Message != "The research topic must be 1–500 characters" {
		t.Fatalf("error message: %+v", e)
	}
	h.do("PATCH", "/v1/me", map[string]any{"reportLanguage": "fr"}, 400, &e)
	if han.MatchString(e.Message) {
		t.Fatalf("error message: %+v", e)
	}

	var tl struct {
		Items []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Category string `json:"category"`
			Sections []struct{ Title string }
		} `json:"items"`
		Categories []string `json:"categories"`
	}
	h.do("GET", "/v1/templates", nil, 200, &tl)
	techMorning := ""
	for _, it := range tl.Items {
		if han.MatchString(it.Name + it.Category) {
			t.Errorf("untranslated template %+v", it)
		}
		if it.Name == "Tech Morning" {
			techMorning = it.ID
		}
	}
	if techMorning == "" || !strings.Contains(strings.Join(tl.Categories, ","), "Business") {
		t.Fatalf("templates: %+v", tl)
	}
	var biz struct {
		Items []struct{ Name string } `json:"items"`
	}
	h.do("GET", "/v1/templates?category=Business", nil, 200, &biz)
	if len(biz.Items) != 2 {
		t.Fatalf("filter by English category: %+v", biz)
	}

	// Report in English by default.
	type taskT struct {
		ID, Subtitle, ReportID string
	}
	var tk taskT
	h.do("POST", "/v1/tasks", map[string]any{"kind": "report", "topic": "EV exports from China in 2026", "depth": "quick"}, 202, &tk)
	if tk.Subtitle != "Quick report" {
		t.Fatalf("subtitle %q", tk.Subtitle)
	}
	for _, ev := range h.streamEvents(tk.ID, 0) {
		if ev.Type == events.TypePlan && ev.Steps[len(ev.Steps)-1].Title != "Writing the report" {
			t.Fatalf("plan steps: %+v", ev.Steps)
		}
	}
	var rep struct {
		Language string `json:"language"`
		HTMLURL  string `json:"htmlUrl"`
	}
	h.do("GET", "/v1/reports/"+tk.ReportID, nil, 200, &rep)
	if rep.Language != "en" {
		t.Fatalf("report language %q", rep.Language)
	}
	if _, page := h.raw(rep.HTMLURL, true); !strings.Contains(page, `<html lang="en">`) || !strings.Contains(page, "<h2>Sources</h2>") {
		t.Fatalf("report page: %.300s", page)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		var inbox struct {
			Items []struct{ Title string } `json:"items"`
		}
		h.do("GET", "/v1/messages", nil, 200, &inbox)
		if len(inbox.Items) > 0 {
			if inbox.Items[0].Title != "Your report is ready" {
				t.Fatalf("message: %+v", inbox.Items[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no report message")
		}
	}

	// Chinese reports while the App stays English; one report can still override it.
	h.do("PATCH", "/v1/me", map[string]any{"reportLanguage": "zh"}, 200, &me)
	if me.User.ReportLanguage != "zh" || me.User.UILanguage != "en" {
		t.Fatalf("patch: %+v", me)
	}
	h.do("POST", "/v1/tasks", map[string]any{"kind": "report", "topic": "新能源车出海", "depth": "quick"}, 202, &tk)
	h.streamEvents(tk.ID, 0)
	h.do("GET", "/v1/reports/"+tk.ReportID, nil, 200, &rep)
	if rep.Language != "zh" {
		t.Fatalf("report language %q", rep.Language)
	}
	h.do("POST", "/v1/tasks", map[string]any{"kind": "report", "topic": "EV exports", "depth": "quick", "language": "en"}, 202, &tk)
	h.streamEvents(tk.ID, 0)
	h.do("GET", "/v1/reports/"+tk.ReportID, nil, 200, &rep)
	if rep.Language != "en" {
		t.Fatalf("override language %q", rep.Language)
	}

	// Briefings follow the display language.
	var sub struct{ ID string }
	h.do("POST", "/v1/briefing-subscriptions", map[string]any{
		"templateId": techMorning, "name": "Morning", "schedule": map[string]any{"freq": "daily", "time": "08:00", "tz": "Asia/Shanghai"},
	}, 201, &sub)
	var run struct {
		ID         string `json:"id"`
		Subtitle   string `json:"subtitle"`
		BriefingID string `json:"briefingId"`
	}
	h.do("POST", "/v1/briefing-subscriptions/"+sub.ID+"/run", nil, 202, &run)
	if run.Subtitle != "Run now" {
		t.Fatalf("run subtitle %q", run.Subtitle)
	}
	h.streamEvents(run.ID, 0)
	var list struct {
		Items []struct{ ID string } `json:"items"`
	}
	h.do("GET", "/v1/briefings?subscriptionId="+sub.ID, nil, 200, &list)
	if len(list.Items) == 0 {
		t.Fatal("no briefing")
	}
	var b struct {
		Language string `json:"language"`
		Sections []struct{ Title string }
	}
	h.do("GET", "/v1/briefings/"+list.Items[0].ID, nil, 200, &b)
	if b.Language != "en" || len(b.Sections) == 0 {
		t.Fatalf("briefing: %+v", b)
	}
	for _, sec := range b.Sections {
		if han.MatchString(sec.Title) {
			t.Errorf("section title %q", sec.Title)
		}
	}
}

// A Traditional Chinese user (Accept-Language: zh-Hant-TW): errors, official templates and
// progress texts come back converted from the Simplified source.
func TestTraditionalChineseUser(t *testing.T) {
	h := setup(t)
	h.lang = "zh-Hant-TW"
	h.login(randomPhone())

	var me struct {
		User struct {
			UILanguage     string `json:"uiLanguage"`
			ReportLanguage string `json:"reportLanguage"`
		} `json:"user"`
	}
	h.do("GET", "/v1/me", nil, 200, &me)
	if me.User.UILanguage != "zh-Hant" || me.User.ReportLanguage != "auto" {
		t.Fatalf("new Traditional Chinese user: %+v", me)
	}

	var e apiError
	h.do("POST", "/v1/tasks", map[string]any{"kind": "report", "topic": ""}, 400, &e)
	if e.Message != "調研題目不能為空且不超過 500 字" {
		t.Fatalf("error message: %+v", e)
	}

	var tl struct {
		Items []struct {
			Name     string `json:"name"`
			Category string `json:"category"`
		} `json:"items"`
		Categories []string `json:"categories"`
	}
	h.do("GET", "/v1/templates", nil, 200, &tl)
	if len(tl.Items) == 0 || len(tl.Categories) == 0 {
		t.Fatalf("templates: %+v", tl)
	}
	if got := strings.Join(tl.Categories, ","); got != i18n.Hant(got) {
		t.Errorf("categories not converted: %s", got)
	}
	for _, it := range tl.Items {
		if it.Name != i18n.Hant(it.Name) {
			t.Errorf("template name not converted: %q", it.Name)
		}
	}
	var byCat struct {
		Items []struct{ Name string } `json:"items"`
	}
	h.do("GET", "/v1/templates?category="+url.QueryEscape(tl.Categories[0]), nil, 200, &byCat)
	if len(byCat.Items) == 0 {
		t.Fatalf("filter by Traditional category %q: no items", tl.Categories[0])
	}

	h.do("PATCH", "/v1/me", map[string]any{"uiLanguage": "zh", "reportLanguage": "zh-Hant"}, 200, &me)
	if me.User.UILanguage != "zh" || me.User.ReportLanguage != "zh-Hant" {
		t.Fatalf("switch languages: %+v", me)
	}
	h.lang = "zh-Hans"
	var tk struct{ ID, ReportID string }
	h.do("POST", "/v1/tasks", map[string]any{"kind": "report", "topic": "2026 年电动车出口", "depth": "quick"}, 202, &tk)
	h.streamEvents(tk.ID, 0)
	var rep struct {
		Language string `json:"language"`
	}
	h.do("GET", "/v1/reports/"+tk.ReportID, nil, 200, &rep)
	if rep.Language != "zh-Hant" {
		t.Fatalf("report language %q", rep.Language)
	}
}
