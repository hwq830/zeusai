package workflows

import (
	"strings"
	"testing"

	"github.com/hwq830/zeusai/server/internal/search"
)

func TestMergeNotesRenumbersCitations(t *testing.T) {
	a := search.Result{URL: "https://a", Title: "A"}
	b := search.Result{URL: "https://b", Title: "B"}
	c := search.Result{URL: "https://c", Title: "C"}
	notes, sources := mergeNotes([]QuestionNotes{
		{Question: "Q1", Notes: "x [1] y [2]", Sources: []search.Result{a, b}},
		{Question: "Q2", Notes: "z [1] w [2] bad [7]", Sources: []search.Result{b, c}},
	})
	if len(sources) != 3 {
		t.Fatalf("sources: %+v", sources)
	}
	if !strings.Contains(notes, "x [1] y [2]") || !strings.Contains(notes, "z [2] w [3] bad") {
		t.Fatalf("notes:\n%s", notes)
	}
}
