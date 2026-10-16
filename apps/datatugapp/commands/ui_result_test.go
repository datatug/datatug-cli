package commands

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-cli/pkg/queryresults"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runUICommand runs `datatug ui <argv>` and answers its exit code.
func runUICommand(t *testing.T, argv ...string) (string, int) {
	t.Helper()
	cmd := uiCommandArgs()
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(argv)
	err := cmd.ExecuteContext(context.Background())
	if err == nil {
		return out.String(), 0
	}
	var coder ExitCoder
	if errors.As(err, &coder) {
		return err.Error(), coder.ExitCode()
	}
	return err.Error(), 1
}

// registeredItemProject is a registered project "covc" with one SQLite source
// "main" holding two Item rows, and the folder it lives in.
func registeredItemProject(t *testing.T) (dir, db string) {
	t.Helper()
	db = covCSQLiteFile(t, "main.sqlite", `CREATE TABLE Item(id INTEGER PRIMARY KEY, name TEXT)`, `INSERT INTO Item VALUES (1,'a'),(2,'b')`)
	dir = covCWriteProject(t, covCMerge(
		covCEnvFiles(map[string]string{"main": db}),
		covCQueryFiles("f", "items", "DTQL", "dtql", "from: {name: Item}\n", `,"targets":[{"catalog":"main"}]`),
		covCQueryFiles("f", "sqlq", "SQL", "sql", "SELECT id FROM Item", `,"targets":[{"catalog":"main"}]`),
	))
	require.NoError(t, os.WriteFile(filepath.Join(os.Getenv("HOME"), ".datatug.yaml"), []byte("projects:\n  - id: covc\n    path: "+dir+"\n"), 0o600))
	return dir, db
}

func TestUIResultOpensTheLoadedRecordsWithoutRunningTheQuery(t *testing.T) {
	resultsHome(t)
	_, db := registeredItemProject(t)
	_, stderr, code := runQuery(t, "", "--db", "datatug://covc/main", "--from", "Item", "--no-policies", "--result-id", "t4")
	require.Zero(t, code, stderr)
	// the database no longer holds those rows: whatever the workspace shows came from the files
	execSQLite(t, db, `DELETE FROM Item`)
	t.Chdir(t.TempDir())

	t.Cleanup(chat.SetRunTeaProgramForTest(func(*tea.Program) (tea.Model, error) { return nil, nil }))
	restore := newSessionChatUI
	t.Cleanup(func() { newSessionChatUI = restore })
	var opened chat.ChatSession
	newSessionChatUI = func(ctx context.Context, sessions *chat.SessionChat, model string) (*chat.ChatUI, error) {
		snapshot, err := sessions.Snapshot(ctx)
		require.NoError(t, err)
		opened = snapshot
		return restore(ctx, sessions, model)
	}
	out, code := runUICommand(t, "--result", "t4")
	require.Zero(t, code, out)
	require.Len(t, opened.RecordSets, 1)
	for _, record := range opened.RecordSets {
		assert.Equal(t, "from:\n  name: Item\n", record.DTQL)
		require.Len(t, record.Result.Rows, 2)
		assert.Equal(t, "a", record.Result.Rows[0].Data["name"])
		assert.Equal(t, int64(2), record.Result.Rows[1].Data["id"])
	}
	assert.Equal(t, resultPrompt, opened.Messages[0].Text)

	// a result that cannot be put in the chat store is said, not hidden
	restoreSeed := seedResult
	t.Cleanup(func() { seedResult = restoreSeed })
	seedResult = func(context.Context, resultSessionStore, string, *resultOpen) error {
		return errors.New("store is full")
	}
	out, code = runUICommand(t, "--result", "t4")
	assert.Equal(t, exitCodeUsage, code)
	assert.Contains(t, out, "open the query result: store is full")
}

func execSQLite(t *testing.T, path, statement string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	_, err = db.Exec(statement)
	require.NoError(t, err)
}

func TestUIResultRefusals(t *testing.T) {
	root := resultsHome(t)
	registeredItemProject(t)
	_, _, code := runQuery(t, "", "--db", "sqlite://"+chinookFixture(t), "--from", "Genre", "--no-policies", "--result-id", "byPath")
	require.Zero(t, code)
	_, _, code = runQuery(t, "", "--db", "datatug://covc/main", "--from", "Item", "--no-policies", "--result-id", "half")
	require.Zero(t, code)
	require.NoError(t, os.Remove(filepath.Join(root, "half", "data.json")))
	for _, tc := range []struct {
		argv []string
		code int
	}{
		{[]string{"--result", "nope"}, exitCodeNotFound},
		{[]string{"--result", "half"}, exitCodeNotFound},
		{[]string{"--result", "../x"}, exitCodeUsage},
		{[]string{"--result", "byPath"}, exitCodeUsage},
		{[]string{"--result", "t1", "--file", "x.db"}, exitCodeUsage},
	} {
		out, code := runUICommand(t, tc.argv...)
		assert.Equal(t, tc.code, code, "%v: %s", tc.argv, out)
		assert.NotContains(t, out, root)
	}
	// an expired result
	_, _, code = runQuery(t, "", "--db", "datatug://covc/main", "--from", "Item", "--no-policies", "--result-id", "old")
	require.Zero(t, code)
	metaPath := filepath.Join(root, "old", "meta.json")
	content, _ := os.ReadFile(metaPath)
	var meta map[string]any
	require.NoError(t, json.Unmarshal(content, &meta))
	meta["expiresAt"] = "2001-01-01T00:00:00Z"
	expired, _ := json.Marshal(meta)
	require.NoError(t, os.WriteFile(metaPath, expired, 0o600))
	_, code = runUICommand(t, "--result", "old")
	assert.Equal(t, exitCodeNotFound, code)

	// a project that is no longer registered
	_, _, code = runQuery(t, "", "--db", "datatug://covc/main", "--from", "Item", "--no-policies", "--result-id", "orphan")
	require.Zero(t, code)
	require.NoError(t, os.WriteFile(filepath.Join(os.Getenv("HOME"), ".datatug.yaml"), []byte("projects: []\n"), 0o600))
	_, code = runUICommand(t, "--result", "orphan")
	assert.Equal(t, exitCodeNotFound, code)

	// a result directory that is a link is not followed
	require.NoError(t, os.Symlink(filepath.Join(root, "orphan"), filepath.Join(root, "linked")))
	_, code = runUICommand(t, "--result", "linked")
	assert.Equal(t, exitCodeUsage, code)

	orig := openResultsStore
	t.Cleanup(func() { openResultsStore = orig })
	openResultsStore = func() (*queryresults.Store, error) { return nil, &queryresults.Error{Reason: queryresults.ReasonOwner} }
	_, code = runUICommand(t, "--result", "t1")
	assert.Equal(t, exitCodeUsage, code)
	openResultsStore = func() (*queryresults.Store, error) {
		store, err := orig()
		require.NoError(t, err)
		require.NoError(t, os.Chmod(filepath.Join(store.Root(), "orphan"), 0o000))
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(store.Root(), "orphan"), 0o700) })
		return store, nil
	}
	_, code = runUICommand(t, "--result", "orphan")
	assert.Equal(t, exitCodeNotFound, code, "an unreadable result is incomplete")
}

func TestLoadResultForUIOfSavedQueries(t *testing.T) {
	resultsHome(t)
	dir, _ := registeredItemProject(t)
	for _, id := range []string{"f/items", "f/sqlq"} {
		_, stderr, code := runQuery(t, "", "--project", dir, "--query", id, "--env", "local", "--result-id", map[string]string{"f/items": "s1", "f/sqlq": "s2"}[id])
		require.Zero(t, code, stderr)
	}
	dtqlResult, err := loadResultForUI(context.Background(), "s1")
	require.NoError(t, err)
	assert.Equal(t, "f/items", dtqlResult.query.QueryID)
	assert.Equal(t, "from: {name: Item}\n", dtqlResult.query.DTQL, "the saved query's own text")
	assert.Equal(t, dir, dtqlResult.projectDir)
	sqlResult, err := loadResultForUI(context.Background(), "s2")
	require.NoError(t, err)
	assert.Equal(t, "f/sqlq", sqlResult.query.QueryID)
	assert.Empty(t, sqlResult.query.DTQL)
}

func TestResultValuesAndParameters(t *testing.T) {
	assert.Nil(t, nativeOf(apicontract.NewNullValue()))
	assert.Equal(t, int64(5), nativeOf(apicontract.NewIntegerValue("5")))
	assert.Equal(t, "99999999999999999999", nativeOf(apicontract.NewIntegerValue("99999999999999999999")))
	assert.Equal(t, 1.5, nativeOf(apicontract.NewNumberValue(1.5)))
	assert.Equal(t, true, nativeOf(apicontract.NewBooleanValue(true)))
	assert.Equal(t, "9.99", nativeOf(apicontract.NewDecimalValue("9.99")))
	assert.Nil(t, nativeParameters(nil))
	set, err := apicontract.NewTypedValueSet(apicontract.NewIntegerValue("1"), apicontract.NewIntegerValue("2"))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"ids": []any{int64(1), int64(2)}, "n": "x"}, nativeParameters(map[string]apicontract.TypedValueOrSet{
		"ids": apicontract.SetValue(set), "n": apicontract.ScalarValue(apicontract.NewStringValue("x")),
	}))
}

type failingResultStore struct{ create, user, query error }

func (s failingResultStore) Create(context.Context, string) (chat.ChatSession, error) {
	return chat.ChatSession{ID: "s"}, s.create
}
func (s failingResultStore) AppendUser(context.Context, string, string) (chat.ChatMessage, error) {
	return chat.ChatMessage{ID: "m"}, s.user
}
func (s failingResultStore) AppendQuery(context.Context, string, string, string, chat.QueryResult) (chat.QueryResult, error) {
	return chat.QueryResult{}, s.query
}

func TestSeedResultSessionFailures(t *testing.T) {
	boom := errors.New("boom")
	ctx, result := context.Background(), &resultOpen{}
	assert.ErrorIs(t, seedResultSession(ctx, failingResultStore{create: boom}, "s", result), boom)
	assert.ErrorIs(t, seedResultSession(ctx, failingResultStore{user: boom}, "s", result), boom)
	assert.ErrorIs(t, seedResultSession(ctx, failingResultStore{query: boom}, "s", result), boom)
	assert.NoError(t, seedResultSession(ctx, failingResultStore{}, "s", result))
}

func TestRunUnifiedResultApp(t *testing.T) {
	tempDatatugHome(t)
	restore := runChatProjectFunc
	t.Cleanup(func() { runChatProjectFunc = restore })
	var seen []chatOptions
	answers := []struct {
		next string
		err  error
	}{}
	runChatProjectFunc = func(_ *cobra.Command, options chatOptions) (string, error) {
		seen = append(seen, options)
		answer := answers[0]
		answers = answers[1:]
		return answer.next, answer.err
	}
	result := resultOpen{projectDir: "/p", env: "e", database: "d"}
	boom := errors.New("boom")
	answers = append(answers, struct {
		next string
		err  error
	}{"", boom})
	assert.ErrorIs(t, runUnifiedResultApp(nil, datatugui.Options{}, result, chatOptions{as: "alice", roles: []string{"r"}}), boom)
	require.Len(t, seen, 1)
	assert.Equal(t, "/p", seen[0].project)
	assert.Equal(t, "e", seen[0].env)
	assert.Equal(t, "d", seen[0].database)
	assert.Equal(t, "alice", seen[0].as)
	require.NotNil(t, seen[0].result)

	// switching to another project continues as the ordinary workspace
	seen = nil
	answers = append(answers, struct {
		next string
		err  error
	}{"other", nil}, struct {
		next string
		err  error
	}{"", nil})
	require.NoError(t, runUnifiedResultApp(nil, datatugui.Options{}, result, chatOptions{}))
	require.Len(t, seen, 2)
	assert.Equal(t, "other", seen[1].project)
	assert.Nil(t, seen[1].result)

	// a remembered AI profile that no longer resolves is dropped
	seen = nil
	optionsPath := filepath.Join(t.TempDir(), "last.json")
	require.NoError(t, os.WriteFile(optionsPath, []byte(`{"ai":"missing-profile"}`), 0o600))
	restorePath := lastChatOptionsPath
	t.Cleanup(func() { lastChatOptionsPath = restorePath })
	lastChatOptionsPath = func() (string, error) { return optionsPath, nil }
	answers = append(answers, struct {
		next string
		err  error
	}{"", nil})
	require.NoError(t, runUnifiedResultApp(nil, datatugui.Options{}, result, chatOptions{}))
	assert.Empty(t, seen[0].ai)
}
