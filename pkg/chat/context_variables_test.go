package chat

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// A model-authored TugQL query that uses a context variable is refused by the
// executor's backstop: chat has no saved QueryDef to check, and the source
// (a sqlite file that does not exist) is never opened.
func TestRunTugQLRefusesContextVariables(t *testing.T) {
	t.Parallel()
	executor := secureread.NewExecutor(secureread.Session{Unrestricted: true})
	source := "sqlite://" + filepath.Join(t.TempDir(), "missing.sqlite")
	doc := "from: {name: \"spaces/{SneatUserPersonalSpaceID}/ext/datatug/projects\"}\nlimit: 5"
	response, err := (&AIConversation{}).runTugQL(context.Background(), executor, source, runTugQLArgs{TugQL: doc})
	if err != nil || response.OK || !strings.Contains(response.Error, "context variable SneatUserPersonalSpaceID,") {
		t.Fatalf("runTugQL = %+v, %v; want a refusal naming SneatUserPersonalSpaceID", response, err)
	}
}
