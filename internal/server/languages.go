package server

// Language is a GOG installer language option.
type Language struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// Languages lists the language codes GOG uses for installers.
var Languages = []Language{
	{"en", "English"}, {"de", "Deutsch"}, {"fr", "Français"}, {"es", "Español"}, {"esmx", "Español (AL)"},
	{"it", "Italiano"}, {"pl", "Polski"}, {"ru", "Русский"}, {"br", "Português do Brasil"}, {"pt", "Português"},
	{"cz", "Český"}, {"hu", "Magyar"}, {"jp", "日本語"}, {"ko", "한국어"}, {"cn", "中文(简体)"}, {"tr", "Türkçe"},
	{"nl", "Nederlands"}, {"sv", "Svenska"}, {"da", "Dansk"}, {"no", "Norsk"}, {"fi", "Suomi"}, {"ar", "العربية"},
	{"uk", "Українська"}, {"ro", "Română"}, {"el", "Ελληνικά"}, {"th", "ไทย"}, {"sk", "Slovenčina"}, {"bl", "Български"},
	{"sr", "Српски"},
}
