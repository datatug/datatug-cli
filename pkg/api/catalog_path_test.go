package api

import (
	"path/filepath"
	"testing"

	"github.com/mitchellh/go-homedir"
)

func TestResolveCatalogPath_Tilde(t *testing.T) {
	homedir.DisableCache = true
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := ResolveCatalogPath("/proj", "~/datatug/dbs/chinook-local.sqlite")
	if err != nil {
		t.Fatalf("ResolveCatalogPath: %v", err)
	}
	want := filepath.Join(home, "datatug", "dbs", "chinook-local.sqlite")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveCatalogPath_DollarHome(t *testing.T) {
	homedir.DisableCache = true
	home := t.TempDir()
	t.Setenv("HOME", home)

	cases := map[string]string{
		"$HOME/dbs/x.db":   filepath.Join(home, "dbs", "x.db"),
		"${HOME}/dbs/x.db": filepath.Join(home, "dbs", "x.db"),
	}
	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			got, err := ResolveCatalogPath("/proj", input)
			if err != nil {
				t.Fatalf("ResolveCatalogPath(%q): %v", input, err)
			}
			if got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}

func TestResolveCatalogPath_RelativeToProjectDir(t *testing.T) {
	got, err := ResolveCatalogPath("/proj", "dbs/chinook-local.sqlite")
	if err != nil {
		t.Fatalf("ResolveCatalogPath: %v", err)
	}
	want := filepath.Join("/proj", "dbs", "chinook-local.sqlite")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveCatalogPath_RelativeWithNoProjectDir_Errors(t *testing.T) {
	_, err := ResolveCatalogPath("", "dbs/chinook-local.sqlite")
	if err == nil {
		t.Fatal("expected an error: a relative path with no project directory to resolve against cannot be opened")
	}
}

func TestResolveCatalogPath_AlreadyAbsolute_Unchanged(t *testing.T) {
	got, err := ResolveCatalogPath("/proj", "/abs/chinook.sqlite")
	if err != nil {
		t.Fatalf("ResolveCatalogPath: %v", err)
	}
	if got != "/abs/chinook.sqlite" {
		t.Errorf("got %q, want unchanged absolute path", got)
	}
}

func TestResolveCatalogPath_Empty_Errors(t *testing.T) {
	if _, err := ResolveCatalogPath("/proj", ""); err == nil {
		t.Fatal("expected an error for an empty catalog path")
	}
}
