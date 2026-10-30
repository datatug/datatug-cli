package commands

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
)

// `datatug chat --database` takes a catalog ID, and a source string such as
// postgres://alice:<password>@host/db is what a person who confuses the two
// types. Whatever was typed is named only when it is a plain name.
const typedSource = "postgres://alice:" + sourceSecret + "@db.example.com/shop"

func TestChatDatabaseThatIsASourceStringIsNeverEchoed(t *testing.T) {
	turn, err := (unavailableSchemaConversation{database: typedSource}).AskWithContext(context.Background(), "Show customers", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(turn.Text, sourceSecret) || strings.Contains(turn.Text, "alice") || !strings.Contains(turn.Text, dbcopy.SourceIDNotShown) {
		t.Fatalf("the degraded chat turn echoes what was typed: %q", turn.Text)
	}

	contextText := projectSchemaContext(chat.ProjectCatalog{}, map[string]string{}, typedSource)
	if strings.Contains(contextText, sourceSecret) || strings.Contains(contextText, "alice") || !strings.Contains(contextText, dbcopy.SourceIDNotShown) {
		t.Fatalf("the schema context sent to the model echoes what was typed: %q", contextText)
	}

	// A plain name is named, as before.
	turn, err = (unavailableSchemaConversation{database: "Café-local"}).AskWithContext(context.Background(), "Show customers", "")
	if err != nil || !strings.Contains(turn.Text, "Café-local") {
		t.Fatalf("a plain catalog ID is named: %q, %v", turn.Text, err)
	}
	if contextText = projectSchemaContext(chat.ProjectCatalog{}, map[string]string{}, "Café-local"); !strings.Contains(contextText, `"Café-local"`) {
		t.Fatalf("a plain catalog ID is named in the context: %q", contextText)
	}
}

func TestResolveQuerySourceURL_NeverEchoesATypedSourceOrItsPath(t *testing.T) {
	dir := writeChatRunProjectFixture(t)
	_, store, err := resolveQueryProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	// A catalog ID that does not exist: the load error is kept, it names the ID.
	_, err = resolveQuerySourceURL(context.Background(), store, dir, "local", "missing-catalog")
	if err == nil || !strings.Contains(err.Error(), `database "missing-catalog" not found in environment "local": `) {
		t.Fatalf("a plain ID that is not found: %v", err)
	}
	// A source string typed for one: neither it nor the path it was turned into is shown.
	_, err = resolveQuerySourceURL(context.Background(), store, dir, "local", typedSource)
	if err == nil {
		t.Fatal("a source string typed as a catalog ID resolved")
	}
	for _, leaked := range []string{sourceSecret, "alice", "db.example.com", "catalogs"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("the error shows %q: %v", leaked, err)
		}
	}
	if !strings.Contains(err.Error(), `database "`+dbcopy.SourceIDNotShown+`" not found in environment "local"`) {
		t.Fatalf("the error does not say that a database was not found: %v", err)
	}
}

func TestRememberedDatabase_OnlyAPlainCatalogIDIsWrittenToTheConfigDirectory(t *testing.T) {
	for in, want := range map[string]string{
		"chinook-local": "chinook-local",
		"Café-local":    "Café-local",
		"":              "",
		typedSource:     "",
		"my db":         "",
		"a/b":           "",
	} {
		if got := rememberedDatabase(in); got != want {
			t.Errorf("rememberedDatabase(%q) = %q, want %q", in, got, want)
		}
	}
}

// A source string typed where --database or --env takes an ID is refused before
// the project or its catalog is read. The fixture is deliberately not a project:
// its invalid project file would surface as a load error, so the absence of that
// error proves no project read happened. The same values used to reach
// buildChatProjectCatalog, whose Core read lock spawns a git process for each
// store read; the refusal at the entry keeps the safety assertion and the cost
// apart. The configured nonplain catalog ID keeps its degraded session.
func TestSourceStringAsDatabaseOrEnvironmentIsRefusedBeforeProjectIO(t *testing.T) {
	opened := false
	t.Cleanup(chat.SetRunTeaProgramForTest(func(*tea.Program) (tea.Model, error) {
		opened = true
		return nil, nil
	}))
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "datatug-project.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	projectDir, store, err := resolveQueryProject(dir)
	if err != nil {
		t.Fatal(err)
	}

	refused := func(t *testing.T, err error, wants ...string) {
		t.Helper()
		if err == nil {
			t.Fatal("the source string was accepted: want a refusal at the entry")
		}
		for _, want := range wants {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("refusal %q does not contain %q", err, want)
			}
		}
		for _, forbidden := range []string{"load project explorer", "invalid character", sourceSecret, "alice", "db.example.com"} {
			if strings.Contains(err.Error(), forbidden) {
				t.Fatalf("the refusal shows %q, so a project was read or the value was echoed: %v", forbidden, err)
			}
		}
		if opened {
			t.Fatal("a refused source string opened a chat session")
		}
	}

	t.Run("chat --database", func(t *testing.T) {
		opened = false
		_, err := runChatProject(chatCommand(), chatOptions{project: dir, env: "local", database: typedSource, model: defaultChatModel, thinking: "low"})
		refused(t, err, "--database takes the ID of a catalog")
	})
	t.Run("chat --env", func(t *testing.T) {
		opened = false
		_, err := runChatProject(chatCommand(), chatOptions{project: dir, env: typedSource, model: defaultChatModel, thinking: "low"})
		refused(t, err, "database catalogs", "must be a plain name")
	})
	t.Run("resolveQueryDatabase --env", func(t *testing.T) {
		opened = false
		_, err := resolveQueryDatabase(context.Background(), store, typedSource, &datatug.QueryDef{ID: "saved"})
		refused(t, err, "database catalogs", "must be a plain name")
	})
	t.Run("resolveSQLOrTugQLSourceURL --env", func(t *testing.T) {
		opened = false
		_, err := resolveSQLOrTugQLSourceURL(context.Background(), store, projectDir, typedSource, &datatug.QueryDef{ID: "saved"})
		refused(t, err, "database catalogs", "must be a plain name")
	})

	// A configured catalog ID that is not a plain name is not a source string:
	// chat still opens the degraded session that reports its unresolved source.
	t.Run("configured nonplain catalog ID", func(t *testing.T) {
		opened = false
		project := writeChatProjectWithUnresolvedCatalog(t, "Chinook db")
		if _, err := runChatProject(chatCommand(), chatOptions{project: project, env: "local", database: "Chinook db", model: defaultChatModel, thinking: "low"}); err != nil || !opened {
			t.Fatalf("a catalog of the environment: err = %v, session opened = %v, want the degraded session to open", err, opened)
		}
	})
}

// A value for --database that is neither a source of the project nor a plain
// name is refused at the entry, naming nothing that was typed: it would
// otherwise reach the connect screen, the settings the browser reads, the scope
// the store hashes and the stand-in source of a failed lookup.
func TestChatDatabaseThatIsASourceStringIsRefusedAtTheEntry(t *testing.T) {
	t.Cleanup(chat.SetRunTeaProgramForTest(func(*tea.Program) (tea.Model, error) { return nil, nil }))
	dir := writeChatRunProjectFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	savedOptionsPath := lastChatOptionsPath
	t.Cleanup(func() { lastChatOptionsPath = savedOptionsPath })
	lastChatOptionsPath = func() (string, error) { return filepath.Join(home, "chat-last.json"), nil }

	for _, typed := range []string{typedSource, "postgres://db.example.com/shop?password=" + sourceSecret, "my db", "a/b", "sqlite:///tmp/x.db#" + sourceSecret} {
		cmd := chatCommand()
		var stdout, stderr bytes.Buffer
		cmd.SetOut(&stdout)
		cmd.SetErr(&stderr)
		_, err := runChatProject(cmd, chatOptions{project: dir, env: "local", database: typed, model: defaultChatModel, thinking: "low"})
		var exit ExitCoder
		if !errors.As(err, &exit) || exit.ExitCode() != exitCodeUsage {
			t.Fatalf("--database %q: error = %v, want a usage error", typed, err)
		}
		if !strings.Contains(err.Error(), "--database takes the ID of a catalog") {
			t.Errorf("--database %q: the refusal does not say what --database takes: %v", typed, err)
		}
		for _, text := range []string{err.Error(), stdout.String(), stderr.String()} {
			for _, leaked := range []string{sourceSecret, "alice", "db.example.com", "my db", "a/b"} {
				if strings.Contains(text, leaked) {
					t.Errorf("--database %q: the refusal shows %q: %s", typed, leaked, text)
				}
			}
		}
	}
	// Nothing was opened or written for a refused value.
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Errorf("a refused --database wrote %d file(s) under the home directory: %v, %v", len(entries), entries, err)
	}

	// A catalog of the project, and a plain name the project does not have, are not refused here.
	for _, database := range []string{"chinook-local", "no-such-catalog"} {
		cmd := chatCommand()
		if _, err := runChatProject(cmd, chatOptions{project: dir, env: "local", database: database, model: defaultChatModel, thinking: "low"}); err != nil {
			t.Errorf("--database %q: %v", database, err)
		}
	}
}
