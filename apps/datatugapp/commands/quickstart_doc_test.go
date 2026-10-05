package commands

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is the test that keeps the quick start of the README true. It reads the README, takes
// the section "Quick start", and runs every command of its ```console blocks, in order, in a folder
// of its own, against a SQLite file the quick start makes itself and, for PostgreSQL, against the
// fake reader of the scan journey (no test dials). The output of each command is compared, as it
// is written in the README, with what the command wrote to its standard output: when a command or
// an output block changes without the other, this test fails until the README says what happens.
//
// A console block is a transcript: a line that starts with "$ " is a command, and the lines under
// it, up to the next command, are its standard output (a command with nothing under it writes
// nothing). A command whose quotes are open goes on in the next line. Only three programs run, and a
// transcript that runs any other fails the test: datatug (the command tree of this package, in this
// process), sqlite3 (which runs its second argument on the database file of its first, as the
// program of that name does) and export (which sets an environment variable of the quick start).
// A block that is not a console block, such as the installation commands, is not run.

const quickStartDatabaseVariable = "DATATUG_SHOP_URL"

// quickStartSection is the README from the heading "## Quick start" to the next heading of the same level.
func quickStartSection(readme string) (string, error) {
	lines := strings.Split(readme, "\n")
	start := -1
	for i, line := range lines {
		if line == "## Quick start" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return "", errors.New(`the README has no section "## Quick start"`)
	}
	end := len(lines)
	for i := start; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n"), nil
}

// consoleBlocks are the contents of the ```console blocks of text, in order.
func consoleBlocks(text string) (blocks []string) {
	var current []string
	inConsole, inOther := false, false
	for _, line := range strings.Split(text, "\n") {
		switch {
		case inConsole && strings.HasPrefix(line, "```"):
			blocks = append(blocks, strings.Join(current, "\n"))
			current, inConsole = nil, false
		case inConsole:
			current = append(current, line)
		case inOther && strings.HasPrefix(line, "```"):
			inOther = false
		case inOther:
		case strings.TrimSpace(line) == "```console":
			inConsole = true
		case strings.HasPrefix(line, "```"):
			inOther = true
		}
	}
	return blocks
}

// shellWords splits a command line into its words: single quotes keep everything, double quotes keep
// everything but a backslash before a quote or a backslash, and a word is closed by a space or a new line
// outside quotes. It fails on a quote that is not closed.
func shellWords(text string) ([]string, error) {
	var words []string
	var word strings.Builder
	inWord := false
	var quote rune
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case quote == '\'' && r == '\'', quote == '"' && r == '"':
			quote = 0
		case quote == '\'':
			word.WriteRune(r)
		case quote == '"':
			if r == '\\' && i+1 < len(runes) && (runes[i+1] == '"' || runes[i+1] == '\\') {
				i++
				r = runes[i]
			}
			word.WriteRune(r)
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\n' || r == '\t':
			if inWord {
				words = append(words, word.String())
				word.Reset()
				inWord = false
			}
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, errors.New("a quote is not closed")
	}
	if inWord {
		words = append(words, word.String())
	}
	return words, nil
}

type quickStartStep struct {
	words []string
	want  string // the standard output the README shows
}

// parseTranscript splits a console block into its commands and the output under each.
func parseTranscript(block string) ([]quickStartStep, error) {
	lines := strings.Split(block, "\n")
	var steps []quickStartStep
	for i := 0; i < len(lines); {
		if !strings.HasPrefix(lines[i], "$ ") {
			return nil, fmt.Errorf("line %q is output with no command above it", lines[i])
		}
		text := strings.TrimPrefix(lines[i], "$ ")
		i++
		words, err := shellWords(text)
		for err != nil && i < len(lines) {
			text += "\n" + lines[i]
			i++
			words, err = shellWords(text)
		}
		if err != nil {
			return nil, fmt.Errorf("command %q: %w", text, err)
		}
		var output []string
		for i < len(lines) && !strings.HasPrefix(lines[i], "$ ") {
			output = append(output, lines[i])
			i++
		}
		want := ""
		if len(output) > 0 {
			want = strings.Join(output, "\n") + "\n"
		}
		steps = append(steps, quickStartStep{words: words, want: want})
	}
	return steps, nil
}

// quickStartPostgres is the fake server of the PostgreSQL step: the scan opens it, never a real one.
// It holds a table of the schema public that the SQLite sample has, with the tables of the journey.
func quickStartPostgres() *fakePgDatabase {
	journey := newJourneyPgDatabase().relations
	customer, invoice := journey[0], journey[3] // Customer of public, Invoice of sales
	invoice.schema = "public"
	return &fakePgDatabase{relations: []pgRelation{customer, invoice}}
}

// runQuickStart runs the quick start of readme in a new folder and returns the first way in which it
// is not true, and the datatug commands it ran, each as the words after "datatug".
func runQuickStart(t *testing.T, readme string) (ran [][]string, err error) {
	t.Helper()
	section, err := quickStartSection(readme)
	if err != nil {
		return nil, err
	}
	blocks := consoleBlocks(section)
	if len(blocks) == 0 {
		return nil, errors.New("the quick start has no console block")
	}

	t.Chdir(t.TempDir())
	// The scan reports its progress through the log, with the time of each line: that is not output
	// the README shows.
	oldLog := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(oldLog) })

	t.Cleanup(api.SetOpenSchemaScanForTest(func(ref dbcopy.BackendRef, _ context.Context) (dbcopy.SchemaScanDB, error) {
		if ref.Raw != "env:"+quickStartDatabaseVariable {
			return nil, errors.New("the quick start opened a source that this test is not set up for")
		}
		return quickStartPostgres(), nil
	}))
	var exported []string

	for _, block := range blocks {
		steps, parseErr := parseTranscript(block)
		if parseErr != nil {
			return ran, parseErr
		}
		for _, step := range steps {
			if len(step.words) == 0 {
				return ran, errors.New("a command is empty")
			}
			command := strings.Join(step.words, " ")
			switch step.words[0] {
			case "sqlite3":
				if len(step.words) != 3 {
					return ran, fmt.Errorf("%q: sqlite3 takes a file and the statements to run on it", command)
				}
				if err = runQuickStartSQLite(step.words[1], step.words[2]); err != nil {
					return ran, fmt.Errorf("%q: %w", command, err)
				}
			case "export":
				if len(step.words) != 2 || !strings.Contains(step.words[1], "=") {
					return ran, fmt.Errorf("%q: export takes NAME=value", command)
				}
				name, value, _ := strings.Cut(step.words[1], "=")
				t.Setenv(name, value)
				exported = append(exported, value)
			case "datatug":
				ran = append(ran, step.words[1:])
				root := DatatugCommand()
				var stdout, stderr bytes.Buffer
				root.SetArgs(step.words[1:])
				root.SetOut(&stdout)
				root.SetErr(&stderr)
				root.SilenceUsage, root.SilenceErrors = true, true
				if err = root.Execute(); err != nil {
					return ran, fmt.Errorf("%q failed: %w", command, err)
				}
				if stdout.String() != step.want {
					return ran, fmt.Errorf("%q wrote\n%s\nand the README says it writes\n%s", command, stdout.String(), step.want)
				}
				continue
			default:
				return ran, fmt.Errorf("the quick start runs %q, a program this test does not run", step.words[0])
			}
			if step.want != "" {
				return ran, fmt.Errorf("%q writes nothing, and the README shows output for it", command)
			}
		}
	}
	return ran, checkNoConnectionInFolder(exported)
}

// checkNoConnectionInFolder is an error when a file of the folder the quick start ran in holds a
// password or a host:port of a URL that the quick start exported: a project holds the name of the
// variable, and nothing of what is in it.
func checkNoConnectionInFolder(exported []string) error {
	var secrets []string
	for _, value := range exported {
		if u, err := url.Parse(value); err == nil && u.Host != "" {
			secrets = append(secrets, u.Host)
			if password, ok := u.User.Password(); ok {
				secrets = append(secrets, password)
			}
		}
	}
	return filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || strings.HasSuffix(path, ".db") {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, secret := range secrets {
			if bytes.Contains(data, []byte(secret)) {
				return fmt.Errorf("%s holds %q, a part of the URL in the variable", filepath.ToSlash(path), secret)
			}
		}
		return nil
	})
}

func runQuickStartSQLite(file, statements string) error {
	db, err := sql.Open("sqlite", file)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	_, err = db.Exec(statements)
	return err
}

func readREADME(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "README.md"))
	require.NoError(t, err)
	return string(data)
}

// The quick start of the README is true: every command runs, and writes what the README says.
func TestQuickStartOfTheREADMEIsTrue(t *testing.T) {
	ran, err := runQuickStart(t, readREADME(t))
	require.NoError(t, err)

	// The four steps are all there: scan the SQLite sample, show it, run a query, and scan PostgreSQL by
	// the name of the variable that holds its URL.
	var verbs []string
	for _, words := range ran {
		verb := words[0]
		if verb == "query" {
			verb += " " + words[1]
		}
		verbs = append(verbs, verb)
	}
	assert.Contains(t, verbs, "scan")
	assert.Contains(t, verbs, "show")
	assert.Contains(t, verbs, "query run")
	var scans []string
	for _, words := range ran {
		if words[0] == "scan" {
			scans = append(scans, strings.Join(words, " "))
		}
	}
	require.Len(t, scans, 2, "one scan of SQLite and one of PostgreSQL")
	assert.Contains(t, scans[0], "-D sqlite3")
	assert.Contains(t, scans[1], "--driver postgres --dsn-env "+quickStartDatabaseVariable)
}

// The test fails when a command or an output block of the quick start is changed without the other.
func TestQuickStartTestCatchesAChangeOfOnlyOneSide(t *testing.T) {
	readme := readREADME(t)
	section, err := quickStartSection(readme)
	require.NoError(t, err)
	require.Contains(t, section, "Project shop-project\n", "the quick start shows the output of show")

	mutations := []struct {
		name   string
		change func(readme string) string
		want   string
	}{
		{"an output block changes", func(r string) string {
			return strings.Replace(r, "\nProject shop-project\n", "\nProject shop-projekt\n", 1)
		}, "the README says it writes"},
		{"a command changes", func(r string) string {
			return strings.Replace(r, "--env local", "--env staging", 1)
		}, "the README says it writes"},
		{"a command is added", func(r string) string {
			return strings.Replace(r, "\n$ datatug show -d shop-project\n", "\n$ datatug show -d shop-project\n$ datatug show -d shop-project\n", 1)
		}, "the README says it writes"},
		{"a program the test does not run", func(r string) string {
			return strings.Replace(r, "\n$ datatug show -d shop-project\n", "\n$ curl https://example.com\n$ datatug show -d shop-project\n", 1)
		}, `a program this test does not run`},
		{"output is shown for a command that writes none", func(r string) string {
			return strings.Replace(r, "--env local\n```", "--env local\nsomething\n```", 1)
		}, "the README says it writes"},
		{"output with no command above it", func(r string) string {
			return strings.Replace(r, "\n$ datatug show -d shop-project\n", "\nsomething\n$ datatug show -d shop-project\n", 1)
		}, "output with no command above it"},
		{"the quick start is gone", func(r string) string {
			return strings.Replace(r, "## Quick start", "## Something else", 1)
		}, `no section "## Quick start"`},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			changed := mutation.change(readme)
			require.NotEqual(t, readme, changed, "the mutation changes the README")
			_, err := runQuickStart(t, changed)
			require.Error(t, err)
			assert.Contains(t, err.Error(), mutation.want)
		})
	}
}

func TestShellWords(t *testing.T) {
	for _, test := range []struct {
		text string
		want []string
		err  bool
	}{
		{text: `datatug scan -d x`, want: []string{"datatug", "scan", "-d", "x"}},
		{text: `export A='b c'`, want: []string{"export", "A=b c"}},
		{text: "sqlite3 f \"a \\\"b\\\" \\\\ c;\nd\"", want: []string{"sqlite3", "f", "a \"b\" \\ c;\nd"}},
		{text: `echo ""`, want: []string{"echo", ""}},
		{text: `echo 'open`, err: true},
	} {
		got, err := shellWords(test.text)
		if test.err {
			assert.Error(t, err, test.text)
			continue
		}
		require.NoError(t, err, test.text)
		assert.Equal(t, test.want, got, test.text)
	}
	_, err := parseTranscript("output with no command")
	assert.Error(t, err)
	_, err = parseTranscript("$ echo 'never closed\nmore")
	assert.Error(t, err)
}

// A project that held a part of the URL of the variable is found.
func TestCheckNoConnectionInFolder(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.WriteFile("shop.db", []byte("hunter2 in a database file is not a project file"), 0o644))
	require.NoError(t, os.WriteFile("a.json", []byte(`{"dsnEnv":"DATATUG_X"}`), 0o644))
	connection := "postgres://me:hunter2@db.example.com:5432/shop"
	assert.NoError(t, checkNoConnectionInFolder([]string{connection, "not a url"}))
	require.NoError(t, os.WriteFile("b.json", []byte(`{"host":"db.example.com:5432"}`), 0o644))
	assert.ErrorContains(t, checkNoConnectionInFolder([]string{connection}), "b.json")
	require.NoError(t, os.WriteFile("b.json", []byte(`hunter2`), 0o644))
	assert.ErrorContains(t, checkNoConnectionInFolder([]string{connection}), "hunter2")
}
