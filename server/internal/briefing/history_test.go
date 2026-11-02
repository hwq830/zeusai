package briefing

import "testing"

func TestHistoryDate(t *testing.T) {
	for _, raw := range []string{"2026-02-30", "2026-13-01", "11/02/2026", "2026-1-2"} {
		if _, err := historyDate(raw); err == nil {
			t.Errorf("accepted invalid date %q", raw)
		}
	}
	if got, err := historyDate(""); err != nil || got != nil {
		t.Fatalf("empty: %v %v", got, err)
	}
	got, err := historyDate("2026-11-02")
	if err != nil || got == nil || got.Format("2006-01-02") != "2026-11-02" {
		t.Fatalf("valid: %v %v", got, err)
	}
}
