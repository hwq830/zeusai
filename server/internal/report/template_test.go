package report

import (
	"github.com/hwq830/zeusai/server/internal/db"
	"testing"
)

func TestTemplateDataReadingEstimate(t *testing.T) {
	for _, tc := range []struct {
		lang  string
		words int32
		want  int
	}{
		{"zh", 0, 0}, {"zh", 301, 2}, {"zh-Hant", 300, 1}, {"en", 201, 2},
	} {
		got := TemplateData(db.Report{Language: tc.lang, WordCount: tc.words}, true).ReadMinutes
		if got != tc.want {
			t.Errorf("%s/%d: got %d, want %d", tc.lang, tc.words, got, tc.want)
		}
	}
}
