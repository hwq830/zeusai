package workflows

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/push"
)

// NotifyQuota sends the 80% / 100% token reminder once per quota month, after a task that
// used tokens finishes. Failures are swallowed by callers: a reminder never fails a task.
func (a *Activities) NotifyQuota(ctx context.Context, userID uuid.UUID) error {
	if a.Quota == nil {
		return nil
	}
	u, err := a.Q.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	th, t, err := a.Quota.NewThreshold(ctx, u)
	if err != nil || th == 0 {
		return err
	}
	return a.Push.Notify(ctx, userID, quotaNotice(th, t.Remaining(), int(t.ResetsAt.Month())))
}

func quotaNotice(threshold int, remaining int64, resetMonth int) push.Notification {
	n := push.Notification{Category: push.CategoryService, DeepLink: "zeus://usage"}
	month := time.Month(resetMonth).String()
	if threshold >= 100 {
		n.Title = "本月 token 额度已用完"
		n.Body = fmt.Sprintf("定时简报照常推送；新的调研报告需等到 %d 月 1 日额度重置。", resetMonth)
		n.TitleEn = "You've used this month's tokens"
		n.BodyEn = fmt.Sprintf("Scheduled briefings keep coming; new reports can start again when tokens reset on %s 1.", month)
		return n
	}
	n.Title = fmt.Sprintf("本月 token 额度已用 %d%%", threshold)
	n.Body = fmt.Sprintf("还剩约 %s token，%d 月 1 日重置。", wan(remaining), resetMonth)
	n.TitleEn = fmt.Sprintf("You've used %d%% of this month's tokens", threshold)
	n.BodyEn = fmt.Sprintf("About %s tokens left; they reset on %s 1.", compact(remaining), month)
	return n
}

// compact formats a token count for English: 1.2M, 350K.
func compact(n int64) string {
	switch {
	case n >= 1_000_000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1e6), ".0") + "M"
	case n >= 10_000:
		return fmt.Sprintf("%dK", n/1000)
	}
	return fmt.Sprint(n)
}

// wan formats a token count the way the App does: 万 above ten thousand.
func wan(n int64) string {
	if n < 10_000 {
		return fmt.Sprint(n)
	}
	return fmt.Sprintf("%d 万", n/10_000)
}
