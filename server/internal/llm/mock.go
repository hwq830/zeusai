package llm

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Mock answers deterministically from the prompt so the whole pipeline runs without a model.
// It understands the call sites by Purpose; unknown purposes get an echo.
type Mock struct{}

var (
	sourcesRe = regexp.MustCompile(`(?s)<sources>(.*?)</sources>`)
	titlesRe  = regexp.MustCompile(`(?m)^- (.+)$`)
	clockRe   = regexp.MustCompile(`(\d{1,2})(?:[:：](\d{2})|\s*点(半)?)`)
)

func (Mock) Complete(_ context.Context, req Request) (Response, error) {
	user := ""
	if n := len(req.Messages); n > 0 {
		user = req.Messages[n-1].Content
	}
	var out any
	switch req.Purpose {
	case "briefing.section":
		out = mockSection(user)
	case "briefing.headline":
		titles := titlesRe.FindAllStringSubmatch(user, 2)
		h := "暂无重要更新"
		if len(titles) > 0 {
			parts := []string{}
			for _, t := range titles {
				parts = append(parts, t[1])
			}
			h = strings.Join(parts, "；")
		}
		out = map[string]string{"headline": h}
	case "subscription.draft":
		out = mockSubscriptionDraft(user)
	default:
		out = map[string]string{"text": "mock: " + user}
	}
	b, _ := json.Marshal(out)
	return Response{
		Content: string(b), Provider: "mock", Model: "mock-1",
		Usage: Usage{InputTokens: utf8.RuneCountInString(req.System + user), OutputTokens: utf8.RuneCountInString(string(b))},
	}, nil
}

func mockSection(user string) any {
	type src struct {
		Title   string `json:"title"`
		Snippet string `json:"snippet"`
	}
	var sources []src
	if m := sourcesRe.FindStringSubmatch(user); m != nil {
		_ = json.Unmarshal([]byte(m[1]), &sources)
	}
	type item struct {
		Title         string `json:"title"`
		Summary       string `json:"summary"`
		SourceIndexes []int  `json:"sourceIndexes"`
	}
	items := []item{}
	for i, s := range sources {
		if i >= 3 {
			break
		}
		items = append(items, item{Title: s.Title, Summary: s.Snippet, SourceIndexes: []int{i}})
	}
	return map[string]any{"items": items}
}

func mockSubscriptionDraft(prompt string) any {
	t := "08:00"
	if m := clockRe.FindStringSubmatch(prompt); m != nil {
		h := m[1]
		if len(h) == 1 {
			h = "0" + h
		}
		mm := "00"
		if m[2] != "" {
			mm = m[2]
		} else if m[3] != "" {
			mm = "30"
		}
		t = h + ":" + mm
	}
	if i := strings.IndexAny(prompt, ":："); i >= 0 {
		prompt = prompt[i+len(":"):]
	}
	var topics []string
	for _, p := range regexp.MustCompile(`[+＋,，、和]`).Split(prompt, -1) {
		if p = strings.TrimSpace(strings.Trim(p, "：: ")); p != "" {
			topics = append(topics, p)
		}
	}
	freq := "daily"
	if strings.Contains(prompt, "每周") {
		freq = "weekly"
	}
	return map[string]any{"name": "我的简报", "topics": topics, "freq": freq, "time": t}
}
