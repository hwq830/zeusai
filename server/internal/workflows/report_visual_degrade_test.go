package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/hwq830/zeusai/server/internal/llm"
	"github.com/hwq830/zeusai/server/internal/search"
	reporttemplate "github.com/hwq830/zeusai/web/report-template"
)

func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &logs
}

func visualFindings() []QuestionNotes {
	return []QuestionNotes{{
		Question: "Q",
		Notes:    "2024 100 万人，2025 200 万人 [1]",
		Sources:  []search.Result{{URL: "https://example.test"}},
	}}
}

func validVisualResponse(t *testing.T) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"visuals": []reporttemplate.VisualSpec{agentVisualSpec("一、规模", "L1", "bar")}})
	require.NoError(t, err)
	return string(body)
}

func TestPlanVisualsRetriesOnceThenSucceeds(t *testing.T) {
	logs := captureSlog(t)
	provider := &sequenceProvider{responses: []llm.Response{
		{Content: `{"visuals": [`},
		{Content: validVisualResponse(t)},
	}}
	taskID := uuid.New()
	a := &Activities{LLM: provider}
	got, err := a.PlanVisuals(context.Background(), ReportPlan{TaskID: taskID}, visualFindings(), []string{"一、规模"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Len(t, provider.requests, 2)
	require.Equal(t, firstVisualMaxTokens, provider.requests[0].MaxTokens)
	require.Equal(t, retryVisualMaxTokens, provider.requests[1].MaxTokens)

	require.Contains(t, logs.String(), "report visual planning failed; retrying")
	require.Contains(t, logs.String(), "attempt=1")
	require.Contains(t, logs.String(), "error_type=parse")
	require.NotContains(t, logs.String(), "report visual degraded")
	// Success is countable too: one plan, two model calls.
	require.Contains(t, logs.String(), "report visuals planned")
	require.Contains(t, logs.String(), "attempts=2")
}

func TestPlanVisualsDegradedAfterTwoFailures(t *testing.T) {
	logs := captureSlog(t)
	provider := &sequenceProvider{responses: []llm.Response{
		{Content: `{"visuals": [`},
		{Content: `{"visuals": [`},
	}}
	taskID := uuid.New()
	a := &Activities{LLM: provider}
	got, err := a.PlanVisuals(context.Background(), reportPlanWithTask(taskID), visualFindings(), []string{"一、规模"})
	require.Error(t, err)
	require.Empty(t, got)
	require.Len(t, provider.requests, 2)

	log := logs.String()
	require.Contains(t, log, "report visual degraded")
	require.Contains(t, log, "task_id="+taskID.String())
	require.Contains(t, log, "stage=model_or_parse")
	require.Contains(t, log, "attempt=2")
	require.Contains(t, log, "error_type=parse")
	require.NotContains(t, log, "report visuals planned")
}

func TestPlanVisualsDegradedOnProviderFailure(t *testing.T) {
	logs := captureSlog(t)
	a := &Activities{LLM: qualityProvider{err: errors.New("model down")}}
	taskID := uuid.New()
	_, err := a.PlanVisuals(context.Background(), reportPlanWithTask(taskID), visualFindings(), []string{"一、规模"})
	require.Error(t, err)
	require.Contains(t, logs.String(), "report visual degraded")
	require.Contains(t, logs.String(), "error_type=model")
	require.Contains(t, logs.String(), "attempt=2")
}

func TestPlanVisualsDegradedWhenEverySpecIsInvalid(t *testing.T) {
	logs := captureSlog(t)
	invalid := agentVisualSpec("一、规模", "L2", "bar")
	invalid.Formula = ""
	body, err := json.Marshal(map[string]any{"visuals": []reporttemplate.VisualSpec{invalid}})
	require.NoError(t, err)
	provider := &sequenceProvider{responses: []llm.Response{{Content: string(body)}, {Content: string(body)}}}
	a := &Activities{LLM: provider}
	got, err := a.PlanVisuals(context.Background(), ReportPlan{TaskID: uuid.New()}, visualFindings(), []string{"一、规模"})
	require.Error(t, err) // the workflow treats it as no charts, not as a failed report
	require.Empty(t, got)
	require.Len(t, provider.requests, 2)
	require.Contains(t, logs.String(), "report visual degraded")
	require.Contains(t, logs.String(), "stage=spec_validation")
	require.Contains(t, logs.String(), "error_type=invalid_spec")
}

func TestPlanVisualsEmptyPlanIsNotDegraded(t *testing.T) {
	logs := captureSlog(t)
	provider := &sequenceProvider{responses: []llm.Response{{Content: `{"visuals":[]}`}}}
	a := &Activities{LLM: provider}
	got, err := a.PlanVisuals(context.Background(), ReportPlan{TaskID: uuid.New()}, visualFindings(), []string{"一、规模"})
	require.NoError(t, err)
	require.Empty(t, got)
	require.Len(t, provider.requests, 1, "an empty plan is a valid answer, not a retry")
	require.Contains(t, logs.String(), "report visuals planned")
	require.Contains(t, logs.String(), "attempts=1")
	require.NotContains(t, logs.String(), "report visual degraded")
}

func reportPlanWithTask(taskID uuid.UUID) ReportPlan {
	return ReportPlan{TaskID: taskID}
}
