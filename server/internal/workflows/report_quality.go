package workflows

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/events"
	"github.com/hwq830/zeusai/server/internal/llm"
	reporttemplate "github.com/hwq830/zeusai/web/report-template"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// A maximum of one draft, visual plan, revision and two independent reviews.
// Optional model stages do not retry: retries inside the provider router remain
// subject to its configured timeouts and the existing task token budget.
type reportDraft struct {
	Summary  string `json:"summary"`
	Markdown string `json:"markdown"`
}
type reviewIssue struct {
	Severity   string `json:"severity"`
	Evidence   string `json:"evidence"`
	Suggestion string `json:"suggestion"`
}
type reportReview struct {
	Status string        `json:"status"`
	Issues []reviewIssue `json:"issues"`
}

func reportQualityPipeline(ctx workflow.Context, plan ReportPlan, findings []QuestionNotes, notes []string) error {
	var a *Activities
	optional := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 7 * time.Minute, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
	var draft reportDraft
	if err := workflow.ExecuteActivity(ctx, a.DraftReport, plan, findings, notes).Get(ctx, &draft); err != nil {
		return err
	}
	var visual string
	if err := workflow.ExecuteActivity(optional, a.PlanReportVisuals, plan, findings).Get(optional, &visual); err == nil && visual != "" {
		draft.Markdown += "\n\n" + visual
	}
	var review reportReview
	if err := workflow.ExecuteActivity(optional, a.ReviewReport, plan, findings, draft).Get(optional, &review); err != nil {
		review = reportReview{Status: "unavailable"}
	}
	if review.Status == "revise" {
		var revised reportDraft
		if err := workflow.ExecuteActivity(optional, a.ReviseReport, plan, findings, draft, review).Get(optional, &revised); err == nil {
			draft = revised
			var recheck reportReview
			if err := workflow.ExecuteActivity(optional, a.ReviewReport, plan, findings, draft).Get(optional, &recheck); err != nil {
				review.Status = "unavailable"
			} else {
				review = recheck
			}
		}
	}
	return workflow.ExecuteActivity(ctx, a.PublishReviewedReport, plan, findings, draft, review).Get(ctx, nil)
}

func (a *Activities) DraftReport(ctx context.Context, p ReportPlan, findings []QuestionNotes, notes []string) (reportDraft, error) {
	step := writeStep(p.UILang, len(p.Questions)+1)
	if err := a.emit(ctx, p.UserID, p.TaskID, events.Event{Type: events.TypeStepStarted, Step: &step}); err != nil {
		return reportDraft{}, err
	}
	merged, _ := mergeNotes(findings)
	sys, err := render(p.Lang, "write.md", map[string]any{"Title": p.Title, "Topic": p.Topic, "Focuses": p.Focuses, "Notes": notes, "LengthHint": reportLengthHint(p.Lang, p.Depth), "Scope": p.Scope, "Uncertainties": p.Uncertainties})
	if err != nil {
		return reportDraft{}, err
	}
	var out reportDraft
	_, err = llm.CompleteJSON(ctx, a.LLM, llm.Request{Purpose: "report.write", Tier: llm.TierStrong, System: sys + "\n只写正文，不输出图表代码块。", Messages: []llm.Message{{Role: "user", Content: "<notes>\n" + merged + "</notes>"}}, UserID: &p.UserID, TaskID: &p.TaskID}, &out)
	out.Summary = p.Lang.Model(out.Summary)
	out.Markdown = reporttemplate.WithoutCharts(p.Lang.Model(out.Markdown))
	if err == nil && strings.TrimSpace(out.Markdown) == "" {
		err = fmt.Errorf("empty report draft")
	}
	return out, err
}

func (a *Activities) PlanReportVisuals(ctx context.Context, p ReportPlan, findings []QuestionNotes) (string, error) {
	merged, sources := mergeNotes(findings)
	if len(sources) == 0 || strings.TrimSpace(merged) == "" {
		return "", nil
	}
	var out struct {
		Charts []json.RawMessage `json:"charts"`
	}
	_, err := llm.CompleteJSON(ctx, a.LLM, llm.Request{Purpose: "report.visual", Tier: llm.TierFast, MaxTokens: 6000, System: "仅依据研究笔记中可信、可比较的原始数值规划图表，证据充足时可以有多个，不要凑数量。仅允许 bar、dot 或 line，line 仅用于同口径的时间趋势，点标签必须是按时间递增且精度一致的 YYYY、YYYY-MM 或 YYYY-MM-DD，所有点必须来自同一来源，标题、scope、单位和统计范围都必须在每个数据点对应的证据段中明确出现；不得把不同口径、范围或预测和实际混连。对预测、估算数据必须填写 qualifier 并保持标注，统计范围明确时填写 scope，二者必须原样出现在每个点的证据段落中。输出 JSON：{\"charts\":[{\"type\":\"bar\",\"title\":\"...\",\"unit\":\"...\",\"period\":\"...\",\"points\":[{\"label\":\"...\",\"value\":1,\"source\":1}]}]}。每个点的标签、原始数值、单位、时间及来源编号必须同段出现；不得计算或补造。证据不足返回空数组。", Messages: []llm.Message{{Role: "user", Content: merged}}, UserID: &p.UserID, TaskID: &p.TaskID}, &out)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, c := range out.Charts {
		fmt.Fprintf(&b, "\n\n```zeus-chart\n%s\n```\n", c)
	}
	// This filter checks co-occurrence, not truth or semantic support.
	return strings.TrimSpace(reporttemplate.FilterCharts(b.String(), p.Lang.Model(merged), len(sources))), nil
}

func (a *Activities) ReviewReport(ctx context.Context, p ReportPlan, findings []QuestionNotes, d reportDraft) (reportReview, error) {
	merged, _ := mergeNotes(findings)
	var out reportReview
	payload, _ := json.Marshal(map[string]any{"topic": p.Topic, "focuses": p.Focuses, "scope": p.Scope, "evidence": merged, "summary": d.Summary, "markdown": d.Markdown})
	_, err := llm.CompleteJSON(ctx, a.LLM, llm.Request{Purpose: "report.review", Tier: llm.TierStrong, MaxTokens: 2400, System: "你是独立报告审查者，只根据提供的证据判断。检查问题覆盖、引用、图表与正文数值单位时间、事实与推断和不确定性。不要假称核验原始网页。输出 JSON：{\"status\":\"pass 或 revise\",\"issues\":[{\"severity\":\"high 或 medium 或 low\",\"evidence\":\"具体证据或缺失\",\"suggestion\":\"修订建议\"}]}。重要问题时 status=revise。", Messages: []llm.Message{{Role: "user", Content: string(payload)}}, UserID: &p.UserID, TaskID: &p.TaskID}, &out)
	if err != nil {
		return reportReview{}, err
	}
	if out.Status != "pass" && out.Status != "revise" {
		return reportReview{}, fmt.Errorf("invalid review status")
	}
	if out.Issues == nil {
		return reportReview{}, fmt.Errorf("review missing issues array")
	}
	if out.Status == "revise" && len(out.Issues) == 0 {
		return reportReview{}, fmt.Errorf("review missing issues")
	}
	if len(out.Issues) > 10 {
		out.Issues = out.Issues[:10]
	}
	for _, i := range out.Issues {
		if (i.Severity != "high" && i.Severity != "medium" && i.Severity != "low") || strings.TrimSpace(i.Evidence) == "" || strings.TrimSpace(i.Suggestion) == "" {
			return reportReview{}, fmt.Errorf("invalid review issue")
		}
		if i.Severity == "high" || i.Severity == "medium" {
			out.Status = "revise"
		}
	}
	return out, nil
}

func (a *Activities) ReviseReport(ctx context.Context, p ReportPlan, findings []QuestionNotes, d reportDraft, r reportReview) (reportDraft, error) {
	merged, sources := mergeNotes(findings)
	payload, _ := json.Marshal(map[string]any{"topic": p.Topic, "evidence": merged, "draft": d, "review": r})
	var out reportDraft
	_, err := llm.CompleteJSON(ctx, a.LLM, llm.Request{Purpose: "report.revise", Tier: llm.TierStrong, MaxTokens: 8192, System: "根据审查意见修订报告，只使用所给证据，保留来源编号。不确定的信息要明确说明。返回 JSON：{\"summary\":\"...\",\"markdown\":\"...\"}。", Messages: []llm.Message{{Role: "user", Content: string(payload)}}, UserID: &p.UserID, TaskID: &p.TaskID}, &out)
	if err != nil {
		return reportDraft{}, err
	}
	if strings.TrimSpace(out.Markdown) == "" {
		return reportDraft{}, fmt.Errorf("empty revised report")
	}
	out.Summary = p.Lang.Model(out.Summary)
	out.Markdown = reporttemplate.FilterCharts(p.Lang.Model(out.Markdown), p.Lang.Model(merged), len(sources))
	return out, nil
}

func (a *Activities) PublishReviewedReport(ctx context.Context, p ReportPlan, findings []QuestionNotes, d reportDraft, r reportReview) error {
	merged, sources := mergeNotes(findings)
	d.Markdown = reporttemplate.FilterCharts(d.Markdown, p.Lang.Model(merged), len(sources))
	if r.Status != "pass" || len(r.Issues) > 0 {
		if r.Status == "unavailable" {
			d.Markdown += p.Lang.Pick("\n\n## 审查说明\n\n独立质量审查暂不可用，请核对原始来源。", "\n\n## Review note\n\nIndependent quality review was unavailable; check original sources.")
			if len(r.Issues) > 0 {
				d.Markdown += p.Lang.Pick("\n\n此前审查发现的问题尚未完成复核：\n", "\n\nPreviously identified issues could not be rechecked:\n")
			}
		} else {
			d.Markdown += p.Lang.Pick("\n\n## 审查说明\n\n模型独立审查发现以下待核实问题；这不是对原始来源的事实核验：\n", "\n\n## Review note\n\nThe independent model review identified issues to check; it does not verify original sources:\n")
		}
		for _, i := range r.Issues {
			d.Markdown += "\n- " + clip(i.Severity, 20) + ": " + clip(i.Evidence, 300) + " " + clip(i.Suggestion, 300)
		}
	}
	if err := a.streamText(ctx, p.UserID, p.TaskID, d.Summary); err != nil {
		return err
	}
	src, _ := json.Marshal(orEmptySources(sources))
	if _, err := a.Q.CompleteReport(ctx, db.CompleteReportParams{ID: p.ReportID, UserID: p.UserID, Title: p.Title, Summary: d.Summary, Markdown: d.Markdown, Sources: src, SourceCount: int32(len(sources)), WordCount: int32(wordCount(p.Lang, reporttemplate.WithoutCharts(d.Markdown)))}); err != nil {
		return err
	}
	step := writeStep(p.UILang, len(p.Questions)+1)
	if err := a.emit(ctx, p.UserID, p.TaskID, events.Event{Type: events.TypeStepDone, Step: &step}); err != nil {
		return err
	}
	return a.emit(ctx, p.UserID, p.TaskID, events.Event{Type: events.TypeCompleted, ReportID: p.ReportID.String()})
}
