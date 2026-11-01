package modelgw

import "testing"

func TestBailianCatalog(t *testing.T) {
	models, updated := BailianCatalog()
	if len(models) < 50 || updated == "" {
		t.Fatalf("catalog: %d models, updated %q", len(models), updated)
	}
	if p, ok := BailianPrice("qwen3.8-max"); !ok || p.Input != 12 || p.Output != 36 || p.Cached != 1.5 {
		t.Fatalf("qwen3.8-max price %+v %v", p, ok)
	}
	if p, ok := BailianPrice("qwen-plus"); !ok || p.Input != 0.8 || p.Output != 2 {
		t.Fatalf("tiered qwen-plus should meter at its first tier: %+v %v", p, ok)
	}
	for _, u := range []string{"https://dashscope.aliyuncs.com/compatible-mode/v1", "https://llm-x.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"} {
		if !IsBailian(u) {
			t.Fatalf("%s should be Bailian", u)
		}
	}
	if IsBailian("https://api.deepseek.com/v1") || IsBailian("https://evil.com/dashscope.aliyuncs.com") {
		t.Fatal("non-Bailian address matched")
	}
	// Gaps in the routing file's prices are filled from the catalog on Bailian only.
	a := attempt{provider: "qwen", model: "qwen3.8-max"}
	if p := a.priceFor(upstream{bailian: true}, "qwen3.8-max"); p.Output != 36 {
		t.Fatalf("catalog fallback %+v", p)
	}
	if p := a.priceFor(upstream{}, "qwen3.8-max"); p != (Price{}) {
		t.Fatalf("non-Bailian %+v", p)
	}
	paged := attempt{provider: "qwen", model: "qwen3.8-max", ov: &Overrides{Prices: map[string]Price{"qwen3.8-max": {Output: 40}}}}
	if p := paged.priceFor(upstream{bailian: true}, "qwen3.8-max"); p.Output != 40 {
		t.Fatalf("pricing page should beat the built-in catalog %+v", p)
	}
	if p := paged.priceFor(upstream{}, "qwen3.8-max"); p != (Price{}) {
		t.Fatalf("Bailian list price used off Bailian %+v", p)
	}
	file := upstream{bailian: true, prices: map[string]Price{"qwen3.8-max": {Input: 1}}}
	if p := a.priceFor(file, "qwen3.8-max"); p.Input != 1 {
		t.Fatalf("file price should beat list prices %+v", p)
	}
	// The pricing page's admin price wins everywhere, on any provider.
	manual := attempt{provider: "deepseek", model: "deepseek-chat", ov: &Overrides{Manual: map[string]Price{"deepseek-chat": {Input: 2}}}}
	if p := manual.priceFor(upstream{prices: map[string]Price{"deepseek-chat": {Input: 9}}}, "deepseek-chat"); p.Input != 2 {
		t.Fatalf("admin price should win %+v", p)
	}
}
