package reporttemplate

import (
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestStructureVisualUsesCitedReportText(t *testing.T) {
	markdown := "## Supply\n\nProduction is constrained by capacity [1].\n\n## Demand\n\nDemand depends on adoption [2].\n"
	block := StructureFromReport(markdown, 2)
	require.Contains(t, block, "zeus-structure")
	out, err := Render(Data{Title: "Study", Lang: "en", Markdown: markdown + "\n" + block, Sources: []Source{{Index: 1, URL: "https://example.org/a"}, {Index: 2, URL: "https://example.org/b"}}})
	require.NoError(t, err)
	require.Contains(t, string(out), `class="report-highlights"`)
	require.Contains(t, string(out), `href="#src-1"`)
	require.Contains(t, string(out), `Production is constrained by capacity`)
	require.NotContains(t, string(out), "```zeus-structure")
	changed := strings.Replace(markdown, "Production is constrained by capacity", "Changed statement", 1)
	out, err = Render(Data{Markdown: changed + "\n" + block, Sources: []Source{{Index: 1}, {Index: 2}}})
	require.NoError(t, err)
	require.NotContains(t, string(out), `<aside class="report-highlights">`)
	require.Empty(t, StructureFromReport("## A\n\nUnsupported claim.\n\n## B\n\nAnother unsupported claim.", 2))
}
