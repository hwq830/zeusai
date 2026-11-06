package workflows

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hwq830/zeusai/server/internal/events"
	"github.com/hwq830/zeusai/server/internal/i18n"
	"github.com/hwq830/zeusai/server/internal/orch"
	reporttemplate "github.com/hwq830/zeusai/web/report-template"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// Every report runs the same stages in the same order. The plan event carries the whole list
// up front, so a stage that ends up doing nothing (no revision needed, no charts) still has a
// step to complete with a detail instead of popping into the list mid-run.
type reportStage int

const (
	stagePlan reportStage = iota
	stageWrite
	stageVisuals
	stageReview
	stageRevise
	stageRecheck
	stagePublish
)

// stageIndex maps a stage to its 1-based step number. Research questions fill 2..N+1.
func stageIndex(questions int, stage reportStage) int {
	switch stage {
	case stagePlan:
		return 1
	case stageWrite:
		return questions + 2
	case stageVisuals:
		return questions + 3
	case stageReview:
		return questions + 4
	case stageRevise:
		return questions + 5
	case stageRecheck:
		return questions + 6
	default:
		return questions + 7
	}
}

func stageTitle(l i18n.Lang, stage reportStage) (title, tool string) {
	switch stage {
	case stagePlan:
		return l.Pick("制定调研计划", "Research plan"), "plan"
	case stageWrite:
		return l.Pick("撰写报告", "Writing the report"), "write"
	case stageVisuals:
		return l.Pick("编排图表", "Charts"), "chart"
	case stageReview:
		return l.Pick("质量审查", "Quality review"), "review"
	case stageRevise:
		return l.Pick("修订", "Revision"), "revise"
	case stageRecheck:
		return l.Pick("复审", "Re-review"), "check"
	default:
		return l.Pick("发布", "Publish"), "publish"
	}
}

func stageStep(l i18n.Lang, questions int, stage reportStage) events.Step {
	title, tool := stageTitle(l, stage)
	return events.Step{Index: stageIndex(questions, stage), Title: title, Tool: tool}
}

// reportSteps is the complete progress list emitted with the plan event.
func reportSteps(l i18n.Lang, questions []Question) []events.Step {
	steps := make([]events.Step, 0, len(questions)+7)
	steps = append(steps, stageStep(l, len(questions), stagePlan))
	for i, q := range questions {
		steps = append(steps, researchStep(l, i+2, q.Question))
	}
	for _, stage := range []reportStage{stageWrite, stageVisuals, stageReview, stageRevise, stageRecheck, stagePublish} {
		steps = append(steps, stageStep(l, len(questions), stage))
	}
	return steps
}

func planDetail(l i18n.Lang, questions int) string {
	return l.Pick(fmt.Sprintf("拆成 %d 个问题", questions), fmt.Sprintf("Split into %d questions", questions))
}

func writeStepDetail(lang, ui i18n.Lang, markdown string, sources int) string {
	return fmt.Sprintf(ui.Pick("%d 字，%d 个来源", "%d words, %d sources"),
		wordCount(lang, reporttemplate.WithoutCharts(markdown)), sources)
}

func visualsDetail(l i18n.Lang, planned int) string {
	if planned == 0 {
		return l.Pick("未发现适合配图的段落", "No paragraphs worth a chart")
	}
	return l.Pick(fmt.Sprintf("规划了 %d 张图", planned), fmt.Sprintf("Planned %d charts", planned))
}

func reviewDetail(l i18n.Lang, review reportReview) string {
	if review.Status == "unavailable" {
		return l.Pick("审查暂不可用", "Review unavailable")
	}
	if len(review.Issues) == 0 {
		return l.Pick("未发现问题", "No issues found")
	}
	high := 0
	for _, issue := range review.Issues {
		if issue.Severity == "high" {
			high++
		}
	}
	return l.Pick(fmt.Sprintf("发现 %d 个问题（%d 个高优先）", len(review.Issues), high),
		fmt.Sprintf("Found %d issues (%d high priority)", len(review.Issues), high))
}

func reviseDetail(l i18n.Lang, outcome string) string {
	switch outcome {
	case "done":
		return l.Pick("修订完成", "Revision complete")
	case "failed":
		return l.Pick("修订未完成", "Revision incomplete")
	case "skipped":
		return l.Pick("审查通过，无需修订", "Review passed, no revision needed")
	default:
		return l.Pick("审查暂不可用，未修订", "Review unavailable, no revision")
	}
}

func recheckDetail(l i18n.Lang, passed bool) string {
	if passed {
		return l.Pick("复审通过", "Re-review passed")
	}
	return l.Pick("复审未通过（已按现有策略发布）", "Re-review did not pass (published anyway)")
}

func publishDetail(l i18n.Lang) string {
	return l.Pick("已发布", "Published")
}

// emitStep records one progress event; every stage emits both step_started and step_done.
func emitStep(ctx workflow.Context, a *Activities, userID, taskID uuid.UUID, typ string, step events.Step) error {
	return workflow.ExecuteActivity(ctx, a.EmitEvent, userID, taskID, events.Event{Type: typ, Step: &step}).Get(ctx, nil)
}

func reportWorkflowStaged(ctx workflow.Context, run orch.ReportRun) error {
	var a *Activities
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: modelActivityTimeout,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	})

	var notes []string
	msgs := workflow.GetSignalChannel(ctx, orch.SignalMessage)
	workflow.Go(ctx, func(ctx workflow.Context) {
		for {
			var text string
			if !msgs.Receive(ctx, &text) {
				return
			}
			notes = append(notes, text)
		}
	})

	var plan ReportPlan
	err := workflow.ExecuteActivity(ctx, a.PlanReport, run).Get(ctx, &plan)
	if err == nil {
		step := stageStep(plan.UILang, len(plan.Questions), stagePlan)
		if err = emitStep(ctx, a, plan.UserID, plan.TaskID, events.TypeStepStarted, step); err == nil {
			step.Detail = planDetail(plan.UILang, len(plan.Questions))
			err = emitStep(ctx, a, plan.UserID, plan.TaskID, events.TypeStepDone, step)
		}
	}
	var results []QuestionNotes
	if err == nil {
		futures := make([]workflow.Future, len(plan.Questions))
		for i := range plan.Questions {
			futures[i] = workflow.ExecuteActivity(ctx, a.ResearchQuestion, plan, i, notes)
		}
		// Every question starts at once; a step is only completed when its own activity
		// returns, never because a later stage started.
		for i := range plan.Questions {
			if err = emitStep(ctx, a, plan.UserID, plan.TaskID, events.TypeStepStarted, researchStep(plan.UILang, i+2, plan.Questions[i].Question)); err != nil {
				break
			}
		}
		results = make([]QuestionNotes, len(futures))
		seen := map[string]struct{}{}
		for i, f := range futures {
			var got QuestionNotes
			if ferr := f.Get(ctx, &got); ferr != nil {
				if err == nil {
					err = ferr
				}
				continue
			}
			results[i] = got
			added := 0
			for _, source := range got.Sources {
				if source.URL == "" {
					continue
				}
				if _, ok := seen[source.URL]; ok {
					continue
				}
				seen[source.URL] = struct{}{}
				added++
			}
			step := researchStep(plan.UILang, i+2, plan.Questions[i].Question)
			step.Detail = researchStepDetail(plan.UILang, len(got.Sources), added)
			if perr := emitStep(ctx, a, plan.UserID, plan.TaskID, events.TypeStepDone, step); perr != nil && err == nil {
				err = perr
			}
		}
	}
	if err == nil {
		err = reportStagedPipeline(ctx, plan, results, notes)
	}
	if err != nil {
		dctx, _ := workflow.NewDisconnectedContext(ctx)
		kind := "failed"
		if temporal.IsCanceledError(err) {
			kind = "cancelled"
		}
		_ = workflow.ExecuteActivity(dctx, a.AbortReport, run, kind).Get(dctx, nil)
		return err
	}
	// The report is already saved; a failed push must not fail the task.
	_ = workflow.ExecuteActivity(ctx, a.NotifyReport, plan).Get(ctx, nil)
	_ = workflow.ExecuteActivity(ctx, a.NotifyQuota, plan.UserID).Get(ctx, nil)
	return nil
}

// reportStagedPipeline runs write → charts → review → (revise → re-review) → publish and
// reports every stage. Optional stages never fail the report, but they always complete their
// step so the progress list cannot stall.
func reportStagedPipeline(ctx workflow.Context, plan ReportPlan, findings []QuestionNotes, notes []string) error {
	var a *Activities
	optional := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 7 * time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1},
	})

	write := stageStep(plan.UILang, len(plan.Questions), stageWrite)
	if err := emitStep(ctx, a, plan.UserID, plan.TaskID, events.TypeStepStarted, write); err != nil {
		return err
	}
	var draft reportDraft
	if err := workflow.ExecuteActivity(ctx, a.DraftReport, plan, findings, notes).Get(ctx, &draft); err != nil {
		return err
	}
	_, sources := mergeNotes(findings)
	write.Detail = writeStepDetail(plan.Lang, plan.UILang, draft.Markdown, len(sources))
	if err := emitStep(ctx, a, plan.UserID, plan.TaskID, events.TypeStepDone, write); err != nil {
		return err
	}

	charts := stageStep(plan.UILang, len(plan.Questions), stageVisuals)
	if err := emitStep(ctx, a, plan.UserID, plan.TaskID, events.TypeStepStarted, charts); err != nil {
		return err
	}
	planned := 0
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
		planned = len(specs)
		draft.Markdown, _, _ = insertVisuals(plan.TaskID, draft.Markdown, specs)
	} else {
		var visual string
		if err := workflow.ExecuteActivity(optional, a.PlanReportVisuals, plan, findings).Get(optional, &visual); err == nil && visual != "" {
			draft.Markdown += "\n\n" + visual
			planned = strings.Count(visual, "```zeus-chart")
		}
	}
	charts.Detail = visualsDetail(plan.UILang, planned)
	if err := emitStep(ctx, a, plan.UserID, plan.TaskID, events.TypeStepDone, charts); err != nil {
		return err
	}

	reviewStep := stageStep(plan.UILang, len(plan.Questions), stageReview)
	if err := emitStep(ctx, a, plan.UserID, plan.TaskID, events.TypeStepStarted, reviewStep); err != nil {
		return err
	}
	var review reportReview
	if err := workflow.ExecuteActivity(optional, a.ReviewReport, plan, findings, draft).Get(optional, &review); err != nil {
		review = reportReview{Status: "unavailable"}
		logReviewVerdict(plan.TaskID, 1, "unavailable", nil)
		slog.Info("report review unavailable", "task_id", plan.TaskID, "report_id", plan.ReportID, "reason", "review_unavailable")
	}
	reviewStep.Detail = reviewDetail(plan.UILang, review)
	if err := emitStep(ctx, a, plan.UserID, plan.TaskID, events.TypeStepDone, reviewStep); err != nil {
		return err
	}

	reviseStep := stageStep(plan.UILang, len(plan.Questions), stageRevise)
	recheckStep := stageStep(plan.UILang, len(plan.Questions), stageRecheck)
	if review.Status == "revise" {
		if err := emitStep(ctx, a, plan.UserID, plan.TaskID, events.TypeStepStarted, reviseStep); err != nil {
			return err
		}
		var revised reportDraft
		if err := workflow.ExecuteActivity(optional, a.ReviseReport, plan, findings, draft, review).Get(optional, &revised); err == nil {
			draft = revised
			if useVisualAgent {
				// ReviseReport rewrites markdown, so charts must be re-inserted
				// by the same anchor; insertVisuals is idempotent.
				draft.Markdown, _, _ = insertVisuals(plan.TaskID, draft.Markdown, specs)
			}
			reviseStep.Detail = reviseDetail(plan.UILang, "done")
			if err := emitStep(ctx, a, plan.UserID, plan.TaskID, events.TypeStepDone, reviseStep); err != nil {
				return err
			}
			// A revision is only useful if the final review agrees.
			if err := emitStep(ctx, a, plan.UserID, plan.TaskID, events.TypeStepStarted, recheckStep); err != nil {
				return err
			}
			var recheck reportReview
			if err := workflow.ExecuteActivity(optional, a.ReviewReport, plan, findings, draft).Get(optional, &recheck); err != nil {
				review.Status = "unavailable"
				logReviewVerdict(plan.TaskID, 1, "unavailable", nil)
				slog.Info("report review unavailable", "task_id", plan.TaskID, "report_id", plan.ReportID, "reason", "review_unavailable")
			} else {
				review = recheck
			}
			recheckStep.Detail = recheckDetail(plan.UILang, review.Status == "pass")
			if err := emitStep(ctx, a, plan.UserID, plan.TaskID, events.TypeStepDone, recheckStep); err != nil {
				return err
			}
		} else {
			slog.Info("report revise unresolved", "task_id", plan.TaskID, "report_id", plan.ReportID, "reason", reviseFailureReason(err))
			reviseStep.Detail = reviseDetail(plan.UILang, "failed")
			if err := emitStep(ctx, a, plan.UserID, plan.TaskID, events.TypeStepDone, reviseStep); err != nil {
				return err
			}
			recheckStep.Detail = recheckDetail(plan.UILang, false)
			if err := emitStep(ctx, a, plan.UserID, plan.TaskID, events.TypeStepStarted, recheckStep); err != nil {
				return err
			}
			if err := emitStep(ctx, a, plan.UserID, plan.TaskID, events.TypeStepDone, recheckStep); err != nil {
				return err
			}
		}
	} else {
		if review.Status == "pass" {
			reviseStep.Detail = reviseDetail(plan.UILang, "skipped")
		} else {
			reviseStep.Detail = reviseDetail(plan.UILang, "unavailable")
		}
		if err := emitStep(ctx, a, plan.UserID, plan.TaskID, events.TypeStepStarted, reviseStep); err != nil {
			return err
		}
		if err := emitStep(ctx, a, plan.UserID, plan.TaskID, events.TypeStepDone, reviseStep); err != nil {
			return err
		}
		recheckStep.Detail = recheckDetail(plan.UILang, review.Status == "pass")
		if err := emitStep(ctx, a, plan.UserID, plan.TaskID, events.TypeStepStarted, recheckStep); err != nil {
			return err
		}
		if err := emitStep(ctx, a, plan.UserID, plan.TaskID, events.TypeStepDone, recheckStep); err != nil {
			return err
		}
	}

	state := reportReviewUnresolved
	if review.Status == "pass" {
		state = reportReviewResolved
	}
	publish := stageStep(plan.UILang, len(plan.Questions), stagePublish)
	if err := emitStep(ctx, a, plan.UserID, plan.TaskID, events.TypeStepStarted, publish); err != nil {
		return err
	}
	if err := workflow.ExecuteActivity(ctx, a.PublishReviewedReport, plan, findings, draft, review, state).Get(ctx, nil); err != nil {
		return err
	}
	publish.Detail = publishDetail(plan.UILang)
	return emitStep(ctx, a, plan.UserID, plan.TaskID, events.TypeStepDone, publish)
}
