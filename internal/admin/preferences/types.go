package preferences

// Preferences is the caller's own settings (today the locale, the backend half of i18n). The type
// names are the ones the OpenAPI document shows, so they stay as they were when this lived in
// package admin.
type Preferences struct {
	Locale string `json:"locale" required:"false"`
}

type PreferencesResult struct {
	Data *Preferences `json:"data"`
}
