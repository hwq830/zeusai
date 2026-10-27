package push

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/uuid"
)

// The worked example from Aliyun's RPC signature documentation.
func TestSignRPCDocExample(t *testing.T) {
	params := map[string]string{
		"AccessKeyId":      "testid",
		"Action":           "DescribeRegions",
		"Format":           "XML",
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureNonce":   "3ee8c1b8-83d3-44af-a94f-4e0ad82fd6cf",
		"SignatureVersion": "1.0",
		"Timestamp":        "2016-02-23T12:46:24Z",
		"Version":          "2014-05-26",
	}
	if got := SignRPC(http.MethodGet, params, "testsecret"); got != "OLeaidS1JvxuMvnyHOwuJ+uX5qY=" {
		t.Fatalf("signature %s", got)
	}
}

func TestPercentEncode(t *testing.T) {
	if got := percentEncode("a b*c~d/é"); got != "a%20b%2Ac~d%2F%C3%A9" {
		t.Fatalf("got %s", got)
	}
}

func TestEMASParams(t *testing.T) {
	e := &EMAS{AppKey: "335", PopupActivity: "com.jasonhu.zeus.app.push.PushPopupActivity"}
	five := 5
	p := e.emasParams([]string{"d1", "d2"}, Notification{
		Category: CategoryReport, Title: "报告完成", Body: "b", DeepLink: "zeus://report/1", MessageID: "m1", Badge: &five,
	})
	want := map[string]string{
		"Action": "Push", "AppKey": "335", "Target": "DEVICE", "TargetValue": "d1,d2", "DeviceType": "ANDROID",
		"PushType": "NOTICE", "Title": "报告完成", "AndroidOpenType": "APPLICATION", "AndroidNotificationChannel": "report",
		"StoreOffline": "true", "AndroidRemind": "true", "AndroidPopupActivity": "com.jasonhu.zeus.app.push.PushPopupActivity",
		"AndroidNotificationVivoChannel": "1", "AndroidMessageHuaweiCategory": "WORK", "AndroidBadgeSetNum": "5",
	}
	for k, v := range want {
		if p[k] != v {
			t.Errorf("%s = %q, want %q", k, p[k], v)
		}
	}
	var ext map[string]string
	if err := json.Unmarshal([]byte(p["AndroidExtParameters"]), &ext); err != nil {
		t.Fatal(err)
	}
	if ext["deeplink"] != "zeus://report/1" || ext["message_id"] != "m1" || ext["category"] != "report" {
		t.Fatalf("ext %v", ext)
	}

	m := e.emasParams([]string{"d1"}, Notification{Category: CategoryMarketing, Title: "t", Body: "b"})
	if m["AndroidNotificationVivoChannel"] != "0" || m["AndroidMessageHuaweiCategory"] != "" || m["AndroidNotificationChannel"] != "marketing" {
		t.Fatalf("marketing params %v", m)
	}
	if s := e.emasParams([]string{"d1"}, Notification{Title: "t"}); s["AndroidNotificationChannel"] != "service" {
		t.Fatalf("empty category should be service, got %q", s["AndroidNotificationChannel"])
	}
}

func TestEMASCallSignsAndParses(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.PostForm
		_, _ = w.Write([]byte(`{"RequestId":"r","MessageId":"501"}`))
	}))
	defer srv.Close()
	e := &EMAS{AppKey: "335", AccessKeyID: "ak", AccessKeySecret: "sk", Endpoint: srv.URL}
	if err := e.call(context.Background(), e.emasParams([]string{"d1"}, Notification{Title: "t", Body: "b"})); err != nil {
		t.Fatal(err)
	}
	params := map[string]string{}
	for k := range got {
		params[k] = got.Get(k)
	}
	if params["AccessKeyId"] != "ak" || params["Version"] != "2016-08-01" || params["TargetValue"] != "d1" {
		t.Fatalf("params %v", params)
	}
	if SignRPC(http.MethodPost, params, "sk") != params["Signature"] {
		t.Fatal("signature does not verify")
	}
}

func TestEMASCallError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"Code":"SignatureDoesNotMatch","Message":"bad"}`))
	}))
	defer srv.Close()
	e := &EMAS{Endpoint: srv.URL}
	err := e.call(context.Background(), map[string]string{"Action": "Push"})
	if err == nil || err.Error() != "emas: HTTP 403 SignatureDoesNotMatch bad" {
		t.Fatalf("err %v", err)
	}
}

type fakeNotifier struct{ err error }

func (f fakeNotifier) Notify(context.Context, uuid.UUID, Notification) error { return f.err }

func TestMulti(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name string
		m    Multi
		want error
	}{
		{"one reached", Multi{fakeNotifier{ErrNoDevice}, fakeNotifier{nil}}, nil},
		{"reached despite failure", Multi{fakeNotifier{boom}, fakeNotifier{nil}}, nil},
		{"no devices", Multi{fakeNotifier{ErrNoDevice}, fakeNotifier{ErrNoDevice}}, ErrNoDevice},
		{"failure", Multi{fakeNotifier{ErrNoDevice}, fakeNotifier{boom}}, boom},
	}
	for _, c := range cases {
		err := c.m.Notify(context.Background(), uuid.New(), Notification{})
		if (c.want == nil) != (err == nil) || (c.want != nil && !errors.Is(err, c.want)) {
			t.Errorf("%s: err %v, want %v", c.name, err, c.want)
		}
	}
}
