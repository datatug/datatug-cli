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
