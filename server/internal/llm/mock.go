package llm

import (
	"context"
	"encoding/json"
	"fmt"
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
	case "template.draft":
		out = mockTemplateDraft(user)
	case "report.plan":
		out = mockReportPlan(user, req.System)
	case "report.research":
		out = mockResearch(user)
	case "report.write":
		out = mockWrite(user)
	case "report.visual":
		out = map[string]any{"charts": []any{}}
	case "report.review":
		out = map[string]any{"status": "pass", "issues": []any{}}
	case "report.revise":
		out = mockWrite(user)
	case "report.tools":
		out = mockToolCalls(user)
	case "report.followup":
		out = map[string]string{"answer": "根据报告内容，" + user + " 的答案见正文第一节 [1]。"}
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

var questionsRe = regexp.MustCompile(`(\d+) 个互不重叠的子问题`)

func mockReportPlan(topic, system string) any {
	n := 3
	if m := questionsRe.FindStringSubmatch(system); m != nil {
		fmt.Sscan(m[1], &n)
	}
	angles := []string{"现状与规模", "主要参与者", "技术路线", "政策环境", "商业模式", "风险", "未来趋势"}
	qs := []map[string]any{}
	for i := 0; i < n && i < len(angles); i++ {
		qs = append(qs, map[string]any{"question": topic + "：" + angles[i], "queries": []string{topic + " " + angles[i]}})
	}
	return map[string]any{"title": topic + "调研", "questions": qs}
}

func mockResearch(user string) any {
	type src struct {
		Index   int    `json:"index"`
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	var sources []src
	if m := sourcesRe.FindStringSubmatch(user); m != nil {
		_ = json.Unmarshal([]byte(m[1]), &sources)
	}
	var b strings.Builder
	for _, s := range sources {
		fmt.Fprintf(&b, "- %s [%d]\n", s.Content, s.Index)
	}
	return map[string]string{"notes": b.String()}
}

var notesRe = regexp.MustCompile(`(?s)<notes>(.*?)</notes>`)

func mockWrite(user string) any {
	notes := ""
	if m := notesRe.FindStringSubmatch(user); m != nil {
		notes = strings.ReplaceAll(m[1], "### ", "## ")
	}
	md := "## 核心结论\n\n- 结论一 [1]\n- 结论二\n\n" + notes + "## 风险与不确定性\n\n- 以上为模拟数据。\n\n| 指标 | 数值 |\n| --- | --- |\n| 示例 | 42 |\n"
	return map[string]string{"summary": "这是一份由模拟模型生成的报告摘要，用于开发联调。", "markdown": md}
}

func mockTemplateDraft(prompt string) any {
	return map[string]any{
		"name": "自定义简报", "description": prompt,
		"fields": []map[string]any{{"key": "keywords", "label": "关键词", "type": "tags", "required": true, "max": 5}},
		"sections": []map[string]any{
			{"key": "news", "title": "最新动态", "instruction": "{{keywords}} 的最新消息", "maxItems": 5},
			{"key": "analysis", "title": "观点与分析", "instruction": "关于 {{keywords}} 的评论和分析", "maxItems": 3},
		},
		"sources":         []map[string]any{{"type": "search", "value": "{{keywords}}"}},
		"defaultSchedule": map[string]any{"freq": "daily", "time": "08:00", "tz": "Asia/Shanghai"},
		"defaultLength":   "standard",
	}
}

// mockToolCalls calls the first offered tool once, passing the question as "query".
func mockToolCalls(user string) any {
	var in struct {
		Question string `json:"question"`
		Tools    []struct {
			Connector string `json:"connector"`
			Tool      string `json:"tool"`
		} `json:"tools"`
	}
	user = strings.TrimSuffix(strings.TrimPrefix(user, "<tools>"), "</tools>")
	_ = json.Unmarshal([]byte(user), &in)
	calls := []map[string]any{}
	if len(in.Tools) > 0 {
		calls = append(calls, map[string]any{"connector": in.Tools[0].Connector, "tool": in.Tools[0].Tool,
			"arguments": map[string]any{"query": in.Question}})
	}
	return map[string]any{"calls": calls}
}
