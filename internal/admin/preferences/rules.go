package preferences

import "slices"

// SupportedLocales is a conservative allowlist rather than any string: a typo'd locale would
// otherwise fall back to the frontend's default with no server-side signal anything was wrong.
var SupportedLocales = []string{"en", "vi"}

// DefaultLocale is what a member who never chose one reads back.
const DefaultLocale = "en"

// localeProblem says what is wrong with locale, or "" when it is supported. Pure: no I/O.
func localeProblem(locale string) string {
	if slices.Contains(SupportedLocales, locale) {
		return ""
	}
	return "`locale` must be one of: en, vi."
}
