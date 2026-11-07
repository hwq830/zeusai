package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/hwq830/zeusai/server/internal/llm"
	reporttemplate "github.com/hwq830/zeusai/web/report-template"
)

const visualPlanningSystem = `你是报告插图规划器。只规划 1–6 个真正值得配图的位置；没有值得配的返回空数组，不要凑数。

只使用 notes 中能读到的数值：
- L1：单个数值直接来自 notes，可用 bar、barGrouped 或 line。
- L2：由 notes 中数值确定可复算（占比、同比、CAGR、累计等），必须写 formula 和 inputs。
- L3：估算/预测，必须写至少 2 条 assumptions、formula、inputs、metric_definition，且 anchor 必须落在趋势/展望/研判/情景/预测类章节；假设由你自拟但必须写清楚，不能把 L3 放进现状/数据/事实章节。
- 所有级别都必须写 metric_definition；数值不得凭空估算，不得与 notes 冲突。
- 每个 spec 的 anchor 必须从用户消息的 headings 列表里逐字选择，不得自拟、不得改写、不得添加序号以外的任何字符；找不到合适标题就不要返回该图。不得把锚点写成“文末”“附录”或模糊描述。
- 同一章节最多 1 张图；不同图的 anchor 可以相同，但总图数最多 6。

输出 JSON，不要 markdown：
{"visuals":[{"anchor":"章节标题","type":"bar|hbar|donut|quadrant|line|barGrouped","title":"图标题","unit":"单位","level":"L1|L2|L3","series":[{"name":"系列名","points":[{"label":"2024","value":100}]}],"x_label":"quadrant 必填","y_label":"quadrant 必填","center_big":"donut 可选","center_small":"donut 可选","label_width":200,"corners":["左上","右上","左下","右下"],"formula":"L2/L3 必填","inputs":["[1] ...","[3] ..."],"assumptions":["L3 假设1","L3 假设2"],"metric_definition":"这个数到底是什么","caption":"图下说明","axis_truncated":false}]}

字段要求：series 至少 1 条、每条至少 2 个点；L2/L3 必须有 formula 和 inputs；L3 必须有至少 2 条 assumptions 和 metric_definition；L1 不强制 formula。
图表选型：分类标签长或类别多 → hbar（单序列、2–10 点、按 value 降序、label ≤40 字）；构成与占比（前 N 名）→ donut（2–5 段、value 全为正）；两个维度的定位（竞争格局、优先级）→ quadrant（4–8 点、x/y ∈ [0,1]、每点 label ≤12 字、必须给 x_label 和 y_label）；时间序列 → line；常规比较 → bar。宁可不配图，也不要为了用某种图生造数据。证据不足返回 {"visuals":[]}。`

// maxVisualAttempts bounds visual planning retries. One retry absorbs a
// truncated or malformed model response without turning a report into a
// no-chart report on a single bad sample.
const maxVisualAttempts = 2

// firstVisualMaxTokens and retryVisualMaxTokens: a truncated response is the
// most common parse failure, so the retry gets more room.
const (
	firstVisualMaxTokens = 8000
	retryVisualMaxTokens = 12000
)

// visualPlanError labels a failed planning attempt for the degraded log.
type visualPlanError struct {
	stage string // model_or_parse, batch_schema or spec_validation
	kind  string // model, truncated, parse, schema or invalid_spec
	err   error
}

func (e *visualPlanError) Error() string { return e.stage + ": " + e.err.Error() }
func (e *visualPlanError) Unwrap() error { return e.err }

// visualModelErrorKind separates provider failures from unparseable output so
// the degraded log can be counted by cause.
func visualModelErrorKind(err error) string {
	var syntax *json.SyntaxError
	switch {
	case errors.Is(err, llm.ErrOutputTruncated):
		return "truncated"
	case errors.As(err, &syntax):
		return "parse"
	default:
		return "model"
	}
}

func visualFailure(err error) (stage, kind string) {
	var failure *visualPlanError
	if errors.As(err, &failure) {
		return failure.stage, failure.kind
	}
	return "model_or_parse", "unknown"
}

// PlanVisuals is the P1 visual-agent entry point. It only plans; it does not
// insert or render. A model/parse failure is retried once, then logged as
// "report visual degraded" and returned to the workflow, which publishes the
// report without charts. It deliberately does not fall back to the legacy
// PlanReportVisuals planner: that one appends charts after the last section
// instead of anchoring them, and validates less, so a silent quality drop is
// worse than a report with no charts and a degraded marker.
func (a *Activities) PlanVisuals(ctx context.Context, p ReportPlan, findings []QuestionNotes, headings []string) ([]reporttemplate.VisualSpec, error) {
	if len(headings) == 0 {
		// The planner cannot invent an anchor that will match the draft. Skip
		// the model call entirely rather than paying for guaranteed drops.
		logVisualsPlanned(p.TaskID, 0, 0, nil, 0)
		return nil, nil
	}
	merged, sources := mergeNotes(findings)
	if len(sources) == 0 || strings.TrimSpace(merged) == "" {
		logVisualsPlanned(p.TaskID, 0, 0, nil, 0)
		return nil, nil
	}

	questions := make([]string, 0, len(p.Questions))
	for _, q := range p.Questions {
		if strings.TrimSpace(q.Question) != "" {
			questions = append(questions, q.Question)
		}
	}
	payload, _ := json.Marshal(map[string]any{
		"title":         p.Title,
		"topic":         p.Topic,
		"focuses":       p.Focuses,
		"scope":         p.Scope,
		"uncertainties": p.Uncertainties,
		"questions":     questions,
		"headings":      headings,
		"notes":         merged,
	})

	maxTokens := firstVisualMaxTokens
	var lastErr error
	for attempt := 1; attempt <= maxVisualAttempts; attempt++ {
		valid, candidates, levels, err := a.planVisualsAttempt(ctx, p, payload, maxTokens)
		if err == nil && (candidates == 0 || len(valid) > 0) {
			logVisualsPlanned(p.TaskID, candidates, len(valid), levels, attempt)
			return valid, nil
		}
		if err == nil {
			// The model answered, but nothing it produced passed validation.
			err = &visualPlanError{stage: "spec_validation", kind: "invalid_spec",
				err: fmt.Errorf("all %d visual specs failed validation", candidates)}
		}
		lastErr = err
		stage, kind := visualFailure(err)
		if attempt < maxVisualAttempts {
			slog.Warn("report visual planning failed; retrying",
				"task_id", p.TaskID, "stage", stage, "attempt", attempt, "error_type", kind)
			maxTokens = retryVisualMaxTokens
			continue
		}
		slog.Warn("report visual degraded",
			"task_id", p.TaskID, "stage", stage, "attempt", attempt, "error_type", kind, "error", err)
	}
	return nil, lastErr
}

// planVisualsAttempt runs one model call and validates what came back.
func (a *Activities) planVisualsAttempt(ctx context.Context, p ReportPlan, payload []byte, maxTokens int) ([]reporttemplate.VisualSpec, int, map[string]int, error) {
	var out struct {
		Visuals []reporttemplate.VisualSpec `json:"visuals"`
		Charts  []reporttemplate.VisualSpec `json:"charts"` // tolerant fallback for models that copy the old key
	}
	_, err := llm.CompleteJSON(ctx, a.LLM, llm.Request{
		Purpose:   "report.visual",
		Tier:      llm.TierStrong,
		MaxTokens: maxTokens,
		System:    visualPlanningSystem,
		Messages:  []llm.Message{{Role: "user", Content: string(payload)}},
		UserID:    &p.UserID,
		TaskID:    &p.TaskID,
	}, &out)
	if err != nil {
		return nil, 0, nil, &visualPlanError{stage: "model_or_parse", kind: visualModelErrorKind(err), err: err}
	}

	raw := out.Visuals
	if len(raw) == 0 {
		raw = out.Charts
	}
	candidates := len(raw)
	if len(raw) > 6 {
		for i := 6; i < len(raw); i++ {
			slog.Info("report visual dropped", "task_id", p.TaskID, "index", i+1, "reason", "invalid_schema")
		}
		raw = raw[:6]
	}
	valid := make([]reporttemplate.VisualSpec, 0, len(raw))
	levels := map[string]int{}
	for i, spec := range raw {
		if err := reporttemplate.ValidateVisualSpec(spec); err != nil {
			slog.Info("report visual dropped", "task_id", p.TaskID, "index", i+1, "reason", "invalid_schema")
			continue
		}
		valid = append(valid, spec)
		levels[spec.Level]++
	}
	if len(valid) > 0 {
		if err := reporttemplate.ValidateVisualSpecs(valid); err != nil {
			return nil, candidates, levels, &visualPlanError{stage: "batch_schema", kind: "schema", err: err}
		}
	}
	return valid, candidates, levels, nil
}

// reportHeadings extracts only H2/H3 titles from the draft, outside fenced
// code blocks. The planner receives this exact list; insertion still matches
// the same text exactly.
func reportHeadings(markdown string) []string {
	var headings []string
	seen := map[string]bool{}
	fence := ""
	for _, line := range strings.Split(markdown, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			marker := trimmed[:3]
			if fence == "" {
				fence = marker
			} else if fence == marker {
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		level := 0
		for level < len(trimmed) && trimmed[level] == '#' {
			level++
		}
		if level != 2 && level != 3 {
			continue
		}
		title := strings.TrimSpace(trimmed[level:])
		if title != "" && !seen[title] {
			seen[title] = true
			headings = append(headings, title)
		}
	}
	return headings
}

// logVisualsPlanned is the success/skip terminal event. attempts is how many
// model calls the plan took (0 when the planner was skipped), so the degraded
// rate is count(report visual degraded) / (degraded + planned with attempts>0).
func logVisualsPlanned(taskID uuid.UUID, candidates, valid int, levels map[string]int, attempts int) {
	slog.Info("report visuals planned", "task_id", taskID, "candidates", candidates, "valid", valid,
		"levels", fmt.Sprintf("L1:%d,L2:%d,L3:%d", levels["L1"], levels["L2"], levels["L3"]), "attempts", attempts)
}

type visualInsertion struct {
	index int
	raw   string
}

// insertVisuals inserts rendered spec JSON blocks after exact heading anchors.
// It never appends to the end: a missing anchor drops that chart. Duplicate
// insertion is prevented by comparing normalized JSON, not raw whitespace.
func insertVisuals(taskID uuid.UUID, markdown string, specs []reporttemplate.VisualSpec) (string, int, int) {
	// Clear first: whatever an earlier draft/revision left behind is replaced.
	// This makes the final block count equal to the inserted valid specs, and
	// prevents stale blocks with rewritten titles from surviving the revision.
	markdown = reporttemplate.WithoutCharts(markdown)
	inserted, missing := 0, 0
	groups := map[string][]visualInsertion{}
	var order []string
	for i, spec := range specs {
		if err := reporttemplate.ValidateVisualSpec(spec); err != nil {
			slog.Info("report visual dropped", "task_id", taskID, "index", i+1, "reason", "invalid_schema")
			continue
		}
		if _, err := reporttemplate.RenderVisualSpec(spec); err != nil {
			slog.Info("report visual dropped", "task_id", taskID, "index", i+1, "reason", "render_error")
			continue
		}
		raw, err := json.Marshal(spec)
		if err != nil {
			slog.Info("report visual dropped", "task_id", taskID, "index", i+1, "reason", "render_error")
			continue
		}
		key := string(raw)
		anchor := strings.TrimSpace(strings.TrimLeft(spec.Anchor, "#"))
		if _, ok := groups[anchor]; !ok {
			order = append(order, anchor)
		}
		groups[anchor] = append(groups[anchor], visualInsertion{index: i + 1, raw: key})
	}

	lines := strings.Split(markdown, "\n")
	for _, anchor := range order {
		heading, ok := findVisualAnchor(lines, anchor)
		if !ok {
			for _, item := range groups[anchor] {
				slog.Info("report visual dropped", "task_id", taskID, "index", item.index, "reason", "missing_anchor")
				missing++
			}
			continue
		}
		position := heading + 1
		for position < len(lines) && strings.TrimSpace(lines[position]) == "" {
			position++
		}
		// Collapse blank lines left by a previous chart block, so clear-then-
		// insert does not grow a blank line on every revision.
		for position-2 > heading && strings.TrimSpace(lines[position-1]) == "" && strings.TrimSpace(lines[position-2]) == "" {
			lines = append(lines[:position-1], lines[position:]...)
			position--
		}
		insertion := make([]string, 0, len(groups[anchor])*4+1)
		if position == heading+1 {
			insertion = append(insertion, "")
		}
		for _, item := range groups[anchor] {
			insertion = append(insertion, "```zeus-chart", item.raw, "```", "")
		}
		lines = append(append(append([]string{}, lines[:position]...), insertion...), lines[position:]...)
		inserted += len(groups[anchor])
	}
	slog.Info("report visuals inserted", "task_id", taskID, "inserted", inserted, "anchors_missing", missing)
	return strings.Join(lines, "\n"), inserted, missing
}

// findVisualAnchor finds an exact heading text outside fenced code blocks. The
// anchor is the title text only; a leading "#" is tolerated, but no fuzzy match.
func findVisualAnchor(lines []string, anchor string) (int, bool) {
	target := strings.TrimSpace(strings.TrimLeft(anchor, "#"))
	if target == "" {
		return 0, false
	}
	fence := ""
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			marker := trimmed[:3]
			if fence == "" {
				fence = marker
			} else if fence == marker {
				fence = ""
			}
			continue
		}
		if fence != "" || !strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.TrimSpace(strings.TrimLeft(trimmed, "#")) == target {
			return i, true
		}
	}
	return 0, false
}
