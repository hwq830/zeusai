package workflows

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hwq830/zeusai/server/internal/browse"
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

type fakeReader map[string]string

func (f fakeReader) Read(_ context.Context, u string) (browse.Page, error) {
	if t, ok := f[u]; ok {
		return browse.Page{URL: u, Text: t}, nil
	}
	return browse.Page{}, errors.New("unreachable")
}

func TestReadPagesOnlyTopN(t *testing.T) {
	a := &Activities{Browse: fakeReader{"https://a": "全文 A", "https://c": "全文 C"}}
	src := []search.Result{{URL: "https://a"}, {URL: "https://b"}, {URL: "https://c"}}
	got := a.readPages(context.Background(), src, 2)
	if got[0] != "全文 A" || got[1] != "" || got[2] != "" {
		t.Fatalf("got %q", got)
	}
	if got := (&Activities{}).readPages(context.Background(), src, 2); len(got) != 3 || got[0] != "" {
		t.Fatalf("nil reader: %q", got)
	}
}
