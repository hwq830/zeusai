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
