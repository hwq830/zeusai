package workflows

import (
	"fmt"
	"sync"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/i18n"
	"github.com/hwq830/zeusai/server/internal/search"
)

// sourceLedger remembers which URLs one report task has already counted as sources, so each
// research step can report how many of its sources are new instead of repeating the cap.
//
// Research activities for a task run concurrently in one worker, hence the mutex; entries are
// per task ID and dropped by take once the report is written or aborted. The ledger is
// process-local: if the worker pool ever spans processes, this has to move to a shared store.
type sourceLedger struct {
	mu   sync.Mutex
	seen map[uuid.UUID]map[string]struct{}
}

// note records sources read by one research step and returns how many the task had not seen.
func (l *sourceLedger) note(taskID uuid.UUID, sources []search.Result) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen == nil {
		l.seen = map[uuid.UUID]map[string]struct{}{}
	}
	urls := l.seen[taskID]
	if urls == nil {
		urls = map[string]struct{}{}
		l.seen[taskID] = urls
	}
	added := 0
	for _, source := range sources {
		if source.URL == "" {
			continue
		}
		if _, ok := urls[source.URL]; ok {
			continue
		}
		urls[source.URL] = struct{}{}
		added++
	}
	return added
}

// take drops the task's ledger and returns how many unique sources it had recorded.
func (l *sourceLedger) take(taskID uuid.UUID) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(l.seen[taskID])
	delete(l.seen, taskID)
	return n
}

// researchStepDetail is a research step's subtitle: how many sources it read and how many of
// them the task had not read before. A step whose sources are all repeats still says so.
func researchStepDetail(l i18n.Lang, total, added int) string {
	switch {
	case total == 0:
		return l.Pick("读了 0 个来源", "Read 0 sources")
	case added == 0:
		return l.Pick(fmt.Sprintf("读了 %d 个来源 · 均为已读", total),
			fmt.Sprintf("Read %d sources · all already read", total))
	default:
		return l.Pick(fmt.Sprintf("读了 %d 个来源 · 新增 %d 个", total, added),
			fmt.Sprintf("Read %d sources · %d new", total, added))
	}
}
