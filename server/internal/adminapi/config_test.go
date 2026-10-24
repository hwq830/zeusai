package adminapi_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/hwq830/zeusai/server/internal/adminapi"
	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/quota"
	"github.com/hwq830/zeusai/server/internal/remoteconfig"
)

func (h *harness) createAdmin(email, password, role string) {
	h.t.Helper()
	hash, err := adminapi.HashPassword(password)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.q.CreateAdminUser(context.Background(), db.CreateAdminUserParams{Email: email, PasswordHash: hash, Role: role}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) submit(note string, wantStatus gen.ConfigReleaseStatus) gen.ConfigReleaseDetail {
	h.t.Helper()
	var d gen.ConfigReleaseDetail
	h.do("POST", "/admin/v1/config/releases", map[string]any{"note": note}, 201, &d)
	if d.Status != wantStatus {
		h.t.Fatalf("submit %q: status %s, want %s", note, d.Status, wantStatus)
	}
	return d
}

func (h *harness) live(key string, c remoteconfig.Context) string {
	h.t.Helper()
	if err := h.config.Load(context.Background()); err != nil {
		h.t.Fatal(err)
	}
	r, ok := h.config.Current().Lookup(key, c)
	if !ok {
		return "<missing>"
	}
	return string(r.Value)
}

func TestConfigCenter(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	if _, err := remoteconfig.SeedBuiltins(ctx, h.q); err != nil {
		t.Fatal(err)
	}
	h.createOwner("owner@zeus.test", "correct-horse-battery")
	h.createAdmin("ops@zeus.test", "correct-horse-battery", "operator")
	h.createAdmin("viewer@zeus.test", "correct-horse-battery", "viewer")

	// ---- first release: the only owner publishes directly
	h.login("owner@zeus.test", "correct-horse-battery")
	var keys gen.ConfigKeyList
	h.do("GET", "/admin/v1/config/keys", nil, 200, &keys)
	if keys.PublishedVersion != 0 || len(keys.Items) != len(remoteconfig.Builtins) || !keys.Items[0].Unpublished {
		t.Fatalf("keys before first release = %+v", keys)
	}
	var changes gen.ConfigChanges
	h.do("GET", "/admin/v1/config/changes", nil, 200, &changes)
	if len(changes.Changes) != len(remoteconfig.Builtins) || !changes.PublishesDirectly {
		t.Fatalf("changes = %+v", changes)
	}
	v1 := h.submit("初始配置", gen.ConfigReleaseStatusPublished)
	if v1.Version == nil || *v1.Version != 1 || len(v1.Changes) != len(remoteconfig.Builtins) || v1.BaseVersion != 0 {
		t.Fatalf("v1 = %+v", v1)
	}
	h.do("POST", "/admin/v1/config/releases", map[string]any{}, 409, nil) // nothing changed
	if got := h.live(remoteconfig.KeyQuotaReportsPerMonth, remoteconfig.Context{}); got != "5" {
		t.Fatalf("live quota = %s", got)
	}

	// ---- a second owner turns on approvals for everyone else
	h.createOwner("owner2@zeus.test", "correct-horse-battery")

	// ---- operator: a kill switch goes live without approval
	h.login("ops@zeus.test", "correct-horse-battery")
	h.do("PATCH", "/admin/v1/config/keys/report.create_enabled", map[string]any{"defaultValue": false}, 200, nil)
	h.do("GET", "/admin/v1/config/changes", nil, 200, &changes)
	if !changes.PublishesDirectly || len(changes.Changes) != 1 {
		t.Fatalf("killswitch changes = %+v", changes)
	}
	v2 := h.submit("报告服务降级", gen.ConfigReleaseStatusPublished)
	if *v2.Version != 2 || v2.BaseVersion != 1 || len(v2.Changes) != 1 || v2.Changes[0].Key != "report.create_enabled" {
		t.Fatalf("v2 = %+v", v2)
	}
	if got := h.live(remoteconfig.KeyReportCreateEnabled, remoteconfig.Context{}); got != "false" {
		t.Fatalf("kill switch not live: %s", got)
	}

	// ---- operator: a gray release waits for an owner
	rules := []map[string]any{
		{"id": "staff", "when": []map[string]any{{"attr": "user_id", "op": "in", "values": []string{"u-staff"}}}, "value": true},
		{"id": "gray", "when": []map[string]any{{"attr": "app_version", "op": "semver_gte", "values": []string{"1.2.0"}}},
			"rollout": map[string]any{"percent": 100, "value": true}},
	}
	var k gen.ConfigKey
	h.do("PATCH", "/admin/v1/config/keys/template.sharing_enabled", map[string]any{"rules": rules, "owner": "小李"}, 200, &k)
	if !k.Unpublished || len(k.Rules) != 2 || k.Rules[1].Rollout == nil || k.Published == nil || len(k.Published.Rules) != 0 {
		t.Fatalf("patched key = %+v", k)
	}
	h.do("PATCH", "/admin/v1/config/keys/template.sharing_enabled", map[string]any{"defaultValue": "yes"}, 400, nil)
	h.do("PATCH", "/admin/v1/config/keys/template.sharing_enabled", map[string]any{"archived": true}, 403, nil)
	h.do("POST", "/admin/v1/config/keys", map[string]any{"key": "a.b", "namespace": "feature", "type": "bool", "defaultValue": true}, 403, nil)
	p3 := h.submit("模板分享灰度", gen.ConfigReleaseStatusPending)
	h.do("POST", fmt.Sprintf("/admin/v1/config/releases/%d/approve", p3.Id), map[string]any{}, 403, nil)
	if got := h.live(remoteconfig.KeyTemplateSharing, remoteconfig.Context{UserID: "u-staff"}); got != "false" {
		t.Fatalf("pending release already live: %s", got)
	}

	// ---- viewer reads but cannot write
	h.login("viewer@zeus.test", "correct-horse-battery")
	h.do("GET", fmt.Sprintf("/admin/v1/config/releases/%d", p3.Id), nil, 200, nil)
	h.do("POST", "/admin/v1/config/releases", map[string]any{}, 403, nil)

	// ---- owner2 approves
	h.login("owner2@zeus.test", "correct-horse-battery")
	var v3 gen.ConfigReleaseDetail
	h.do("POST", fmt.Sprintf("/admin/v1/config/releases/%d/approve", p3.Id), map[string]any{"note": "OK"}, 200, &v3)
	if v3.Status != gen.ConfigReleaseStatusPublished || *v3.Version != 3 || v3.DecidedByEmail != "owner2@zeus.test" || v3.CreatedByEmail != "ops@zeus.test" {
		t.Fatalf("v3 = %+v", v3)
	}
	h.do("POST", fmt.Sprintf("/admin/v1/config/releases/%d/approve", p3.Id), map[string]any{}, 409, nil)
	if got := h.live(remoteconfig.KeyTemplateSharing, remoteconfig.Context{UserID: "u-staff"}); got != "true" {
		t.Fatalf("staff rule: %s", got)
	}
	if got := h.live(remoteconfig.KeyTemplateSharing, remoteconfig.Context{UserID: "u-1", AppVersion: "1.1"}); got != "false" {
		t.Fatalf("old app: %s", got)
	}
	if got := h.live(remoteconfig.KeyTemplateSharing, remoteconfig.Context{UserID: "u-1", AppVersion: "1.2.1"}); got != "true" {
		t.Fatalf("new app: %s", got)
	}

	// ---- owners cannot approve their own release when there is another owner
	h.do("PATCH", "/admin/v1/config/keys/quota.free.reports_per_month", map[string]any{"defaultValue": 8}, 200, nil)
	p4 := h.submit("报告额度提到 8", gen.ConfigReleaseStatusPending)
	h.do("POST", fmt.Sprintf("/admin/v1/config/releases/%d/approve", p4.Id), map[string]any{}, 403, nil)
	h.login("owner@zeus.test", "correct-horse-battery")
	h.do("POST", fmt.Sprintf("/admin/v1/config/releases/%d/approve", p4.Id), map[string]any{}, 200, nil)
	phone := "+8613800009999"
	u, err := h.q.CreateUser(ctx, db.CreateUserParams{Nickname: "小张", Phone: &phone, Timezone: "Asia/Shanghai"})
	if err != nil {
		t.Fatal(err)
	}
	h.live(remoteconfig.KeyQuotaReportsPerMonth, remoteconfig.Context{})
	lim, _, err := (&quota.Service{Q: h.q, Config: h.config}).Effective(ctx, u)
	if err != nil || lim.ReportsPerMonth != 8 || lim.Subscriptions != 2 {
		t.Fatalf("effective quota = %+v, %v", lim, err)
	}
	var detail gen.UserDetail
	h.do("GET", "/admin/v1/users/"+u.ID.String(), nil, 200, &detail)
	if detail.Quota.Reports.PlanLimit != 8 {
		t.Fatalf("user detail plan limit = %d", detail.Quota.Reports.PlanLimit)
	}

	// ---- a new submission supersedes the pending one; rejecting closes it
	h.do("PATCH", "/admin/v1/config/keys/home.notice", map[string]any{"defaultValue": map[string]any{"text": "维护中", "level": "loud"}}, 400, nil)
	h.do("PATCH", "/admin/v1/config/keys/home.notice", map[string]any{"defaultValue": map[string]any{"text": "维护中", "level": "info"}}, 200, nil)
	h.createOwner("owner3@zeus.test", "correct-horse-battery") // more than one owner besides the submitter
	p5 := h.submit("公告", gen.ConfigReleaseStatusPending)
	h.do("PATCH", "/admin/v1/config/keys/home.notice", map[string]any{"defaultValueNull": true}, 200, nil)
	h.do("PATCH", "/admin/v1/config/keys/app.min_version", map[string]any{"defaultValue": "1.0.0"}, 200, nil)
	p6 := h.submit("最低版本", gen.ConfigReleaseStatusPending)
	var old gen.ConfigReleaseDetail
	h.do("GET", fmt.Sprintf("/admin/v1/config/releases/%d", p5.Id), nil, 200, &old)
	if old.Status != gen.ConfigReleaseStatusSuperseded {
		t.Fatalf("p5 = %s", old.Status)
	}
	h.login("owner2@zeus.test", "correct-horse-battery")
	h.do("POST", fmt.Sprintf("/admin/v1/config/releases/%d/reject", p6.Id), map[string]any{"note": "先不限制"}, 200, nil)

	// ---- rollback to v1 restores the working copy too
	var rb gen.ConfigReleaseDetail
	h.do("POST", fmt.Sprintf("/admin/v1/config/releases/%d/rollback", v1.Id), map[string]any{}, 201, &rb)
	h.login("owner@zeus.test", "correct-horse-battery")
	h.do("POST", fmt.Sprintf("/admin/v1/config/releases/%d/approve", rb.Id), map[string]any{}, 200, &rb)
	if *rb.Version != 5 || rb.RolledBackFrom == nil || *rb.RolledBackFrom != v1.Id || rb.Note != "回滚到 v1" {
		t.Fatalf("rollback = %+v", rb)
	}
	if got := h.live(remoteconfig.KeyReportCreateEnabled, remoteconfig.Context{}); got != "true" {
		t.Fatalf("rollback not live: %s", got)
	}
	h.do("GET", "/admin/v1/config/keys/report.create_enabled", nil, 200, &k)
	if k.Unpublished || k.DefaultValue == nil || (*k.DefaultValue) != true {
		t.Fatalf("working copy not restored: %+v", k)
	}

	// ---- preview against the working copy and the live version
	h.do("PATCH", "/admin/v1/config/keys/template.sharing_enabled", map[string]any{"rules": []map[string]any{
		{"id": "me", "when": []map[string]any{{"attr": "user_id", "op": "in", "values": []string{u.ID.String()}}}, "value": true},
	}}, 200, nil)
	var pv gen.ConfigPreview
	h.do("POST", "/admin/v1/config/preview", map[string]any{"phone": "13800009999", "source": "working"}, 200, &pv)
	if pv.UserId == nil || *pv.UserId != u.ID.String() || pv.Version != 0 {
		t.Fatalf("preview = %+v", pv)
	}
	found := false
	for _, v := range pv.Values {
		if v.Key == remoteconfig.KeyTemplateSharing {
			found = v.RuleId != nil && *v.RuleId == "me" && v.Value != nil && *v.Value == true
		}
	}
	if !found {
		t.Fatalf("preview values = %+v", pv.Values)
	}
	pv = gen.ConfigPreview{}
	h.do("POST", "/admin/v1/config/preview", map[string]any{"appVersion": "1.0"}, 200, &pv)
	if pv.Version != 5 || pv.UserId != nil {
		t.Fatalf("live preview = %+v", pv)
	}
	h.do("POST", "/admin/v1/config/preview", map[string]any{"phone": "139"}, 404, nil)

	// ---- create and archive a key (owner)
	h.do("POST", "/admin/v1/config/keys", map[string]any{"key": "briefing.new_card_layout", "namespace": "feature", "type": "bool",
		"defaultValue": false, "expose": true, "applyMode": "next_launch"}, 201, &k)
	h.do("POST", "/admin/v1/config/keys", map[string]any{"key": "briefing.new_card_layout", "namespace": "feature", "type": "bool", "defaultValue": false}, 409, nil)
	h.do("POST", "/admin/v1/config/keys", map[string]any{"key": "Bad Key", "namespace": "feature", "type": "bool", "defaultValue": false}, 400, nil)
	h.do("PATCH", "/admin/v1/config/keys/app.min_version", map[string]any{"archived": true}, 200, &k)
	h.do("GET", "/admin/v1/config/keys", nil, 200, &keys)
	for _, it := range keys.Items {
		if it.Key == "app.min_version" {
			t.Fatal("archived key listed")
		}
	}
	h.do("GET", "/admin/v1/config/changes", nil, 200, &changes)
	kinds := map[string]gen.ConfigChangeKind{}
	for _, c := range changes.Changes {
		kinds[c.Key] = c.Kind
	}
	if kinds["briefing.new_card_layout"] != "added" || kinds["app.min_version"] != "removed" || kinds["template.sharing_enabled"] != "changed" {
		t.Fatalf("changes = %+v", kinds)
	}

	// ---- history and audit
	var rels gen.ConfigReleasePage
	h.do("GET", "/admin/v1/config/releases?limit=3", nil, 200, &rels)
	if len(rels.Items) != 3 || rels.Items[0].Current == nil || !*rels.Items[0].Current || rels.NextCursor == nil {
		t.Fatalf("releases = %+v", rels)
	}
	var audit gen.AuditPage
	h.do("GET", "/admin/v1/audit-log?targetType=config_release&limit=100", nil, 200, &audit)
	actions := map[string]int{}
	for _, e := range audit.Items {
		actions[e.Action]++
	}
	if actions["config.publish"] != 2 || actions["config.approve"] != 3 || actions["config.reject"] != 1 || actions["config.rollback"] != 1 {
		t.Fatalf("audit = %+v", actions)
	}
}
