package api

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
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

// The path of a catalog names a file or a directory. A URL written there is not one,
// and joining it to the project folder turns "https://tok@host/x" into a relative
// path that every later message would show whole, so it is refused before it is
// joined, with a message that says what is wrong and shows nothing of the path.
func TestResolveCatalogPath_RefusesAURLWithoutShowingIt(t *testing.T) {
	for _, path := range []string{
		"https://tok_Zk39xq@git.example/x",
		"HTTPS://carol:pw-Zk39x@git.example/x",
		"postgres://carol:pw-Zk39x@db.example/shop",
		"./https://tok_Zk39xq@git.example/x",
		"data/http://tok_Zk39xq@git.example/x",
	} {
		resolved, err := ResolveCatalogPath("/proj", path)
		if err == nil {
			t.Errorf("ResolveCatalogPath(%q) = %q, want a refusal", path, resolved)
			continue
		}
		if !strings.Contains(err.Error(), "is a URL") {
			t.Errorf("ResolveCatalogPath(%q): %v, want the message to say the path is a URL", path, err)
		}
		for _, secret := range []string{"tok_Zk39xq", "carol", "pw-Zk39x", "git.example", "db.example"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("ResolveCatalogPath(%q) shows %q: %v", path, secret, err)
			}
		}
	}
	// A path that merely holds a colon, an "@" or a drive letter is a path.
	for _, path := range []string{"dbs/team@work/a.db", "dbs/a:b.db", "C://work/a@b.db"} {
		if _, err := ResolveCatalogPath("/proj", path); err != nil {
			t.Errorf("ResolveCatalogPath(%q): %v, want a path", path, err)
		}
	}
}

// Every driver that reads a path takes it through ResolveCatalogPath, and the
// error names the catalog by its ID and nothing of the path.
func TestSourceURLFromCatalog_RefusesAURLInTheCatalogPath(t *testing.T) {
	for _, driver := range []string{"sqlite3", "ingitdb", "openvaultdb"} {
		catalog := datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: driver, Path: "https://tok_Zk39xq@git.example/x"}}
		catalog.ID = "chinook-local"
		_, err := sourceURLFromCatalog(catalog, "/proj")
		if err == nil || !strings.Contains(err.Error(), "is a URL") || strings.Contains(err.Error(), "tok_Zk39xq") || strings.Contains(err.Error(), "git.example") {
			t.Errorf("%s: sourceURLFromCatalog = %v, want a refusal that shows nothing of the path", driver, err)
		}
	}
}
