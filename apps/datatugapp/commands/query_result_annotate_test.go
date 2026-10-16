package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/queryresults"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runAnnotate runs `datatug query results annotate <argv>` with stdin.
func runAnnotate(t *testing.T, stdin string, argv ...string) (stdout, stderr string, code int) {
	t.Helper()
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	root := &cobra.Command{Use: "datatug", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(queryCommand())
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{"query", "results", "annotate"}, argv...))
	if err := root.ExecuteContext(context.Background()); err != nil {
		var coder ExitCoder
		code = 1
		if errors.As(err, &coder) {
			code = coder.ExitCode()
		}
		errOut.WriteString(err.Error() + "\n")
	}
	return out.String(), errOut.String(), code
}

func TestQueryResultsAnnotate(t *testing.T) {
	root := resultsHome(t)
	_, stderr, code := runQuery(t, "", "--db", "sqlite://"+chinookFixture(t), "--from", "Genre", "--no-policies", "--result-id", "p1")
	require.Equal(t, 0, code, stderr)
	metaPath := filepath.Join(root, "p1", "meta.json")
	before, err := os.ReadFile(metaPath)
	require.NoError(t, err)

	origNow := annotateNow
	t.Cleanup(func() { annotateNow = origNow })
	annotateNow = func() time.Time { return time.Date(2026, 10, 16, 1, 2, 3, 0, time.UTC) }

	const secret = "SECRET-PROMPT-TEXT"
	out, errOut, code := runAnnotate(t, "  "+secret+"\x00 and more\r\n", "--id", "p1", "--prompt-stdin")
	require.Equal(t, 0, code, errOut)
	assert.JSONEq(t, `{"id":"p1","truncated":false,"bytes":27}`, out)
	assert.NotContains(t, out+errOut, secret, "the text is never echoed")

	after, err := os.ReadFile(metaPath)
	require.NoError(t, err)
	var members map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(after, &members))
	assert.JSONEq(t, `{"text":"SECRET-PROMPT-TEXT and more","truncated":false,"at":"2026-10-16T01:02:03Z"}`, string(members["prompt"]))
	delete(members, "prompt")
	var original map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(before, &original))
	assert.Equal(t, original, members, "no other member changed")
	info, err := os.Lstat(metaPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// truncated, overwriting the earlier prompt
	out, _, code = runAnnotate(t, strings.Repeat("€", 4000), "--id", "p1", "--prompt-stdin")
	require.Equal(t, 0, code)
	assert.JSONEq(t, `{"id":"p1","truncated":true,"bytes":8190}`, out)

	// an unknown id is answered before stdin is read, empty or not
	_, _, code = runAnnotate(t, "", "--id", "nope", "--prompt-stdin")
	assert.Equal(t, exitCodeNotFound, code)

	// stdin that is a terminal is refused without being read
	origTTY := stdinIsTerminal
	t.Cleanup(func() { stdinIsTerminal = origTTY })
	stdinIsTerminal = func(io.Reader) bool { return true }
	_, stderr, code = runAnnotate(t, "never read", "--id", "p1", "--prompt-stdin")
	assert.Equal(t, exitCodeUsage, code)
	assert.Contains(t, stderr, "pipe the text on stdin")
	_, _, code = runAnnotate(t, "", "--id", "nope", "--prompt-stdin")
	assert.Equal(t, exitCodeNotFound, code, "the lookup comes before the terminal check")
	stdinIsTerminal = origTTY

	// a meta.json of a version this CLI does not know is refused
	good, err := os.ReadFile(metaPath)
	require.NoError(t, err)
	future := strings.Replace(string(good), `"version":1`, `"version":2`, 1)
	require.NoError(t, os.WriteFile(metaPath, []byte(future), 0o600))
	_, stderr, code = runAnnotate(t, "x", "--id", "p1", "--prompt-stdin")
	assert.Equal(t, exitCodeUsage, code)
	assert.Contains(t, stderr, "unsupported-version")
	require.NoError(t, os.WriteFile(metaPath, good, 0o600))

	// refusals
	_, stderr, code = runAnnotate(t, "x", "--id", "p1")
	assert.Equal(t, exitCodeUsage, code)
	assert.Contains(t, stderr, "--prompt-stdin")
	_, _, code = runAnnotate(t, "x", "--id", "p1", "--prompt-stdin", "--format", "text")
	assert.Equal(t, exitCodeUsage, code)
	_, _, code = runAnnotate(t, "x", "--id", "../x", "--prompt-stdin")
	assert.Equal(t, exitCodeUsage, code)
	_, _, code = runAnnotate(t, " \n\x01 ", "--id", "p1", "--prompt-stdin")
	assert.Equal(t, exitCodeUsage, code, "empty stdin")
	_, _, code = runAnnotate(t, "", "--id", "p1", "--prompt-stdin")
	assert.Equal(t, exitCodeUsage, code, "empty stdin")
	_, stderr, code = runAnnotate(t, secret, "--id", "nope", "--prompt-stdin")
	assert.Equal(t, exitCodeNotFound, code)
	assert.NotContains(t, stderr, secret)
	_, _, code = runAnnotate(t, "x", "--prompt-stdin")
	assert.Equal(t, 1, code, "--id is required")

	// an expired or incomplete result is a store failure
	require.NoError(t, os.Remove(metaPath))
	_, _, code = runAnnotate(t, "x", "--id", "p1", "--prompt-stdin")
	assert.Equal(t, exitCodeUsage, code)

	require.NoError(t, os.WriteFile(metaPath, good, 0o600))

	// stdin that cannot be read
	root2 := &cobra.Command{Use: "datatug", SilenceUsage: true, SilenceErrors: true}
	root2.AddCommand(queryCommand())
	root2.SetIn(errReader{})
	root2.SetArgs([]string{"query", "results", "annotate", "--id", "p1", "--prompt-stdin"})
	err = root2.ExecuteContext(context.Background())
	var coder ExitCoder
	require.True(t, errors.As(err, &coder))
	assert.Equal(t, exitCodeUsage, coder.ExitCode())

	// a store that cannot be opened
	orig := openResultsStore
	t.Cleanup(func() { openResultsStore = orig })
	openResultsStore = func() (*queryresults.Store, error) {
		return nil, &queryresults.Error{Reason: queryresults.ReasonInsideGit}
	}
	_, stderr, code = runAnnotate(t, "x", "--id", "p1", "--prompt-stdin")
	assert.Equal(t, exitCodeUsage, code)
	assert.Contains(t, stderr, "inside-git")
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

// With the real check, a reader that is not a file is not a terminal.
func TestStdinIsTerminal(t *testing.T) {
	assert.False(t, stdinIsTerminal(strings.NewReader("x")))
	file, err := os.Open(os.DevNull)
	require.NoError(t, err)
	defer func() { _ = file.Close() }()
	assert.False(t, stdinIsTerminal(file))
}
