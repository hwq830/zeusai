package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/httpapi/gen"
	"github.com/hwq830/zeusai/server/internal/task"
)

func (s *Server) ListTasks(ctx context.Context, req gen.ListTasksRequestObject) (gen.ListTasksResponseObject, error) {
	lim, off := page(req.Params.Cursor, req.Params.Limit)
	rows, err := s.Tasks.List(ctx, userID(ctx), deref(req.Params.Active), lim, off)
	if err != nil {
		return nil, err
	}
	items := make([]task.Task, 0, len(rows))
	for _, r := range rows {
		items = append(items, task.FromRow(r))
	}
	if err := s.Tasks.FillFollowupIDs(ctx, userID(ctx), items); err != nil {
		return nil, err
	}
	return gen.ListTasks200JSONResponse{Items: recode[[]gen.Task](items), NextCursor: nextCursor(len(items), lim, off)}, nil
}

func (s *Server) GetTask(ctx context.Context, req gen.GetTaskRequestObject) (gen.GetTaskResponseObject, error) {
	t, err := s.Tasks.Get(ctx, userID(ctx), req.TaskId)
	if err != nil {
		return nil, err
	}
	items := []task.Task{task.FromRow(t)}
	if err := s.Tasks.FillFollowupIDs(ctx, userID(ctx), items); err != nil {
		return nil, err
	}
	return gen.GetTask200JSONResponse(recode[gen.Task](items[0])), nil
}

func (s *Server) CancelTask(ctx context.Context, req gen.CancelTaskRequestObject) (gen.CancelTaskResponseObject, error) {
	t, err := s.Tasks.Cancel(ctx, userID(ctx), req.TaskId)
	if err != nil {
		return nil, err
	}
	return gen.CancelTask202JSONResponse(recode[gen.Task](task.FromRow(t))), nil
}

func (s *Server) SendTaskMessage(ctx context.Context, req gen.SendTaskMessageRequestObject) (gen.SendTaskMessageResponseObject, error) {
	if err := s.Tasks.SendMessage(ctx, userID(ctx), req.TaskId, req.Body.Text); err != nil {
		return nil, err
	}
	return gen.SendTaskMessage204Response{}, nil
}

func (s *Server) ResolveConfirmation(ctx context.Context, req gen.ResolveConfirmationRequestObject) (gen.ResolveConfirmationResponseObject, error) {
	if err := s.Tasks.ResolveConfirm(ctx, userID(ctx), req.TaskId, req.ConfirmId, string(req.Body.Decision)); err != nil {
		return nil, err
	}
	return gen.ResolveConfirmation204Response{}, nil
}

func (s *Server) StreamTaskEvents(ctx context.Context, req gen.StreamTaskEventsRequestObject) (gen.StreamTaskEventsResponseObject, error) {
	t, err := s.Tasks.Get(ctx, userID(ctx), req.TaskId)
	if err != nil {
		return nil, err
	}
	after := deref(req.Params.After)
	if h := req.Params.LastEventID; h != nil {
		if n, err := strconv.ParseInt(*h, 10, 64); err == nil && n > after {
			after = n
		}
	}
	return sseStream{s: s, ctx: ctx, task: t, after: after}, nil
}

// sseStream writes the task's events as Server-Sent Events until a terminal event, the
// client disconnecting, or the task being finished with nothing left to send.
type sseStream struct {
	s     *Server
	ctx   context.Context
	task  db.Task
	after int64
}

const sseWait = 5 * time.Second

func (st sseStream) VisitStreamTaskEventsResponse(w http.ResponseWriter) error {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	bw := bufio.NewWriter(w)
	flush := func() error {
		if err := bw.Flush(); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}
	if err := flush(); err != nil {
		return err
	}

	ping := st.s.SSEPing
	if ping <= 0 {
		ping = 15 * time.Second
	}
	lastWrite := time.Now()
	after := st.after
	for {
		if st.ctx.Err() != nil {
			return nil
		}
		evs, next, err := st.s.Bus.Read(st.ctx, st.task.ID.String(), after, min(sseWait, ping))
		if err != nil {
			if st.ctx.Err() != nil {
				return nil
			}
			return err
		}
		for _, ev := range evs {
			data, _ := json.Marshal(ev)
			fmt.Fprintf(bw, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Type, data)
			if ev.Terminal() {
				return flush()
			}
		}
		after = next
		if len(evs) > 0 {
			lastWrite = time.Now()
			if err := flush(); err != nil {
				return nil
			}
			continue
		}
		// Nothing new: stop if the task already ended (e.g. its stream expired), else keep alive.
		if done, err := st.finished(after); err != nil || done {
			return flush()
		}
		if time.Since(lastWrite) >= ping {
			fmt.Fprint(bw, ": ping\n\n")
			lastWrite = time.Now()
			if err := flush(); err != nil {
				return nil
			}
		}
	}
}

func (st sseStream) finished(after int64) (bool, error) {
	t, err := st.s.Q.GetTask(st.ctx, db.GetTaskParams{ID: st.task.ID, UserID: st.task.UserID})
	if err != nil {
		return false, err
	}
	return task.Terminal(t.Status) && after >= t.LastEventID, nil
}
