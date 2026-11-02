package market

import "strings"

const Global = "global"

// SearchRegion resolves an explicit content region; missing/invalid input uses the saved user region.
// Unlike Other (a user market), Global explicitly removes the country preference.
func SearchRegion(value, fallback string) string {
	if strings.EqualFold(strings.TrimSpace(value), Global) {
		return Global
	}
	if value == Other {
		return Other
	}
	v := strings.ToUpper(strings.TrimSpace(value))
	if Valid(v) {
		return v
	}
	if fallback == Global {
		return Global
	}
	return Normalize(fallback)
}
