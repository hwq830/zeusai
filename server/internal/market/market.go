// Package market is the user's region for localized content: which official briefing templates
// they see and which sources and search settings their briefings use. The App reports it from
// the phone's region setting (like the time zone); the user can change it in settings.
package market

import "strings"

const (
	TW    = "TW"
	HK    = "HK"
	MO    = "MO"
	SG    = "SG"
	MY    = "MY"
	US    = "US"
	Other = "other"
)

// All lists the markets in display order; Other is last.
var All = []string{TW, HK, MO, SG, MY, US, Other}

// Named are the markets a template can target (all but Other).
var Named = All[:len(All)-1]

var names = map[string]string{TW: "台湾", HK: "香港", MO: "澳门", SG: "新加坡", MY: "马来西亚", US: "美国", Other: "其他地区"}

// Valid reports whether s is one of All (exact spelling).
func Valid(s string) bool {
	_, ok := names[s]
	return ok
}

// Name is the Chinese display name, for the admin console.
func Name(m string) string {
	if n, ok := names[m]; ok {
		return n
	}
	return m
}

// Normalize maps a stored or reported value to a market, unknown values to Other.
func Normalize(s string) string {
	if s == Other || s == "" {
		return Other
	}
	if u := strings.ToUpper(strings.TrimSpace(s)); u != "OTHER" && Valid(u) {
		return u
	}
	return Other
}

// Traditional reports whether the market reads Traditional Chinese (search terms are converted).
func Traditional(m string) bool { return m == TW || m == HK || m == MO }

// Country is the ISO 3166 code (lower case) search APIs take, "" for Other.
func Country(m string) string {
	if m == Other || !Valid(m) {
		return ""
	}
	return strings.ToLower(m)
}

// SearchLang is the language of local results ("zh-hant" for TW / HK / MO), "" for no preference.
func SearchLang(m string) string {
	if Traditional(m) {
		return "zh-hant"
	}
	return ""
}
