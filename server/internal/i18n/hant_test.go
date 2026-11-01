package i18n

import "testing"

func TestParseTraditional(t *testing.T) {
	for in, want := range map[string]Lang{
		"zh-Hant": ZHHant, "zh-Hant-TW": ZHHant, "zh-TW": ZHHant, "zh_HK": ZHHant, "zh-MO": ZHHant, "zh-Hant-CN": ZHHant,
		"zh-Hans-HK": ZH, "zh-CN": ZH, "zh": ZH,
	} {
		if got := Parse(in); got != want {
			t.Errorf("Parse(%q) = %s, want %s", in, got, want)
		}
	}
	if !Valid("zh-Hant") || Valid("zh-TW") {
		t.Error("Valid")
	}
	if ReportLang("auto", "zh-Hant") != ZHHant || ReportLang("zh-Hant", "en") != ZHHant {
		t.Error("ReportLang")
	}
	if got := FromAcceptLanguage("zh-Hant, en;q=0.5"); got != ZHHant {
		t.Errorf("FromAcceptLanguage = %s", got)
	}
}

func TestHant(t *testing.T) {
	for in, want := range map[string]string{
		"报告不存在":       "報告不存在",
		"登录已过期，请重新登录": "登入已過期，請重新登入",
		"账号不存在或已注销":   "帳號不存在或已註銷",
		"已用 3 分钟":     "已用 3 分鐘",
		"":            "",
	} {
		if got := Hant(in); got != want {
			t.Errorf("Hant(%q) = %q, want %q", in, got, want)
		}
	}
	// Model output only gets characters converted: wording such as 发布 / 信息 stays.
	if got := HantChars("宁德时代发布了新信息"); got != "寧德時代發佈了新信息" && got != "寧德時代發布了新信息" {
		t.Errorf("HantChars = %q", got)
	}
}

func TestPickTraditional(t *testing.T) {
	if got := ZHHant.Pick("简报生成失败", "Briefing failed"); got != "簡報生成失敗" {
		t.Errorf("Pick = %q", got)
	}
	if got := Message(ZHHant, "not_found", "报告不存在"); got != "報告不存在" {
		t.Errorf("Message = %q", got)
	}
	if ZH.Model("简报") != "简报" || ZHHant.Model("简报") != "簡報" {
		t.Error("Model")
	}
	if !ZHHant.Chinese() || EN.Chinese() {
		t.Error("Chinese")
	}
	if ZHHant.ClipLen(30) != 30 {
		t.Error("ClipLen")
	}
}
