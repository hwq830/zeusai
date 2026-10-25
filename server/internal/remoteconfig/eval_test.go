package remoteconfig

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestEvaluateFirstMatchWins(t *testing.T) {
	snap := &Snapshot{Version: 3, Keys: map[string]Key{
		"briefing.new_card": {Namespace: NSFeature, Type: TypeBool, Default: raw("false"), ApplyMode: ApplyNextLaunch, ClientVisible: true, Rules: []Rule{
			{ID: "staff", When: []Condition{{Attr: AttrUserID, Op: OpIn, Values: []string{"u1", "u2"}}}, Value: raw("true")},
			{ID: "old", When: []Condition{{Attr: AttrAppVersion, Op: OpSemverLt, Values: []string{"1.2"}}}, Value: raw("false")},
			{ID: "all", When: nil, Value: raw("true")},
		}},
		"server.only": {Namespace: NSQuota, Type: TypeInt, Default: raw("1"), ClientVisible: false},
	}}
	cases := []struct {
		c      Context
		value  string
		ruleID string
	}{
		{Context{UserID: "u1", AppVersion: "1.0"}, "true", "staff"},
		{Context{UserID: "u9", AppVersion: "1.1.9"}, "false", "old"},
		{Context{UserID: "u9", AppVersion: "1.2.0"}, "true", "all"},
		{Context{UserID: "u9"}, "true", "all"}, // no version: semver condition does not match
	}
	for _, tc := range cases {
		got := snap.Evaluate(tc.c, true)
		r := got["briefing.new_card"]
		if string(r.Value) != tc.value || r.RuleID != tc.ruleID {
			t.Errorf("%+v: got %s/%s, want %s/%s", tc.c, r.Value, r.RuleID, tc.value, tc.ruleID)
		}
		if _, ok := got["server.only"]; ok {
			t.Error("server-only key sent to client")
		}
	}
	if _, ok := snap.Evaluate(Context{}, false)["server.only"]; !ok {
		t.Error("server-only key missing for server reads")
	}
}

func TestRolloutIsStableAndMonotonic(t *testing.T) {
	key := func(p float64) *Snapshot {
		return &Snapshot{Keys: map[string]Key{"f.x": {Type: TypeBool, Default: raw("false"), Rules: []Rule{
			{ID: "r", Rollout: &Rollout{Percent: p, Value: raw("true")}},
		}}}}
	}
	in := func(s *Snapshot, user string) bool {
		r, _ := s.Lookup("f.x", Context{UserID: user})
		return string(r.Value) == "true"
	}
	five, twenty := key(5), key(20)
	n5, n20 := 0, 0
	for i := range 20000 {
		u := fmt.Sprintf("user-%d", i)
		a, b := in(five, u), in(twenty, u)
		if a && !b {
			t.Fatalf("%s left the rollout when it grew from 5%% to 20%%", u)
		}
		if a {
			n5++
		}
		if b {
			n20++
		}
	}
	if n5 < 800 || n5 > 1200 || n20 < 3700 || n20 > 4300 {
		t.Errorf("rollout shares off: 5%% → %d, 20%% → %d of 20000", n5, n20)
	}
	if in(key(0), "user-1") || !in(key(100), "user-1") {
		t.Error("0% and 100% must be exact")
	}
	// No user and no install: nothing to bucket on, falls through to the default.
	if r, _ := key(100).Lookup("f.x", Context{}); string(r.Value) != "false" {
		t.Errorf("anonymous without install id got %s", r.Value)
	}
	if r, _ := key(100).Lookup("f.x", Context{InstallID: "i-1"}); string(r.Value) != "true" {
		t.Error("install id should be bucketed before login")
	}
}

func TestBucketFormula(t *testing.T) {
	// Pinned so the App, analytics and this server keep agreeing on the formula.
	// python3 -c "import hashlib;print(int.from_bytes(hashlib.sha256(b'f.x:user-1').digest()[:8],'big')%10000)"
	if got := Bucket("f.x", "user-1"); got != 573 {
		t.Fatalf("Bucket(f.x, user-1) = %d, want 573", got)
	}
	if Bucket("a.b", "u") == Bucket("c.d", "u") && Bucket("a.b", "v") == Bucket("c.d", "v") && Bucket("a.b", "w") == Bucket("c.d", "w") {
		t.Error("different salts should give independent buckets")
	}
}

func TestConditions(t *testing.T) {
	reg := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	c := Context{UserID: "u1", AppVersion: "1.10.0", Platform: "ios", OSVersion: "17.4", Plan: "free", Region: "china", Locale: "zh-Hans", Channel: "testflight", RegisteredAt: &reg}
	cases := []struct {
		cond Condition
		want bool
	}{
		{Condition{AttrAppVersion, OpSemverGte, []string{"1.9"}}, true}, // numeric, not lexical
		{Condition{AttrAppVersion, OpSemverLt, []string{"1.10"}}, false},
		{Condition{AttrOSVersion, OpSemverGte, []string{"17.4.0"}}, true},
		{Condition{AttrPlatform, OpEq, []string{"iOS"}}, true},
		{Condition{AttrPlatform, OpNeq, []string{"android"}}, true},
		{Condition{AttrPlan, OpNotIn, []string{"free"}}, false},
		{Condition{AttrChannel, OpIn, []string{"testflight", "dev"}}, true},
		{Condition{AttrLocale, OpEq, []string{"en"}}, false},
		{Condition{AttrRegisteredAt, OpAfter, []string{"2026-10-01"}}, true},
		{Condition{AttrRegisteredAt, OpBefore, []string{"2026-10-01"}}, false},
		{Condition{AttrRegion, OpEq, []string{""}}, false},
	}
	for _, tc := range cases {
		if got := matches(tc.cond, c); got != tc.want {
			t.Errorf("%+v: got %v, want %v", tc.cond, got, tc.want)
		}
	}
	if matches(Condition{AttrRegisteredAt, OpAfter, []string{"2026-01-01"}}, Context{}) {
		t.Error("missing registered_at must not match")
	}
}

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
		ok   bool
	}{
		{"1.2", "1.2.0", 0, true},
		{"1.2.1", "1.10", -1, true},
		{"2.0.0-beta.1", "2.0", 0, true},
		{"v3", "2.99", 1, true},
		{"abc", "1", 0, false},
	} {
		got, ok := CompareVersions(tc.a, tc.b)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s vs %s: got %d %v", tc.a, tc.b, got, ok)
		}
	}
}

func TestValidateKey(t *testing.T) {
	notice := Key{Namespace: NSUI, Type: TypeJSON, Default: raw("null")}
	if err := ValidateKey("home.notice", notice, raw(noticeSchema)); err != nil {
		t.Fatal(err)
	}
	notice.Rules = []Rule{{ID: "r1", Value: raw(`{"text":"维护中","level":"loud"}`)}}
	if err := ValidateKey("home.notice", notice, raw(noticeSchema)); err == nil {
		t.Error("schema violation accepted")
	}
	bad := []Key{
		{Namespace: NSFeature, Type: TypeBool, Default: raw(`"yes"`)},
		{Namespace: NSQuota, Type: TypeInt, Default: raw("2.5")},
		{Namespace: NSEndpoint, Type: TypeString, Default: raw(`"http://api.example.com"`)},
		{Namespace: NSFeature, Type: TypeBool, Default: raw("false"), Rules: []Rule{{ID: "a", Value: raw("true")}, {ID: "a", Value: raw("true")}}},
		{Namespace: NSFeature, Type: TypeBool, Default: raw("false"), Rules: []Rule{{ID: "a", Rollout: &Rollout{Percent: 120, Value: raw("true")}}}},
		{Namespace: NSFeature, Type: TypeBool, Default: raw("false"), Rules: []Rule{{ID: "a", When: []Condition{{AttrPlatform, OpSemverGte, []string{"1"}}}, Value: raw("true")}}},
		{Namespace: NSFeature, Type: TypeBool, Default: raw("false"), Rules: []Rule{{ID: "a", When: []Condition{{AttrUserID, OpIn, nil}}, Value: raw("true")}}},
	}
	for i, k := range bad {
		if err := ValidateKey("x.y", k, nil); err == nil {
			t.Errorf("case %d accepted: %+v", i, k)
		}
	}
	if err := ValidateKey("x.y", Key{Namespace: NSEndpoint, Type: TypeString, Default: raw(`"https://api2.zeus.example.com"`)}, nil); err != nil {
		t.Error(err)
	}
	for _, b := range Builtins {
		var schema json.RawMessage
		if b.Schema != "" {
			schema = raw(b.Schema)
		}
		if err := ValidateDefinition(b.Key, b.Namespace, b.Type, b.ApplyMode); err != nil {
			t.Errorf("builtin %s: %v", b.Key, err)
		}
		if err := ValidateKey(b.Key, Key{Namespace: b.Namespace, Type: b.Type, Default: raw(b.Default)}, schema); err != nil {
			t.Errorf("builtin %s: %v", b.Key, err)
		}
	}
}

func TestDiffIgnoresFormatting(t *testing.T) {
	a := &Snapshot{Keys: map[string]Key{"a.b": {Type: TypeJSON, Default: raw(`{"x": 1}`), Rules: []Rule{}}}}
	b := &Snapshot{Keys: map[string]Key{"a.b": {Type: TypeJSON, Default: raw(`{"x":1}`)}, "c.d": {Type: TypeBool, Default: raw("true")}}}
	ch := Diff(a, b)
	if len(ch) != 1 || ch[0].Key != "c.d" || ch[0].Kind != "added" {
		t.Fatalf("diff = %+v", ch)
	}
	if ch := Diff(b, a); len(ch) != 1 || ch[0].Kind != "removed" {
		t.Fatalf("reverse diff = %+v", ch)
	}
}
