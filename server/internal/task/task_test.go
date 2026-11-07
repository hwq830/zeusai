package task

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/platform/testdb"
)

func newUser(t *testing.T, q *db.Queries) db.User {
	t.Helper()
	u, err := q.CreateUser(context.Background(), db.CreateUserParams{Nickname: "测试", Timezone: "Asia/Shanghai"})
	require.NoError(t, err)
	return u
}

func newTask(t *testing.T, q *db.Queries, userID uuid.UUID, kind string) db.Task {
	t.Helper()
	task, err := q.CreateTask(context.Background(), db.CreateTaskParams{
		UserID: userID, Kind: kind, Title: "t", Input: json.RawMessage(`{}`),
	})
	require.NoError(t, err)
	return task
}

func TestFillFollowupIDs(t *testing.T) {
	pool := testdb.New(t)
	q := db.New(pool)
	ctx := context.Background()
	user := newUser(t, q)

	report, err := q.CreateReport(ctx, db.CreateReportParams{
		UserID: user.ID, Title: "r", Topic: "t", Depth: "quick", Focuses: []string{}, Language: "zh",
	})
	require.NoError(t, err)
	followupTask := newTask(t, q, user.ID, "followup")
	record, err := q.CreateReportFollowup(ctx, db.CreateReportFollowupParams{
		ReportID: report.ID, UserID: user.ID, TaskID: &followupTask.ID,
		Question: "q", Answer: "a", Sources: json.RawMessage(`[]`),
	})
	require.NoError(t, err)

	svc := &Service{Q: q}
	items := []Task{FromRow(followupTask)}
	require.NoError(t, svc.FillFollowupIDs(ctx, user.ID, items))
	require.NotNil(t, items[0].FollowupID)
	require.Equal(t, record.ID.String(), *items[0].FollowupID)

	// Other task kinds never carry the field.
	reportTask := newTask(t, q, user.ID, "report")
	plain := []Task{FromRow(reportTask)}
	require.NoError(t, svc.FillFollowupIDs(ctx, user.ID, plain))
	require.Nil(t, plain[0].FollowupID)

	// The record is scoped to its owner: another user's lookup stays empty.
	other := newUser(t, q)
	scoped := []Task{FromRow(followupTask)}
	require.NoError(t, svc.FillFollowupIDs(ctx, other.ID, scoped))
	require.Nil(t, scoped[0].FollowupID)

	// A deleted record leaves the field empty instead of failing the whole task response.
	_, err = pool.Exec(ctx, "DELETE FROM report_followups WHERE id = $1", record.ID)
	require.NoError(t, err)
	gone := []Task{FromRow(followupTask)}
	require.NoError(t, svc.FillFollowupIDs(ctx, user.ID, gone))
	require.Nil(t, gone[0].FollowupID)
}

func TestFillFollowupIDsIgnoresTasksWithoutFollowups(t *testing.T) {
	pool := testdb.New(t)
	q := db.New(pool)
	svc := &Service{Q: q}
	items := []Task{{ID: uuid.NewString(), Kind: "briefing"}}
	require.NoError(t, svc.FillFollowupIDs(context.Background(), uuid.New(), items))
	require.Nil(t, items[0].FollowupID)
	require.NoError(t, svc.FillFollowupIDs(context.Background(), uuid.New(), nil))
}
