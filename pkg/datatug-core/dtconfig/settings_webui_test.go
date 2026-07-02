package dtconfig

import "testing"

func TestSettings_WebUIOrigin(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings Settings
		want     string
	}{
		{name: "no_webui_config", settings: Settings{}, want: DefaultWebUIOrigin},
		{name: "empty_origin", settings: Settings{WebUI: &WebUIConfig{}}, want: DefaultWebUIOrigin},
		{name: "custom_origin", settings: Settings{WebUI: &WebUIConfig{Origin: "http://localhost:4200"}}, want: "http://localhost:4200"},
		{name: "trailing_slash_trimmed", settings: Settings{WebUI: &WebUIConfig{Origin: "https://example.com/"}}, want: "https://example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.settings.WebUIOrigin(); got != tc.want {
				t.Errorf("WebUIOrigin() = %q, want %q", got, tc.want)
			}
		})
	}
}
