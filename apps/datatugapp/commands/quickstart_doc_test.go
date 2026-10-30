package commands

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
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
// The only other block of the section is the installation command, which is not run: it must be the one
// ```bash block, and say exactly quickStartInstall. A block of any other kind (or with no kind) fails the
// test, so that changing the kind of a block never takes it out of the test without a word.

const quickStartDatabaseVariable = "DATATUG_SHOP_URL"

// quickStartInstall is the one command of the quick start that is shown and not run.
const quickStartInstall = "brew install --cask datatug/tap/datatug"

// quickStartAccessLine is the line the README says the query writes to stderr.
const quickStartAccessLine = "access: running without access policies"

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

// fencedBlock is a fenced block of the README: its kind (the word after the three backticks) and its lines.
type fencedBlock struct {
	kind string
	body string
}

// fencedBlocks are the fenced blocks of text, in order.
func fencedBlocks(text string) (blocks []fencedBlock) {
	var current []string
	kind, in := "", false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case in && trimmed == "```":
			blocks = append(blocks, fencedBlock{kind: kind, body: strings.Join(current, "\n")})
			current, in = nil, false
		case in:
			current = append(current, line)
		case strings.HasPrefix(trimmed, "```"):
			kind, in = strings.TrimSpace(strings.TrimPrefix(trimmed, "```")), true
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

// quickStartRun is what a run of the quick start did: each datatug command it ran (the words after
// "datatug") and what that command wrote to stderr, as the process would show it (what the command wrote
// to its error stream, and the log, which is where the scan reports its progress).
type quickStartRun struct {
	commands [][]string
	stderr   []string
}

// runQuickStart runs the quick start of readme in a new folder and returns the first way in which it
// is not true, and what it ran.
func runQuickStart(t *testing.T, readme string) (run quickStartRun, err error) {
	t.Helper()
	section, err := quickStartSection(readme)
	if err != nil {
		return run, err
	}
	var consoles []string
	installs := 0
	for _, block := range fencedBlocks(section) {
		switch {
		case block.kind == "console":
			consoles = append(consoles, block.body)
		case block.kind == "bash" && installs == 0 && block.body == quickStartInstall:
			installs++
		default:
			return run, fmt.Errorf("the quick start has a fenced block that is neither a console block nor the one installation command %q: kind %q, %q", quickStartInstall, block.kind, block.body)
		}
	}
	if len(consoles) == 0 {
		return run, errors.New("the quick start has no console block")
	}
	if installs != 1 {
		return run, fmt.Errorf("the quick start shows the installation command %q %d times, not once", quickStartInstall, installs)
	}

	t.Chdir(t.TempDir())
	// The scan reports its progress through the log, with the time of each line: that is not output
	// the README shows, and it is stderr.
	oldLog := log.Writer()
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(oldLog) })

	t.Cleanup(api.SetOpenSchemaScanForTest(func(ref dbcopy.BackendRef, _ context.Context) (dbcopy.SchemaScanDB, error) {
		if ref.Raw != "env:"+quickStartDatabaseVariable {
			return nil, errors.New("the quick start opened a source that this test is not set up for")
		}
		return quickStartPostgres(), nil
	}))
	var exported []string

	for _, block := range consoles {
		steps, parseErr := parseTranscript(block)
		if parseErr != nil {
			return run, parseErr
		}
		for _, step := range steps {
			if len(step.words) == 0 {
				return run, errors.New("a command is empty")
			}
			command := strings.Join(step.words, " ")
			switch step.words[0] {
			case "sqlite3":
				if len(step.words) != 3 {
					return run, fmt.Errorf("%q: sqlite3 takes a file and the statements to run on it", command)
				}
				if err = runQuickStartSQLite(step.words[1], step.words[2]); err != nil {
					return run, fmt.Errorf("%q: %w", command, err)
				}
			case "export":
				if len(step.words) != 2 || !strings.Contains(step.words[1], "=") {
					return run, fmt.Errorf("%q: export takes NAME=value", command)
				}
				name, value, _ := strings.Cut(step.words[1], "=")
				t.Setenv(name, value)
				exported = append(exported, value)
			case "datatug":
				logged.Reset()
				root := DatatugCommand()
				var stdout, stderr bytes.Buffer
				root.SetArgs(step.words[1:])
				root.SetOut(&stdout)
				root.SetErr(&stderr)
				root.SilenceUsage, root.SilenceErrors = true, true
				if err = root.Execute(); err != nil {
					return run, fmt.Errorf("%q failed: %w", command, err)
				}
				run.commands = append(run.commands, step.words[1:])
				run.stderr = append(run.stderr, stderr.String()+logged.String())
				if stdout.String() != step.want {
					return run, fmt.Errorf("%q wrote\n%s\nand the README says it writes\n%s", command, stdout.String(), step.want)
				}
				continue
			default:
				return run, fmt.Errorf("the quick start runs %q, a program this test does not run", step.words[0])
			}
			if step.want != "" {
				return run, fmt.Errorf("%q writes nothing, and the README shows output for it", command)
			}
		}
	}
	return run, checkNoConnectionInFolder(exported)
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

// The commands of the quick start, exactly: a change of the commands of the README (or a block that is
// no longer run) changes this list, and the author reads what the quick start now says.
var quickStartCommands = [][]string{
	{"scan", "-d", "shop-project", "-D", "sqlite3", "--path", "shop.db", "--db", "shop", "--env", "local"},
	{"show", "-d", "shop-project"},
	{"query", "run", "--db", "sqlite://./shop.db", "--from", "Customer", "--no-policies"},
	{"scan", "-d", "shop-pg-project", "--driver", "postgres", "--dsn-env", quickStartDatabaseVariable, "--db", "shop", "--env", "prod"},
	{"show", "-d", "shop-pg-project"},
}

// The quick start of the README is true: every command runs, and writes what the README says.
func TestQuickStartOfTheREADMEIsTrue(t *testing.T) {
	run, err := runQuickStart(t, readREADME(t))
	require.NoError(t, err)

	// The five commands are all there, in this order: scan the SQLite sample, show it, run a query, and scan
	// and show PostgreSQL by the name of the variable that holds its URL.
	assert.Equal(t, quickStartCommands, run.commands)

	// What the README says of stderr: the scan reports its progress there (and writes nothing to stdout, which
	// the empty output under it checks), and the query says it runs without access policies.
	require.Len(t, run.stderr, len(quickStartCommands))
	assert.Contains(t, run.stderr[0], "Scanner completed", "the scan reports its progress on stderr")
	assert.Contains(t, run.stderr[2], quickStartAccessLine)
	assert.Empty(t, run.stderr[1], "show writes only to stdout")
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
		{"the kind of a console block changes", func(r string) string {
			return strings.Replace(r, "```console\n$ datatug show -d shop-project\n", "```text\n$ datatug show -d shop-project\n", 1)
		}, "neither a console block nor the one installation command"},
		{"a block has no kind", func(r string) string {
			return strings.Replace(r, "```console\n$ datatug show -d shop-project\n", "```\n$ datatug show -d shop-project\n", 1)
		}, "neither a console block nor the one installation command"},
		{"the installation command changes", func(r string) string {
			return strings.Replace(r, "```bash\nbrew install --cask datatug/tap/datatug\n```", "```bash\nbrew install datatug\n```", 1)
		}, "neither a console block nor the one installation command"},
		{"the installation command is shown twice", func(r string) string {
			return strings.Replace(r, "```bash\nbrew install --cask datatug/tap/datatug\n```", "```bash\nbrew install --cask datatug/tap/datatug\n```\n\n```bash\nbrew install --cask datatug/tap/datatug\n```", 1)
		}, "neither a console block nor the one installation command"},
		{"the installation command is gone", func(r string) string {
			return strings.Replace(r, "```bash\nbrew install --cask datatug/tap/datatug\n```", "", 1)
		}, "not once"},
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

// A quick start with the installation command and nothing to run is not a quick start.
func TestQuickStartWithNoConsoleBlockFails(t *testing.T) {
	_, err := runQuickStart(t, "## Quick start\n\n```bash\n"+quickStartInstall+"\n```\n")
	assert.ErrorContains(t, err, "no console block")
}
