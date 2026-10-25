package billing

import "testing"

func TestTokens(t *testing.T) {
	tab := Table{
		"qwen-max":   {Input: 1, Output: 1, Cached: 0.4},
		"qwen-turbo": {Input: 0.1, Output: 0.1, Cached: 0.05},
		DefaultModel: {Input: 2, Output: 2, Cached: 2},
	}
	cases := []struct {
		model                 string
		input, output, cached int
		want                  int64
	}{
		{"qwen-max", 1000, 200, 0, 1200},
		{"qwen-max", 1000, 200, 500, 500 + 200 + 200},
		{"qwen-turbo", 1000, 200, 0, 120},
		{"unknown", 100, 50, 0, 300},
		{"qwen-max", 10, 0, 20, 8}, // cached above input never goes negative
	}
	for _, c := range cases {
		if got := tab.Tokens(c.model, c.input, c.output, c.cached); got != c.want {
			t.Errorf("%s(%d,%d,%d) = %d, want %d", c.model, c.input, c.output, c.cached, got, c.want)
		}
	}
	if got := (Table{}).Tokens("x", 10, 5, 0); got != 15 {
		t.Errorf("empty table should bill at weight 1, got %d", got)
	}
	if got := (*Policy)(nil).Weights(nil).Tokens("x", 10, 5, 0); got != 15 {
		t.Errorf("nil weights should be neutral, got %d", got)
	}
}
