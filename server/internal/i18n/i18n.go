// Package i18n is the server side of the app's two languages, Chinese and English. A user has a
// display language (pushes, messages, briefings and API texts follow it) and a report language
// (auto follows the display language). Chinese is the source language everywhere; English
// texts sit next to it and anything without an English text falls back to Chinese.
package i18n

import (
	"context"
	"strings"
)

type Lang string

const (
	ZH Lang = "zh"
	EN Lang = "en"
	// Auto is only valid as a report-language preference: follow the display language.
	Auto = "auto"
)

// Parse maps a language tag (zh, zh-Hans-CN, en-US, …) to a supported language. Anything that
// is not English is Chinese, the product's home language.
func Parse(tag string) Lang {
	t := strings.ToLower(strings.TrimSpace(tag))
	if t == "en" || strings.HasPrefix(t, "en-") || strings.HasPrefix(t, "en_") {
		return EN
	}
	return ZH
}

// Valid reports whether s is exactly a supported language code.
func Valid(s string) bool { return s == string(ZH) || s == string(EN) }

// FromAcceptLanguage returns the first tag of an Accept-Language header, ignoring q-values.
func FromAcceptLanguage(h string) Lang {
	first, _, _ := strings.Cut(h, ",")
	first, _, _ = strings.Cut(first, ";")
	return Parse(first)
}

// ReportLang resolves a user's report-language preference against their display language.
func ReportLang(pref, ui string) Lang {
	if Valid(pref) {
		return Lang(pref)
	}
	return Parse(ui)
}

// Pick returns en for English when it is not empty, otherwise zh.
func (l Lang) Pick(zh, en string) string {
	if l == EN && en != "" {
		return en
	}
	return zh
}

type ctxKey struct{}

// WithLang stores the request's language; the HTTP layer sets it from Accept-Language.
func WithLang(ctx context.Context, l Lang) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// FromContext returns the request's language, Chinese when none was set.
func FromContext(ctx context.Context) Lang {
	if l, ok := ctx.Value(ctxKey{}).(Lang); ok {
		return l
	}
	return ZH
}

// T picks between a Chinese and an English text by the request's language.
func T(ctx context.Context, zh, en string) string { return FromContext(ctx).Pick(zh, en) }

// OutputInstruction is appended to a generation prompt. The prompts are written in Chinese;
// this pins the language of everything the model writes for the user.
func OutputInstruction(l Lang) string {
	if l == EN {
		return "\n\nOutput language: write every user-facing text in the JSON (titles, headlines, summaries, notes, " +
			"answers, headings and Markdown) in natural English, even though these instructions and the sources may be " +
			"in Chinese. Translate Chinese material and any fixed headings named above; keep names in their usual " +
			"English form. Where a limit is given in 字 (Chinese characters), aim for about half as many English words. " +
			"JSON keys stay exactly as specified."
	}
	return "\n\n输出语言：所有给用户看的文字都用简体中文，来源是外文时翻译成中文；人名、机构名等首次出现可附原文。JSON 的键名保持不变。"
}

// ClipLen scales a length limit given for Chinese text; English needs more characters than Chinese for the same text.
func (l Lang) ClipLen(zh int) int {
	if l == EN {
		return zh * 8 / 3
	}
	return zh
}
