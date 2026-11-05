package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/events"
	"github.com/hwq830/zeusai/server/internal/llm"
	reporttemplate "github.com/hwq830/zeusai/web/report-template"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// A maximum of one draft, visual plan, revision and two independent reviews.
// Temporal does not retry optional stages. A review may retry one truncated
// response; all calls remain subject to provider timeouts and the task budget.
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

// useVisualAgent switches the report pipeline between the P1 VisualSpec agent
// (PlanVisuals + deterministic insertion) and the legacy PlanReportVisuals.
// To roll back after a deployment, change this constant to false; the old
// activity and filter are intentionally kept.
const useVisualAgent = true

const (
	reportReviewResolved   = "resolved"
	reportReviewUnresolved = "unresolved"
)

func reportQualityPipeline(ctx workflow.Context, plan ReportPlan, findings []QuestionNotes, notes []string) error {
	var a *Activities
	optional := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 7 * time.Minute, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
	var draft reportDraft
	if err := workflow.ExecuteActivity(ctx, a.DraftReport, plan, findings, notes).Get(ctx, &draft); err != nil {
		return err
	}
	var specs []reporttemplate.VisualSpec
	if useVisualAgent {
		// The planner can only choose anchors that exist in the actual draft.
		headings := reportHeadings(draft.Markdown)
		if len(headings) > 0 {
			// Failure isolation: a model/parse/validation error is just "no charts".
			if err := workflow.ExecuteActivity(optional, a.PlanVisuals, plan, findings, headings).Get(optional, &specs); err != nil {
				specs = nil
			}
		}
		draft.Markdown, _, _ = insertVisuals(plan.TaskID, draft.Markdown, specs)
	} else {
		var visual string
		if err := workflow.ExecuteActivity(optional, a.PlanReportVisuals, plan, findings).Get(optional, &visual); err == nil && visual != "" {
			draft.Markdown += "\n\n" + visual
		}
	}
	reviewState := reportReviewResolved
	var review reportReview
	if err := workflow.ExecuteActivity(optional, a.ReviewReport, plan, findings, draft).Get(optional, &review); err != nil {
		review = reportReview{Status: "unavailable"}
		logReviewVerdict(plan.TaskID, 1, "unavailable", nil)
	}
	if review.Status == "revise" {
		var revised reportDraft
		if err := workflow.ExecuteActivity(optional, a.ReviseReport, plan, findings, draft, review).Get(optional, &revised); err == nil {
			draft = revised
			if useVisualAgent {
				// ReviseReport rewrites markdown, so charts must be re-inserted
				// by the same anchor; insertVisuals is idempotent.
				draft.Markdown, _, _ = insertVisuals(plan.TaskID, draft.Markdown, specs)
			}
			var recheck reportReview
			if err := workflow.ExecuteActivity(optional, a.ReviewReport, plan, findings, draft).Get(optional, &recheck); err != nil {
				review.Status = "unavailable"
				logReviewVerdict(plan.TaskID, 1, "unavailable", nil)
			} else {
				review = recheck
			}
		} else {
			reviewState = reportReviewUnresolved
			slog.Info("report revise unresolved", "task_id", plan.TaskID, "report_id", plan.ReportID, "reason", reviseFailureReason(err))
		}
	}
	return workflow.ExecuteActivity(ctx, a.PublishReviewedReport, plan, findings, draft, review, reviewState).Get(ctx, nil)
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
	out.Markdown = reporttemplate.TrimLeadingTitle(out.Markdown, p.Title)
	if err == nil && strings.TrimSpace(out.Markdown) == "" {
		err = fmt.Errorf("empty report draft")
	}
	return out, err
}

func (a *Activities) PlanReportVisuals(ctx context.Context, p ReportPlan, findings []QuestionNotes) (string, error) {
	merged, sources := mergeNotes(findings)
	if len(sources) == 0 || strings.TrimSpace(merged) == "" {
		slog.Info("report visual filtering", "task_id", p.TaskID, "candidates", 0, "accepted", 0, "reason", "no_sources")
		return "", nil
	}
	var out struct {
		Charts []json.RawMessage `json:"charts"`
	}
	_, err := llm.CompleteJSON(ctx, a.LLM, llm.Request{Purpose: "report.visual", Tier: llm.TierFast, MaxTokens: 6000, System: "仅依据研究笔记中可信、可比较的原始数值规划图表，证据充足时可以有多个，不要凑数量。仅允许 bar、dot 或 line，line 仅用于同口径的时间趋势，点标签必须是按时间递增且精度一致的 YYYY、YYYY-MM 或 YYYY-MM-DD，所有点必须来自同一来源，标题、scope、单位和统计范围都必须在每个数据点对应的证据段中明确出现；不得把不同口径、范围或预测和实际混连。对预测、估算数据必须填写 qualifier 并保持标注，统计范围明确时填写 scope，二者必须原样出现在每个点的证据段落中。输出 JSON：{\"charts\":[{\"type\":\"bar\",\"title\":\"...\",\"unit\":\"...\",\"period\":\"...\",\"points\":[{\"label\":\"...\",\"value\":1,\"source\":1}]}]}。每个点的标签、原始数值、单位、时间及来源编号必须同段出现；不得计算或补造。证据不足返回空数组。", Messages: []llm.Message{{Role: "user", Content: merged}}, UserID: &p.UserID, TaskID: &p.TaskID}, &out)
	if err != nil {
		slog.Warn("report visual planning failed", "task_id", p.TaskID, "stage", "model_or_parse")
		return "", err
	}
	if out.Charts == nil {
		slog.Warn("report visual planning failed", "task_id", p.TaskID, "stage", "schema")
		return "", fmt.Errorf("visual response missing charts array")
	}
	var b strings.Builder
	for _, c := range out.Charts {
		fmt.Fprintf(&b, "\n\n```zeus-chart\n%s\n```\n", c)
	}
	// This filter checks co-occurrence, not truth or semantic support.
	filtered := strings.TrimSpace(reporttemplate.FilterCharts(b.String(), p.Lang.Model(merged), len(sources)))
	slog.Info("report visual filtering", "task_id", p.TaskID, "candidates", len(out.Charts), "accepted", strings.Count(filtered, "```zeus-chart"))
	return filtered, nil
}

func (a *Activities) ReviewReport(ctx context.Context, p ReportPlan, findings []QuestionNotes, d reportDraft) (reportReview, error) {
	merged, _ := mergeNotes(findings)
	var out reportReview
	payload, _ := json.Marshal(map[string]any{"topic": p.Topic, "focuses": p.Focuses, "scope": p.Scope, "evidence": merged, "summary": d.Summary, "markdown": d.Markdown})
	req := llm.Request{Purpose: "report.review", Tier: llm.TierStrong, MaxTokens: 2400, DisableThinking: true, System: "你是独立报告审查者，只根据提供的证据判断。检查问题覆盖、引用、图表与正文数值单位时间、事实与推断和不确定性。不要假称核验原始网页。按严重程度只列最重要的 5 项问题，每项证据与建议各不超过 100 字；不要遗漏会影响结论的重要问题。输出 JSON：{\"status\":\"pass 或 revise\",\"issues\":[{\"severity\":\"high 或 medium 或 low\",\"evidence\":\"具体证据或缺失\",\"suggestion\":\"修订建议\"}]}。重要问题时 status=revise。", Messages: []llm.Message{{Role: "user", Content: string(payload)}}, UserID: &p.UserID, TaskID: &p.TaskID}
	resp, err := llm.CompleteJSON(ctx, a.LLM, req, &out)
	logReviewAttempt(p.TaskID, 1, req, resp, err)
	attempt := 1
	// Some compatible providers omit finish_reason. An incomplete JSON response
	// at the exact requested output limit is also strong evidence of truncation.
	var syntax *json.SyntaxError
	truncated := errors.Is(err, llm.ErrOutputTruncated) || (errors.As(err, &syntax) && strings.Contains(syntax.Error(), "unexpected end of JSON input") && resp.UsageKnown && resp.Usage.OutputTokens >= req.MaxTokens)
	// DeepSeek JSON mode can also return whitespace with finish_reason=stop.
	// A single bounded retry is safe; never turn an empty response into a pass.
	blank := err != nil && resp.Provider != "" && resp.FinishReason == "stop" && strings.TrimSpace(resp.Content) == ""
	if truncated || blank {
		timer := time.NewTimer(250 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return reportReview{}, ctx.Err()
		case <-timer.C:
		}
		// One retry only. Both calls remain metered and obey the task budget and
		// activity deadline; never treat a second truncated response as a pass.
		if truncated {
			req.MaxTokens = 4800
		}
		out = reportReview{}
		attempt = 2
		resp, err = llm.CompleteJSON(ctx, a.LLM, req, &out)
		logReviewAttempt(p.TaskID, 2, req, resp, err)
	}
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
	if len(out.Issues) > 5 {
		out.Issues = out.Issues[:5]
	}
	for _, i := range out.Issues {
		if (i.Severity != "high" && i.Severity != "medium" && i.Severity != "low") || strings.TrimSpace(i.Evidence) == "" || strings.TrimSpace(i.Suggestion) == "" {
			return reportReview{}, fmt.Errorf("invalid review issue")
		}
		if i.Severity == "high" || i.Severity == "medium" {
			out.Status = "revise"
		}
	}
	logReviewVerdict(p.TaskID, attempt, out.Status, out.Issues)
	return out, nil
}

func logReviewVerdict(taskID uuid.UUID, attempt int, status string, issues []reviewIssue) {
	high, medium, low := 0, 0, 0
	for _, issue := range issues {
		switch issue.Severity {
		case "high":
			high++
		case "medium":
			medium++
		case "low":
			low++
		}
	}
	slog.Info("report review verdict", "task_id", taskID, "attempt", attempt, "status", status,
		"issue_count", len(issues), "severity_high", high, "severity_medium", medium, "severity_low", low)
}

func logReviewAttempt(taskID uuid.UUID, attempt int, req llm.Request, resp llm.Response, err error) {
	slog.Info("report review model result", "task_id", taskID, "attempt", attempt,
		"provider", resp.Provider, "model", resp.Model, "max_tokens", req.MaxTokens,
		"finish_reason", resp.FinishReason, "output_tokens", resp.Usage.OutputTokens,
		"reasoning_tokens", resp.ReasoningTokens, "reasoning_content_present", resp.ReasoningContentPresent,
		"error_type", fmt.Sprintf("%T", err))
}

var errReviseEmptyOutput = errors.New("empty revised report")

const (
	reviseBaseTokens  = 16384
	reviseRetryTokens = 24576
)

const reviseSystemPrompt = `根据审查意见修订报告，只使用所给证据，保留来源编号。不确定的信息要明确说明。保留所有 ## / ### 章节标题原文不变；不得新增或删除章节标题。图表会由系统在修订后自动回填；不要输出图表块、不要提及图表标题或图注。summary 是给读者的报告摘要，必须概括报告结论本身，不是本次修改的说明；不得出现“修订”“按审查意见”“保留原有章节”“删除/修改了…”这类描述修改过程的措辞。返回 JSON：{"summary":"...","markdown":"..."}。`

const reviseSummaryRetryPrompt = reviseSystemPrompt + `
上一次 summary 写成了修改过程说明；这次 summary 必须只写报告结论摘要，不得提修改过程。`

var reviseSummaryChangelogWords = []string{
	"修订", "按审查意见", "保留原", "删除", "修改为", "调整为", "本次", "修改了", "删去", "删掉",
	"this revision", "per the review", "kept the original", "removed", "changed to", "adjusted to",
}

func reviseSummaryHasChangelog(summary string) bool {
	lower := strings.ToLower(summary)
	for _, word := range reviseSummaryChangelogWords {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

func (a *Activities) ReviseReport(ctx context.Context, p ReportPlan, findings []QuestionNotes, d reportDraft, r reportReview) (reportDraft, error) {
	merged, sources := mergeNotes(findings)
	cleanDraft := d
	cleanDraft.Markdown = reporttemplate.WithoutCharts(d.Markdown)
	payload, _ := json.Marshal(map[string]any{"topic": p.Topic, "evidence": merged, "draft": cleanDraft, "review": r})
	req := llm.Request{
		Purpose:   "report.revise",
		Tier:      llm.TierStrong,
		MaxTokens: reviseBaseTokens,
		System:    reviseSystemPrompt,
		Messages:  []llm.Message{{Role: "user", Content: string(payload)}},
		UserID:    &p.UserID,
		TaskID:    &p.TaskID,
	}
	var out reportDraft
	summaryFallback := false
	for attempt := 1; attempt <= 2; attempt++ {
		out = reportDraft{}
		_, err := llm.CompleteJSON(ctx, a.LLM, req, &out)
		if err != nil {
			if errors.Is(err, llm.ErrOutputTruncated) && attempt == 1 {
				// A revision must emit the whole markdown. Thinking competes
				// with the output budget, so the single retry disables it and
				// raises the cap. There is never a third call.
				req.MaxTokens = reviseRetryTokens
				req.DisableThinking = true
				continue
			}
			return reportDraft{}, err
		}
		if strings.TrimSpace(out.Markdown) == "" {
			return reportDraft{}, errReviseEmptyOutput
		}
		if !reviseSummaryHasChangelog(out.Summary) {
			break
		}
		if attempt == 1 {
			req.System = reviseSummaryRetryPrompt
			continue
		}
		// The second attempt also wrote a changelog. Keep the revised body,
		// but never show the process note as the reader-facing summary.
		summaryFallback = true
		break
	}
	if summaryFallback {
		out.Summary = d.Summary
		slog.Info("report summary fell back", "task_id", p.TaskID, "reason", "revision_changelog")
	}
	out.Summary = p.Lang.Model(out.Summary)
	out.Markdown = reporttemplate.FilterCharts(p.Lang.Model(out.Markdown), p.Lang.Model(merged), len(sources))
	out.Markdown = reporttemplate.TrimLeadingTitle(out.Markdown, p.Title)
	if strings.TrimSpace(out.Markdown) == "" {
		return reportDraft{}, errReviseEmptyOutput
	}
	return out, nil
}

func reviseFailureReason(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ToLower(err.Error())
	if errors.Is(err, llm.ErrOutputTruncated) || strings.Contains(message, "output_truncated") || strings.Contains(message, "output truncated") {
		return "output_truncated"
	}
	if errors.Is(err, errReviseEmptyOutput) || strings.Contains(message, "empty revised report") {
		return "empty_output"
	}
	return "model_error"
}

func (a *Activities) PublishReviewedReport(ctx context.Context, p ReportPlan, findings []QuestionNotes, d reportDraft, r reportReview, reviewState string) error {
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
	if reviewState != reportReviewResolved && reviewState != reportReviewUnresolved {
		reviewState = reportReviewResolved
	}
	state := reviewState
	if _, err := a.Q.CompleteReport(ctx, db.CompleteReportParams{ID: p.ReportID, UserID: p.UserID, Title: p.Title, Summary: d.Summary, Markdown: d.Markdown, Sources: src, SourceCount: int32(len(sources)), WordCount: int32(wordCount(p.Lang, reporttemplate.WithoutCharts(d.Markdown))), ReviewState: &state}); err != nil {
		return err
	}
	step := writeStep(p.UILang, len(p.Questions)+1)
	if err := a.emit(ctx, p.UserID, p.TaskID, events.Event{Type: events.TypeStepDone, Step: &step}); err != nil {
		return err
	}
	return a.emit(ctx, p.UserID, p.TaskID, events.Event{Type: events.TypeCompleted, ReportID: p.ReportID.String()})
}
