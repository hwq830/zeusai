// Package i18n is the server side of the app's languages: Chinese (Simplified), Traditional
// Chinese and English. A user has a display language (pushes, messages, briefings and API texts
// follow it) and a report language (auto follows the display language). Simplified Chinese is the
// source language everywhere; English texts sit next to it and anything without an English text
// falls back to Chinese. Traditional Chinese is converted from the Simplified text (see Hant).
package i18n

import (
	"context"
	"strings"
)

type Lang string

const (
	ZH     Lang = "zh"
	ZHHant Lang = "zh-Hant"
	EN     Lang = "en"
	// Auto is only valid as a report-language preference: follow the display language.
	Auto = "auto"
)

// Parse maps a language tag (zh, zh-Hans-CN, zh-Hant-TW, zh-HK, en-US, …) to a supported
// language. Chinese written in Hant, or for Taiwan, Hong Kong and Macau without a script, is
// Traditional. Anything that is not English is Chinese, the product's home language.
func Parse(tag string) Lang {
	t := strings.ToLower(strings.TrimSpace(tag))
	if t == "en" || strings.HasPrefix(t, "en-") || strings.HasPrefix(t, "en_") {
		return EN
	}
	parts := strings.FieldsFunc(t, func(r rune) bool { return r == '-' || r == '_' })
	if len(parts) > 1 && parts[0] == "zh" {
		for _, p := range parts[1:] {
			switch p {
			case "hans":
				return ZH
			case "hant":
				return ZHHant
			}
		}
		for _, p := range parts[1:] {
			if p == "tw" || p == "hk" || p == "mo" {
				return ZHHant
			}
		}
	}
	return ZH
}

// Valid reports whether s is exactly a supported language code.
func Valid(s string) bool { return s == string(ZH) || s == string(ZHHant) || s == string(EN) }

// Chinese reports whether l is Simplified or Traditional Chinese.
func (l Lang) Chinese() bool { return l != EN }

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

// Pick returns en for English when it is not empty, zh converted to Traditional for
// Traditional Chinese, otherwise zh.
func (l Lang) Pick(zh, en string) string {
	switch {
	case l == EN && en != "":
		return en
	case l == ZHHant:
		return Hant(zh)
	}
	return zh
}

// Zh returns a Chinese text in l's script: converted for Traditional Chinese, unchanged otherwise
// (English callers pick their own text first).
func (l Lang) Zh(s string) string {
	if l == ZHHant {
		return Hant(s)
	}
	return s
}

// Model cleans up model output that was asked to be written in l: for Traditional Chinese it
// converts any Simplified characters that slipped in. Other languages pass through.
func (l Lang) Model(s string) string {
	if l == ZHHant {
		return HantChars(s)
	}
	return s
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
	if l == ZHHant {
		return "\n\n输出语言：所有给用户看的文字（标题、摘要、正文、栏目名和 Markdown）都用繁體中文书写，用台湾常用词" +
			"（如「資訊」「軟體」「影片」）；上面写的固定栏目名也换成繁体。来源是简体中文或外文时改写/翻译成繁体中文；" +
			"人名、机构名等首次出现可附原文。字数限制按字计，和中文一样。JSON 的键名保持不变。"
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
