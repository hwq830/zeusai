package report

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hwq830/zeusai/server/internal/events"
	"github.com/hwq830/zeusai/server/internal/i18n"
)

func TestFollowupAppendixRenumbersCitations(t *testing.T) {
	sources := []followupSource{
		{Index: 3, Title: "来源三", URL: "https://example.test/c"},
		{Index: 1, Title: "来源一", URL: "https://example.test/a"},
	}
	got := followupAppendix(i18n.ZH, "为什么？", "结论见 [3]，旁证 [1]，再引 [3]。", sources)
	require.Contains(t, got, "## 附录 · 追问：为什么？")
	require.Contains(t, got, "结论见 [追问1]，旁证 [追问2]，再引 [追问1]。")
	require.NotContains(t, got, "[3]")
	require.Contains(t, got, "### 追问来源")
	require.Contains(t, got, "1. [来源三](https://example.test/c)")
	require.Contains(t, got, "2. [来源一](https://example.test/a)")
}

func TestFollowupAppendixWithoutSourcesHasNoSourceSection(t *testing.T) {
	got := followupAppendix(i18n.ZH, "没有引用的问题", "答案里没有引用。", nil)
	require.Contains(t, got, "## 附录 · 追问：没有引用的问题")
	require.Contains(t, got, "答案里没有引用。")
	require.NotContains(t, got, "追问来源")
}

func TestFollowupAppendixEnglish(t *testing.T) {
	sources := []followupSource{{Index: 2, Title: "Source two", URL: "https://example.test/two"}}
	got := followupAppendix(i18n.EN, "Why?", "See [2].", sources)
	require.Contains(t, got, "## Appendix · Follow-up: Why?")
	require.Contains(t, got, "See [follow-up 1].")
	require.Contains(t, got, "### Follow-up sources")
}

func TestFollowupAppendixTraditionalChinese(t *testing.T) {
	got := followupAppendix(i18n.ZHHant, "為什麼？", "見 [2]。", []followupSource{{Index: 2, Title: "來源", URL: "https://example.test"}})
	require.Contains(t, got, "附錄 · 追問：為什麼？")
	require.Contains(t, got, "[追問1]")
	require.Contains(t, got, "追問來源")
}

func TestFollowupSourcesJSONKeepsCitationOrderAndSkipsUnknown(t *testing.T) {
	sources := []events.Source{
		{Title: "一", URL: "https://example.test/1", SiteName: "站一"},
		{Title: "二", URL: "https://example.test/2"},
	}
	raw := FollowupSourcesJSON("先 [2]，再 [2]，最后 [1]；越界 [9]，编号 [0]。", sources)
	var got []followupSource
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, []followupSource{
		{Index: 2, Title: "二", URL: "https://example.test/2"},
		{Index: 1, Title: "一", URL: "https://example.test/1", SiteName: "站一"},
	}, got)
}

func TestFollowupSourcesJSONEmptyWhenNoCitations(t *testing.T) {
	require.JSONEq(t, `[]`, string(FollowupSourcesJSON("没有引用。", []events.Source{{URL: "https://example.test"}})))
}

func TestFollowupAppendixEscapesLinkSyntaxInTitles(t *testing.T) {
	got := followupAppendix(i18n.ZH, "q", "a [1]", []followupSource{{Index: 1, Title: "标题 [含] 方括号", URL: "https://example.test"}})
	require.Contains(t, got, `1. [标题 \[含\] 方括号](https://example.test)`)
}
