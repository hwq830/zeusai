package market

import "testing"

func TestContentRegionAndMainland(t *testing.T) {
	for _, tc := range []struct{ value, fallback, want string }{
		{"US", CN, US}, {"global", CN, Global}, {"", CN, CN}, {"", Global, Global}, {"garbage", TW, TW}, {"other", CN, Other},
	} {
		if got := SearchRegion(tc.value, tc.fallback); got != tc.want {
			t.Errorf("%+v got %s", tc, got)
		}
	}
	if Normalize("cn") != CN || Country(CN) != "cn" || SearchLang(CN) != "zh-hans" {
		t.Fatal("mainland must be supported")
	}
}
