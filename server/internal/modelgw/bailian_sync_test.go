package modelgw

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestFetchBailianCatalog(t *testing.T) {
	page, err := os.ReadFile("testdata/bailian_page.json")
	if err != nil {
		t.Fatal(err)
	}
	var form string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm.Get("params")
		_, _ = w.Write(page)
	}))
	defer srv.Close()
	models, err := FetchBailianCatalog(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(form, `"queryPrice":true`) || !strings.Contains(form, `"TG"`) {
		t.Fatalf("request params %s", form)
	}
	if len(models) != 4 {
		t.Fatalf("want 4 models (deduped), got %+v", models)
	}
	byName := map[string]CatalogModel{}
	for _, m := range models {
		byName[m.Model] = m
	}
	if p, ok := byName["qwen3.8-max"].Metered(); !ok || p != (Price{Input: 12, Output: 36, Cached: 1.5}) {
		t.Fatalf("qwen3.8-max %+v", p)
	}
	if m := byName["qwen3.8-max-preview"]; m.ContextWindow != 131072 || m.Tiers[0].Price.Cached != 10 {
		t.Fatalf("preview (string context, no cache price) %+v", m)
	}
	if m := byName["decision-model-preview"]; len(m.Tiers) != 0 || m.Vendor != "qwen-domain-model" {
		t.Fatalf("input-only price should leave the model unpriced %+v", m)
	}
	qp := byName["qwen-plus"]
	if len(qp.Tiers) != 2 || qp.Tiers[0].Price != (Price{Input: 0.8, Output: 2, Cached: 0.16}) || qp.Tiers[0].MaxTokens != 131072 ||
		qp.Tiers[0].Name != "≤128K" || qp.Tiers[1].Name != "128K–256K" || qp.Tiers[1].Price.Output != 20 {
		t.Fatalf("qwen-plus tiers %+v", qp.Tiers)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":"403","message":"denied"}`))
	}))
	defer bad.Close()
	if _, err := FetchBailianCatalog(context.Background(), bad.Client(), bad.URL); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("want error naming the message, got %v", err)
	}
}
