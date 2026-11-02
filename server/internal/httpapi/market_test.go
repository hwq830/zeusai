package httpapi_test

import (
	"testing"
)

// The App reports the user's region; official templates and their sources follow it.
func TestMarketTemplates(t *testing.T) {
	h := setup(t)
	h.login(randomPhone())

	type tmpl struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Sources []struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"sources"`
	}
	var me struct {
		User struct {
			Market string `json:"market"`
		} `json:"user"`
	}
	h.do("GET", "/v1/me", nil, 200, &me)
	if me.User.Market != "other" {
		t.Fatalf("new user market = %q", me.User.Market)
	}
	list := func() map[string]tmpl {
		var page struct {
			Items []tmpl `json:"items"`
		}
		h.do("GET", "/v1/templates?limit=50", nil, 200, &page)
		out := map[string]tmpl{}
		for _, it := range page.Items {
			out[it.Name] = it
		}
		return out
	}
	got := list()
	if _, ok := got["台股早报"]; ok {
		t.Fatal("market-only template shown to other")
	}
	if _, ok := got["科技早报"]; !ok {
		t.Fatal("tech-morning missing")
	}

	h.do("PATCH", "/v1/me", map[string]any{"market": "XX"}, 400, nil)
	h.do("PATCH", "/v1/me", map[string]any{"market": "TW"}, 200, &me)
	if me.User.Market != "TW" {
		t.Fatalf("market = %q", me.User.Market)
	}
	got = list()
	if _, ok := got["台股早报"]; !ok {
		t.Fatal("TW template missing for TW user")
	}
	if _, ok := got["美股早报"]; ok {
		t.Fatal("US template shown to TW user")
	}
	tech := got["科技早报"]
	if len(tech.Sources) == 0 || tech.Sources[0].Value != "ithome.com.tw" {
		t.Fatalf("TW sources not applied: %+v", tech.Sources)
	}
	var one tmpl
	h.do("GET", "/v1/templates/"+tech.ID, nil, 200, &one)
	if one.Sources[0].Value != "ithome.com.tw" {
		t.Fatalf("get sources: %+v", one.Sources)
	}
	// A copy keeps the user's market sources as its own.
	var cp tmpl
	h.do("POST", "/v1/templates/"+tech.ID+"/copy", nil, 201, &cp)
	if cp.Sources[0].Value != "ithome.com.tw" {
		t.Fatalf("copy sources: %+v", cp.Sources)
	}
}
