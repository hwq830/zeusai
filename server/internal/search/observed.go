package search

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/calllog"
)

// Observed wraps a single physical search attempt, including direct console tests.
type Observed struct {
	Searcher
	Calls              *calllog.Recorder
	Provider, Protocol string
	GroupID            uuid.UUID
	Attempt            int
	Timeout            time.Duration
}

func (o Observed) Search(ctx context.Context, q Query) (results []Result, err error) {
	trace, err := o.Calls.Start(ctx, calllog.Begin{GroupID: o.GroupID, Attempt: o.Attempt, Scope: calllog.FromContext(ctx),
		Kind: "search", Provider: o.Provider, Protocol: o.Protocol, Region: q.Region, Query: q.Q, Dispatched: true}, o.Timeout)
	if err != nil {
		return nil, err
	}
	info := &calllog.HTTPInfo{}
	ctx = calllog.WithHTTPInfo(ctx, info)
	defer func() {
		status, code := calllog.Failure(err, info.Status)
		var count *int
		if err == nil {
			n := len(results)
			count = &n
			if n == 0 {
				status = "empty"
			}
		}
		trace.Finish(ctx, calllog.End{Status: status, ErrorCode: code, RequestID: info.RequestID, HTTPStatus: info.Status, ResultCount: count})
	}()
	return o.Searcher.Search(ctx, q)
}
