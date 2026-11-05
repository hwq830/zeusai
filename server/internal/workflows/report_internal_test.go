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

func TestEvidenceReadsReplaceSoft404WithinBudget(t *testing.T) {
	sources := []search.Result{{URL: "https://a"}, {URL: "https://b"}, {URL: "https://c"}, {URL: "https://d"}, {URL: "https://e"}}
	a := &Activities{Browse: fakeReader{"https://a": "您访问的页面不见了！3秒后返回", "https://b": "usable first source", "https://c": "404 Not Found", "https://d": "usable second source", "https://e": "must not be read"}}
	texts, ok, attempted := a.readEvidencePages(context.Background(), sources, 2)
	if attempted != 4 || ok[0] || !ok[1] || ok[2] || !ok[3] || ok[4] || texts[0] != "" || texts[3] != "usable second source" {
		t.Fatalf("attempts=%d, statuses=%v, pages=%q", attempted, ok, texts)
	}
	if !usableEvidencePage(strings.Repeat("article ", 200) + "page not found") {
		t.Fatal("long articles mentioning error messages should not be treated as soft 404s")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, attempted = a.readEvidencePages(ctx, sources, 2)
	if attempted != 0 {
		t.Fatalf("cancelled context issued %d reads", attempted)
	}
}

func TestNumericExcerptsExcludeDocumentNavigationAndRegistration(t *testing.T) {
	page := "统一社会信用代码\n123456789012345678\n\n文档页数 12 页，下载需要 3 积分\n\n年度游客人数\n2024 年：42 万人次"
	excerpts := numericSourceExcerptsForQuery(page, 1, false, "年度游客人数")
	if len(excerpts) != 1 || !strings.Contains(excerpts[0].Text, "42 万人次") {
		t.Fatalf("unexpected excerpts: %v", excerpts)
	}
}
