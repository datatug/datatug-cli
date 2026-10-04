package commands

import (
	"bytes"
	"context"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/datatug/datatug-cli/internal/sourcecases"
	"github.com/datatug/datatug-cli/pkg/chat"
)

// TestProperty_NoCommandPathEchoesASourceSecret is the DT-0C acceptance
// property at the command layer: for every generated source string (see
// internal/sourcecases) given to every command path that takes one, no secret of
// four or more characters is in stdout, in stderr, in the returned error, in the
// log, or in any file written.
//
// The command paths are `datatug db <url>`, `datatug db copy --from <url>` and
// `--to <url>`, and `datatug query run --db <url>`, each with the string itself
// and with `env:NAME` where the variable holds it, and `datatug chat --database
// <source>`, which takes a catalog ID but is given whatever was typed there (its
// stdout, stderr, error, the conversation it opens, the schema context sent to the
// model, and the remembered options and chat store it writes). `datatug scan
// --dsn-env` is not in this tree yet; its refusals are covered where it lands.
//
// Nothing here relies on a redactor: Exit no longer calls one, and the
// top-level handler in main.go that does is not run.
func TestProperty_NoCommandPathEchoesASourceSecret(t *testing.T) {
	// A relative path a source names lands in this directory, so a stray write is
	// found below.
	workdir := t.TempDir()
	t.Chdir(workdir)
	dir := t.TempDir()
	emptySource := "sqlite://" + filepath.Join(dir, "in.db")
	emptyTarget := "sqlite://" + filepath.Join(dir, "out.db")
	for _, name := range []string{"in.db", "out.db"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var logged bytes.Buffer
	savedLog := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(savedLog) })

	// No file holds a secret in its name or its content. A random identifier (a
	// chat session's UUID) can hold a short run of a generated secret by chance, so
	// what a case's command wrote is read for that case alone, in a directory of its own.
	checkFiles := func(root string, cases ...sourcecases.Case) {
		t.Helper()
		walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || path == root {
				return err
			}
			content := []byte(nil)
			if !entry.IsDir() {
				if content, err = os.ReadFile(path); err != nil {
					return err
				}
			}
			for _, c := range cases {
				// A chat store holds a UUID and a timestamp per row, and is named by a
				// hash: none comes from a source, and a run of a secret can match one.
				if leaked := sourcecases.Leaks(c, sourcecases.WithoutGeneratedIdentifiers(path), sourcecases.WithoutGeneratedIdentifiers(string(content))); len(leaked) > 0 {
					t.Errorf("%s: %q was written to %s", c.Name, leaked, path)
				}
			}
			return nil
		})
		if walkErr != nil {
			t.Fatal(walkErr)
		}
	}

	// `datatug chat` keeps its chat store under the home directory and remembers
	// its options in the user config directory: both go to a directory of the
	// case's own, which is read back. Its terminal program never starts, and the
	// conversation it opens is kept so that what it says can be read.
	var caseHome string
	t.Cleanup(chat.SetRunTeaProgramForTest(func(*tea.Program) (tea.Model, error) { return nil, nil }))
	savedOptionsPath, savedNewSessionChat := lastChatOptionsPath, newSessionChat
	t.Cleanup(func() { lastChatOptionsPath, newSessionChat = savedOptionsPath, savedNewSessionChat })
	lastChatOptionsPath = func() (string, error) { return filepath.Join(caseHome, "chat-last.json"), nil }
	var conversation chat.ContextualConversation
	newSessionChat = func(ctx context.Context, store *chat.SessionStore, agent chat.ContextualConversation, source string, catalogs ...chat.ProjectCatalog) (*chat.SessionChat, error) {
		conversation = agent
		return savedNewSessionChat(ctx, store, agent, source, catalogs...)
	}
	chatProject := writeChatRunProjectFixture(t)

	cases := sourcecases.CommandCases()
	failed := 0
	for _, c := range cases {
		t.Setenv("DT0C_PROPERTY_SOURCE", c.Source)
		var texts []string
		note := func(stdout, stderr string, err error) {
			texts = append(texts, stdout, stderr)
			if err != nil {
				texts = append(texts, err.Error())
			}
		}

		for _, source := range []string{c.Source, "env:DT0C_PROPERTY_SOURCE"} {
			cmd := dbCommand()
			var err error
			out := covDCaptureStdout(t, func() { err = cmd.RunE(cmd, []string{source}) })
			note(out, "", err)

			stdout, stderr, err := runCopy(t, "db", "copy", "--from", source, "--to", emptyTarget)
			note(stdout.String(), stderr.String(), err)
			stdout, stderr, err = runCopy(t, "db", "copy", "--from", emptySource, "--to", source)
			note(stdout.String(), stderr.String(), err)

			queryOut, queryErr, _ := runQuery(t, "", "--db", source, "--from", "customers", "--no-policies")
			note(queryOut, queryErr, nil)
		}

		// `datatug chat --database <source>`: a source string where a catalog ID belongs.
		chatCmd := chatCommand()
		var chatOut, chatErrOut bytes.Buffer
		chatCmd.SetOut(&chatOut)
		chatCmd.SetErr(&chatErrOut)
		conversation = nil
		caseHome = t.TempDir()
		t.Setenv("HOME", caseHome)
		_, chatErr := runChatProject(chatCmd, chatOptions{project: chatProject, env: "local", database: c.Source, model: defaultChatModel, thinking: "low"})
		note(chatOut.String(), chatErrOut.String(), chatErr)
		if conversation != nil {
			turn, askErr := conversation.AskWithContext(context.Background(), "Show customers", "")
			note(turn.Text, "", askErr)
		}
		// The context the model is given when the selected source is unavailable.
		texts = append(texts, projectSchemaContext(chat.ProjectCatalog{}, map[string]string{}, c.Source))
		checkFiles(caseHome, c)
		checkFiles(chatProject, c)
		texts = append(texts, logged.String())
		logged.Reset()

		if leaked := sourcecases.Leaks(c, texts...); len(leaked) > 0 {
			failed++
			if failed <= 20 {
				t.Errorf("%s\n  leaked %q in:\n    %s", c.Name, leaked, strings.Join(texts, "\n    "))
			}
		}
	}
	if failed > 0 {
		t.Errorf("%d of %d generated sources leaked a secret through a command", failed, len(cases))
	}

	// No file was written with a secret in its name or its content, and nothing
	// was written where a relative source pointed.
	for _, root := range []string{workdir, dir} {
		checkFiles(root, cases...)
	}
	if entries, err := os.ReadDir(workdir); err != nil || len(entries) != 0 {
		t.Errorf("a command wrote %d file(s) where a relative source pointed: %v, %v", len(entries), entries, err)
	}
}
