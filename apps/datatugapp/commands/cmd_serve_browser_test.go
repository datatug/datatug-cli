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
