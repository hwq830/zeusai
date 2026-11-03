package workflows

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hwq830/zeusai/server/internal/llm"
	"github.com/hwq830/zeusai/server/internal/search"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

type qualityProvider struct {
	result string
	err    error
}

func (p qualityProvider) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{Content: p.result}, p.err
}

func TestVisualRequiresEvidence(t *testing.T) {
	p := ReportPlan{}
	findings := []QuestionNotes{{Question: "Q", Notes: "2025 搜索 25% [1]", Sources: []search.Result{{URL: "https://example.test"}}}}
	a := &Activities{LLM: qualityProvider{result: `{"charts":[{"type":"bar","title":"渠道","unit":"%","period":"2025","points":[{"label":"搜索","value":25,"source":1},{"label":"社区","value":80,"source":1}]}]}`}}
	got, err := a.PlanReportVisuals(context.Background(), p, findings)
	require.NoError(t, err)
	require.Empty(t, got)
	findings[0].Notes = "2025 搜索 25% 社区 80% [1]"
	got, err = a.PlanReportVisuals(context.Background(), p, findings)
	require.NoError(t, err)
	require.Contains(t, got, "zeus-chart")
}

func TestReviewValidation(t *testing.T) {
	cases := []struct {
		body string
		want string
		bad  bool
	}{
		{`{"status":"pass","issues":[]}`, "pass", false},
		{`{"status":"pass","issues":[{"severity":"high","evidence":"冲突","suggestion":"修正"}]}`, "revise", false},
		{`{"status":"pass","issues":[{"severity":"mystery","evidence":"x","suggestion":"y"}]}`, "", true},
		{`{"status":"revise","issues":[]}`, "", true},
		{`{"status":"pass"}`, "", true},
		{`{}`, "", true},
	}
	for _, tc := range cases {
		a := &Activities{LLM: qualityProvider{result: tc.body}}
		got, err := a.ReviewReport(context.Background(), ReportPlan{}, nil, reportDraft{})
		if tc.bad {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
			require.Equal(t, tc.want, got.Status)
		}
	}
}

func TestQualityPipelineOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name                              string
		first                             reportReview
		reviewErr, revisionErr, secondErr bool
		timeout                           bool
		want                              string
		revise, second, stillOpen         bool
	}{
		{name: "pass", first: reportReview{Status: "pass"}, want: "pass"},
		{name: "review unavailable", reviewErr: true, want: "unavailable"},
		{name: "review timeout", reviewErr: true, timeout: true, want: "unavailable"},
		{name: "one revision passes", first: reportReview{Status: "revise"}, revise: true, second: true, want: "pass"},
		{name: "revision fails", first: reportReview{Status: "revise"}, revise: true, revisionErr: true, want: "revise"},
		{name: "second review fails", first: reportReview{Status: "revise"}, revise: true, second: true, secondErr: true, want: "unavailable"},
		{name: "revision limit reached", first: reportReview{Status: "revise"}, revise: true, second: true, stillOpen: true, want: "revise"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			a := &Activities{}
			env.RegisterActivity(a)
			p := ReportPlan{}
			var findings []QuestionNotes
			var notes []string
			draft := reportDraft{Summary: "original", Markdown: "body"}
			revised := reportDraft{Summary: "revised", Markdown: "fixed"}
			env.OnActivity(a.DraftReport, mock.Anything, p, findings, notes).Return(draft, nil).Once()
			env.OnActivity(a.PlanReportVisuals, mock.Anything, p, findings).Return("", errors.New("visual unavailable")).Once()
			if tc.reviewErr {
				failure := error(errors.New("review unavailable"))
				if tc.timeout {
					failure = temporal.NewTimeoutError(enums.TIMEOUT_TYPE_START_TO_CLOSE, nil)
				}
				env.OnActivity(a.ReviewReport, mock.Anything, p, findings, draft).Return(reportReview{}, failure).Once()
			} else {
				env.OnActivity(a.ReviewReport, mock.Anything, p, findings, draft).Return(tc.first, nil).Once()
			}
			if tc.revise {
				if tc.revisionErr {
					env.OnActivity(a.ReviseReport, mock.Anything, p, findings, draft, tc.first).Return(reportDraft{}, errors.New("revision failed")).Once()
				} else {
					env.OnActivity(a.ReviseReport, mock.Anything, p, findings, draft, tc.first).Return(revised, nil).Once()
				}
			}
			if tc.second {
				if tc.secondErr {
					env.OnActivity(a.ReviewReport, mock.Anything, p, findings, revised).Return(reportReview{}, errors.New("review unavailable")).Once()
				} else if tc.stillOpen {
					env.OnActivity(a.ReviewReport, mock.Anything, p, findings, revised).Return(reportReview{Status: "revise"}, nil).Once()
				} else {
					env.OnActivity(a.ReviewReport, mock.Anything, p, findings, revised).Return(reportReview{Status: "pass"}, nil).Once()
				}
			}
			env.OnActivity(a.PublishReviewedReport, mock.Anything, p, findings, mock.Anything, mock.MatchedBy(func(r reportReview) bool { return r.Status == tc.want })).Return(nil).Once()
			env.ExecuteWorkflow(func(ctx workflow.Context) error {
				ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
				return reportQualityPipeline(ctx, p, findings, notes)
			})
			require.NoError(t, env.GetWorkflowError())
			env.AssertExpectations(t)
		})
	}
}
