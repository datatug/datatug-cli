package commands

import "testing"

func TestServeOpenBrowserFlagDefaultsOff(t *testing.T) {
	cmd := serveCommandArgs()
	f, err := readServeFlags(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if f.openBrowser {
		t.Fatal("serve must not open a browser by default")
	}
	if err := cmd.Flags().Set(serveOpenBrowserFlag, "true"); err != nil {
		t.Fatal(err)
	}
	if f, err = readServeFlags(cmd); err != nil {
		t.Fatal(err)
	}
	if !f.openBrowser {
		t.Fatal("--open-browser must enable opening the browser")
	}
}

func TestServeAgentURLsCarrySchemeHostAndPort(t *testing.T) {
	agentURL, webUIURL := serveAgentURLs("localhost", 8989)
	if agentURL != "http://localhost:8989" {
		t.Errorf("agentURL = %q", agentURL)
	}
	if webUIURL != "https://datatug.app/store/http-localhost:8989" {
		t.Errorf("webUIURL = %q", webUIURL)
	}
	_, webUIURL = serveAgentURLs("0.0.0.0", 80)
	if webUIURL != "https://datatug.app/store/http-0.0.0.0:80" {
		t.Errorf("port 80 must stay explicit in the store id, got %q", webUIURL)
	}
}

// TestBannerAddr covers the bug S77 found: the serve banner printed the
// literal string "localhost" while the process actually bound 127.0.0.1.
func TestBannerAddr(t *testing.T) {
	tests := []struct {
		name         string
		resolvedHost string
		explicitHost string
		want         string
	}{
		{
			name:         "default localhost prints the real bind address",
			resolvedHost: "localhost",
			explicitHost: "",
			want:         "127.0.0.1",
		},
		{
			name:         "explicit --host localhost is printed exactly as given",
			resolvedHost: "localhost",
			explicitHost: "localhost",
			want:         "localhost",
		},
		{
			name:         "explicit --host wildcard passes through unchanged",
			resolvedHost: "0.0.0.0",
			explicitHost: "0.0.0.0",
			want:         "0.0.0.0",
		},
		{
			name:         "a config-resolved non-localhost host passes through unchanged",
			resolvedHost: "example.com",
			explicitHost: "",
			want:         "example.com",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bannerAddr(tt.resolvedHost, tt.explicitHost); got != tt.want {
				t.Errorf("bannerAddr(%q, %q) = %q, want %q", tt.resolvedHost, tt.explicitHost, got, tt.want)
			}
		})
	}
}
