package workflows

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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

func TestNumericExcerptsPreferRelevantMeasuredQuantitiesOverMetadata(t *testing.T) {
	page := "2025 年国内旅游统计标题\n索引 357A04 2026 0103\n发布时间 2026 01 26\n\n2025 年国内出游人数 65.22 亿人次，增长 16.2%。\n2025 年国内出游花费 6.30 万亿元，增长 9.5%。\n\n版权信息 2026 01 26"
	excerpts := numericSourceExcerptsForQuery(page, 1, false, "2025 年国内旅游统计人次与花费")
	var evidence string
	for _, e := range excerpts {
		evidence += e.Text
	}
	if !strings.Contains(evidence, "65.22") || !strings.Contains(evidence, "6.30") {
		t.Fatal("ranked excerpts dropped the source's measured quantities")
	}
}

func TestMergeNotesCarriesSourceReadStatusAndFullTimestamp(t *testing.T) {
	stamp := time.Date(2025, 1, 21, 16, 0, 0, 0, time.UTC)
	findings := []QuestionNotes{{Question: "Q", Notes: "fact [1]", Sources: []search.Result{{URL: "https://example.test/a", PublishedAt: &stamp}, {URL: "https://example.test/b"}}, SourceReadStatus: []string{"read", "failed"}}}
	merged, _ := mergeNotes(findings)
	for _, expected := range []string{`"url":"https://example.test/a"`, `"readStatus":"read"`, `"readStatus":"failed"`, `"publishedAt":"2025-01-21T16:00:00Z"`} {
		if !strings.Contains(merged, expected) {
			t.Fatalf("source metadata missing %s", expected)
		}
	}
	findings[0].SourceReadStatus = nil
	merged, _ = mergeNotes(findings)
	if !strings.Contains(merged, `"readStatus":"unknown"`) {
		t.Fatal("legacy status should remain unknown")
	}
}

func TestResearchSourcesIncludeURLReadStatusAndTimezone(t *testing.T) {
	stamp := time.Date(2026, 1, 25, 16, 0, 0, 0, time.UTC)
	sources := []search.Result{{URL: "https://example.test/read", Snippet: "a longer search snippet", PublishedAt: &stamp}, {URL: "https://example.test/fail", Snippet: "search result"}, {URL: "https://example.test/skip"}}
	got, status := researchSources(sources, []string{"short page", "", ""}, []bool{true, false, false}, 2)
	if got[0].URL != sources[0].URL || got[0].Content != "short page" || got[0].Date != "2026-01-25T16:00:00Z" {
		t.Fatalf("read source lost its URL, extracted text or timezone: %+v", got[0])
	}
	if strings.Join(status, ",") != "read,failed,not_attempted" || got[1].Content != "search result" {
		t.Fatalf("incorrect statuses: %v", status)
	}
}
