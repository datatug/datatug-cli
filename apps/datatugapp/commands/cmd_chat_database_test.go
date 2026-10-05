package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/datatug/datatug-cli/pkg/chat"
)

// writeChatProjectWithUnresolvedCatalog lays out a project whose environment has
// one catalog, named id, whose driver no source can be built for: the catalog
// exists, and its source does not resolve.
func writeChatProjectWithUnresolvedCatalog(t *testing.T, id string) string {
	t.Helper()
	dir := t.TempDir()
	write := func(path, content string) {
		t.Helper()
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("datatug-project.json", `{"id":"chat-unresolved","title":"Chat unresolved"}`)
	write("queries/.keep", "")
	write("environments/local/local.env.json", `{"id":"local","dbServers":[{"driver":"oracle","catalogs":["`+id+`"]}]}`)
	write("environments/local/catalogs/"+id+"/"+id+".db.json", `{"driver":"oracle"}`)
	return dir
}

// --database takes the ID of a catalog of the environment. A real catalog ID
// that is not a plain name ("Chinook db") and whose source does not resolve is
// still a catalog of the environment: chat opens the degraded session that shows
// the issue, as it did before the check was added. A typed value that is neither
// a catalog of the environment nor a plain name is refused at the entry.
func TestChatDatabase_ACatalogIDThatIsNotAPlainNameIsKnownEvenWhenItsSourceDoesNotResolve(t *testing.T) {
	opened := false
	t.Cleanup(chat.SetRunTeaProgramForTest(func(*tea.Program) (tea.Model, error) { opened = true; return nil, nil }))
	project := writeChatProjectWithUnresolvedCatalog(t, "Chinook db")

	run := func(database string) error {
		opened = false
		_, err := runChatProject(chatCommand(), chatOptions{project: project, env: "local", database: database, model: defaultChatModel, thinking: "low"})
		return err
	}
	if err := run("Chinook db"); err != nil || !opened {
		t.Fatalf("a catalog of the environment: err = %v, session opened = %v, want the degraded session to open", err, opened)
	}
	for _, typed := range []string{"sqlite://carol:pw-Zk39x@host.example/x", "Chinook db 2", "../elsewhere"} {
		err := run(typed)
		if err == nil || !strings.Contains(err.Error(), "--database takes the ID of a catalog") || opened {
			t.Errorf("%q: err = %v, session opened = %v, want the refusal at the entry", typed, err, opened)
		}
		if err != nil && strings.Contains(err.Error(), "pw-Zk39x") {
			t.Errorf("%q: the refusal shows the secret: %v", typed, err)
		}
	}
}

func TestProjectCatalogHasSource(t *testing.T) {
	catalog := chat.ProjectCatalog{Objects: []chat.ProjectObject{
		{Reference: chat.ContextReference{Kind: "project", ObjectID: "p", SourceID: "orders"}},
		{Reference: chat.ContextReference{Kind: "source", SourceID: "orders", ObjectID: "orders"}},
		{Reference: chat.ContextReference{Kind: "table", SourceID: "shop", ObjectID: "main.Album"}},
	}}
	for id, want := range map[string]bool{"orders": true, "shop": false, "main.Album": false, "": false} {
		if got := projectCatalogHasSource(catalog, id); got != want {
			t.Errorf("projectCatalogHasSource(%q) = %v, want %v", id, got, want)
		}
	}
}

// An older version stored whatever was typed for --database, a source string with
// its password included. A plain `datatug chat` that read it back would fail with
// a message about a flag the user did not pass, and the file is only rewritten
// after a successful start: so a remembered database that is not a plain name is
// ignored, and the next start overwrites it.
func TestApplyLastChatOptions_IgnoresARememberedDatabaseThatIsNotAPlainName(t *testing.T) {
	path := useTestLastChatOptionsPath(t)
	for _, tc := range []struct {
		remembered string
		want       string
	}{
		{"chinook-local", "chinook-local"},
		{"sqlite://carol:pw-Zk39x@host.example/x", ""},
		{"<source id not shown>", ""},
		{"../elsewhere", ""},
		{"", ""},
	} {
		data, _ := json.Marshal(lastChatOptions{Env: "dev", Database: tc.remembered, AI: "deepseek"})
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		options := chatOptions{project: ".", env: "local", model: defaultChatModel}
		if err := applyLastChatOptions(chatCommand(), &options); err != nil {
			t.Fatalf("%q: %v", tc.remembered, err)
		}
		if options.database != tc.want || options.env != "dev" || options.ai != "deepseek" {
			t.Errorf("remembered %q: options = %+v, want database %q and the other remembered options kept", tc.remembered, options, tc.want)
		}
	}
}
