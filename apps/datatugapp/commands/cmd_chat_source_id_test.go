package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
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
