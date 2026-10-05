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
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-core/pkg/datatug"
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
// <source>`, which takes a catalog ID but is given whatever was typed there (it is
// refused at the entry; its stdout, stderr, error, the conversation it would open,
// the schema context sent to the model, and the remembered options and chat store
// it would write are read all the same), and `datatug scan
// -D postgres`, whose `--dsn-env` is given the variable that holds the source and
// the source itself where a variable name belongs, and whose `--db` and `--env`
// are given the source where a name belongs. The scan's own answer that
// PostgreSQL cannot be scanned yet is lifted (liftPostgresRefusal) so that what
// comes after it is read; the real api.UpdateDbSchema still stops before it
// connects, as TestScanCommandAction_PostgresAnswersThatTheScanIsNotAvailable...
// holds, so no case reaches a server.
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

	// `datatug scan -D postgres` reads its connection URL from a variable, and opens it
	// through the fake server.
	const scanVariable = "DATATUG_PROPERTY_SOURCE"
	var scanValue string
	covDSetVar(t, &scanLookupEnv, func(name string) (string, bool) { return scanValue, name == scanVariable })
	server := &propertyPgServer{}
	t.Cleanup(api.SetOpenSchemaScanForTest(server.open))

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

		// `datatug scan -D postgres`: the source in the variable, which the fake server fails
		// in each way it can and then serves, each scan into a folder of its own; and the
		// source where a name belongs, which is refused before anything is opened.
		scanValue = c.Source
		for _, mode := range propertyPgModes {
			server.mode = mode
			scanDir := t.TempDir()
			var scanErr error
			out := covDCaptureStdout(t, func() {
				scanErr = covDRunScan("-d", scanDir, "-D", "postgres", "--dsn-env", scanVariable, "--env", "prod", "--db", "shop")
			})
			note(out, "", scanErr)
			checkFiles(scanDir, c)
		}
		scanDir := t.TempDir()
		for _, args := range [][]string{
			{"--dsn-env", c.Source, "--env", "prod", "--db", "shop"},
			{"--dsn-env", scanVariable, "--env", "prod", "--db", c.Source},
			{"--dsn-env", scanVariable, "--env", c.Source, "--db", "shop"},
		} {
			var scanErr error
			out := covDCaptureStdout(t, func() { scanErr = covDRunScan(append([]string{"-d", scanDir, "-D", "postgres"}, args...)...) })
			note(out, "", scanErr)
		}
		checkFiles(scanDir, c)

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
		// No generated source is a plain name or a source of the project: each is
		// refused at the entry, and what is read below is what stays if it were not.
		if chatErr == nil || !strings.Contains(chatErr.Error(), "--database takes the ID of a catalog") {
			t.Errorf("%s: chat --database was not refused at the entry: %v", c.Name, chatErr)
		}
		if conversation != nil {
			turn, askErr := conversation.AskWithContext(context.Background(), "Show customers", "")
			note(turn.Text, "", askErr)
		}
		// --env takes the ID of an environment, and a source string can be typed there
		// too. `datatug chat` with no --database and `datatug query run` of a saved query
		// both pick the database of that environment, and say so when it has none.
		chatCmd = chatCommand()
		chatOut.Reset()
		chatErrOut.Reset()
		chatCmd.SetOut(&chatOut)
		chatCmd.SetErr(&chatErrOut)
		_, chatErr = runChatProject(chatCmd, chatOptions{project: chatProject, env: c.Source, model: defaultChatModel, thinking: "low"})
		note(chatOut.String(), chatErrOut.String(), chatErr)
		if chatErr == nil || !strings.Contains(chatErr.Error(), "database catalogs") {
			t.Errorf("%s: chat --env did not stop at the environment's database catalogs: %v", c.Name, chatErr)
		}
		projectDir, projectStore, projectErr := resolveQueryProject(chatProject)
		if projectErr != nil {
			t.Fatal(projectErr)
		}
		_, envErr := resolveSQLOrDTQLSourceURL(context.Background(), projectStore, projectDir, c.Source, &datatug.QueryDef{ID: "saved"})
		note("", "", envErr)
		if envErr == nil || !strings.Contains(envErr.Error(), "database catalogs") {
			t.Errorf("%s: a saved query with --env did not stop at the environment's database catalogs: %v", c.Name, envErr)
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

	// The scans reached the fake server, in every way it can fail and in the way it works,
	// and it was asked for the source of the variable and nothing else: it was the only
	// thing the scans opened, and no scan dialled a server.
	server.assertReached(t, len(propertyPgModes))

	// No file was written with a secret in its name or its content, and nothing
	// was written where a relative source pointed.
	for _, root := range []string{workdir, dir} {
		checkFiles(root, cases...)
	}
	if entries, err := os.ReadDir(workdir); err != nil || len(entries) != 0 {
		t.Errorf("a command wrote %d file(s) where a relative source pointed: %v, %v", len(entries), entries, err)
	}
}
