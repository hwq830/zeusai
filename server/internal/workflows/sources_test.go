package workflows

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/hwq830/zeusai/server/internal/i18n"
	"github.com/hwq830/zeusai/server/internal/search"
)

func numbered(prefix string, n int) []search.Result {
	out := make([]search.Result, n)
	for i := range out {
		out[i] = search.Result{URL: fmt.Sprintf("https://%s.example/%d", prefix, i)}
	}
	return out
}

func TestResearchStepDetailReportsNewSources(t *testing.T) {
	ledger := &sourceLedger{}
	task := uuid.New()

	first := numbered("first", 8)
	detail := researchStepDetail(i18n.ZH, len(first), ledger.note(task, first))
	require.Equal(t, "读了 8 个来源 · 新增 8 个", detail)

	// Five of the second step's eight sources were already read by the first step.
	second := append(numbered("first", 5), numbered("second", 3)...)
	detail = researchStepDetail(i18n.ZH, len(second), ledger.note(task, second))
	require.Equal(t, "读了 8 个来源 · 新增 3 个", detail)

	// A step that only repeats earlier sources says so instead of pretending it is new.
	detail = researchStepDetail(i18n.ZH, len(second), ledger.note(task, second))
	require.Equal(t, "读了 8 个来源 · 均为已读", detail)
}

func TestResearchStepDetailEnglish(t *testing.T) {
	require.Equal(t, "Read 8 sources · 3 new", researchStepDetail(i18n.EN, 8, 3))
	require.Equal(t, "Read 8 sources · all already read", researchStepDetail(i18n.EN, 8, 0))
	require.Equal(t, "Read 0 sources", researchStepDetail(i18n.EN, 0, 0))
}

func TestResearchStepDetailTraditionalChinese(t *testing.T) {
	require.Equal(t, "讀了 8 個來源 · 新增 3 個", researchStepDetail(i18n.ZHHant, 8, 3))
	require.Equal(t, "讀了 8 個來源 · 均為已讀", researchStepDetail(i18n.ZHHant, 8, 0))
}

func TestResearchStepDetailEmptyStep(t *testing.T) {
	require.Equal(t, "读了 0 个来源", researchStepDetail(i18n.ZH, 0, 0))
}

func TestSourceLedgerIsPerTask(t *testing.T) {
	ledger := &sourceLedger{}
	first, second := uuid.New(), uuid.New()
	step := numbered("shared", 8)

	require.Equal(t, 8, ledger.note(first, step))
	require.Equal(t, 0, ledger.note(first, step))
	// Another report task never shares sources with this one.
	require.Equal(t, 8, ledger.note(second, step))

	require.Equal(t, 8, ledger.take(first))
	require.Zero(t, ledger.take(first))
	// After the report is done the URL is new again, so a stale entry cannot leak forever.
	require.Equal(t, 8, ledger.note(first, step))
	require.Equal(t, 8, ledger.take(first))
	require.Equal(t, 8, ledger.take(second))
}

func TestSourceLedgerCountsOneStepAtATime(t *testing.T) {
	ledger := &sourceLedger{}
	task := uuid.New()
	// Repeated URLs inside one step only count once, and empty URLs are ignored.
	step := []search.Result{{URL: "https://a"}, {URL: "https://a"}, {URL: ""}, {URL: "https://b"}}
	require.Equal(t, 2, ledger.note(task, step))
}

func TestSourceLedgerConcurrentSteps(t *testing.T) {
	ledger := &sourceLedger{}
	task := uuid.New()
	const steps, perStep = 8, 40
	var added atomic.Int64
	var wg sync.WaitGroup
	for step := 0; step < steps; step++ {
		wg.Add(1)
		go func(step int) {
			defer wg.Done()
			added.Add(int64(ledger.note(task, numbered(fmt.Sprintf("s%d", step), perStep))))
		}(step)
	}
	wg.Wait()
	require.Equal(t, int64(steps*perStep), added.Load())
	require.Equal(t, steps*perStep, ledger.take(task))
}
