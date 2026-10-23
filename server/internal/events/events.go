// Package events carries task progress from the orchestrator to the api process through a
// Redis Stream per task. The stream entry id is "<seq>-0", so seq doubles as the SSE id and a
// reconnecting client resumes with XREAD from its Last-Event-ID.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Type values mirror TaskEventType in api/openapi.yaml.
const (
	TypeStatus          = "status"
	TypePlan            = "plan"
	TypeStepStarted     = "step_started"
	TypeStepDone        = "step_done"
	TypeSourceReading   = "source_reading"
	TypeTextDelta       = "text_delta"
	TypeConfirmRequired = "confirm_required"
	TypeConfirmResolved = "confirm_resolved"
	TypeCompleted       = "completed"
	TypeFailed          = "failed"
	TypeCancelled       = "cancelled"
)

type Step struct {
	Index  int    `json:"index"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	Tool   string `json:"tool,omitempty"`
}

type Source struct {
	URL         string     `json:"url"`
	Title       string     `json:"title"`
	SiteName    string     `json:"siteName,omitempty"`
	PublishedAt *time.Time `json:"publishedAt,omitempty"`
}

type Confirm struct {
	ConfirmID   string     `json:"confirmId"`
	Title       string     `json:"title"`
	Detail      string     `json:"detail,omitempty"`
	ConnectorID string     `json:"connectorId,omitempty"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
}

type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// Event is the TaskEvent of the contract.
type Event struct {
	Seq        int64     `json:"seq"`
	Type       string    `json:"type"`
	TaskID     string    `json:"taskId"`
	At         time.Time `json:"at"`
	Status     string    `json:"status,omitempty"`
	Step       *Step     `json:"step,omitempty"`
	Steps      []Step    `json:"steps,omitempty"`
	Source     *Source   `json:"source,omitempty"`
	Delta      string    `json:"delta,omitempty"`
	Confirm    *Confirm  `json:"confirm,omitempty"`
	Decision   string    `json:"decision,omitempty"`
	ReportID   string    `json:"reportId,omitempty"`
	BriefingID string    `json:"briefingId,omitempty"`
	Error      *Error    `json:"error,omitempty"`
}

// Terminal reports whether no further events follow.
func (e Event) Terminal() bool {
	return e.Type == TypeCompleted || e.Type == TypeFailed || e.Type == TypeCancelled
}

const retention = 24 * time.Hour

type Bus struct{ R *redis.Client }

func streamKey(taskID string) string { return "task:" + taskID + ":events" }
func seqKey(taskID string) string    { return "task:" + taskID + ":seq" }

// appendScript assigns the next seq and appends atomically, so stream ids never go backwards
// even when several activities of one task publish concurrently.
var appendScript = redis.NewScript(`
local seq = redis.call('INCR', KEYS[2])
redis.call('XADD', KEYS[1], seq .. '-0', 'e', ARGV[1])
redis.call('EXPIRE', KEYS[1], ARGV[2])
redis.call('EXPIRE', KEYS[2], ARGV[2])
return seq
`)

// Publish appends the event and returns it with its assigned seq. The stored JSON has no seq;
// Read fills it in from the entry id.
func (b *Bus) Publish(ctx context.Context, ev Event) (Event, error) {
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return ev, err
	}
	seq, err := appendScript.Run(ctx, b.R, []string{streamKey(ev.TaskID), seqKey(ev.TaskID)}, string(data), int(retention/time.Second)).Int64()
	if err != nil {
		return ev, err
	}
	ev.Seq = seq
	return ev, nil
}

// Read returns events with seq > after, blocking up to wait when none are ready. It returns
// no events on timeout.
func (b *Bus) Read(ctx context.Context, taskID string, after int64, wait time.Duration) ([]Event, int64, error) {
	res, err := b.R.XRead(ctx, &redis.XReadArgs{
		Streams: []string{streamKey(taskID), fmt.Sprintf("%d-0", after)},
		Count:   100,
		Block:   wait,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return nil, after, nil
	}
	if err != nil {
		return nil, after, err
	}
	var out []Event
	for _, s := range res {
		for _, m := range s.Messages {
			seq, err := strconv.ParseInt(strings.TrimSuffix(m.ID, "-0"), 10, 64)
			if err != nil {
				continue
			}
			after = seq
			raw, _ := m.Values["e"].(string)
			var ev Event
			if json.Unmarshal([]byte(raw), &ev) != nil {
				continue
			}
			ev.Seq = seq
			out = append(out, ev)
		}
	}
	return out, after, nil
}
