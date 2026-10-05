package commands

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/go-git/go-git/v5"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	_ "modernc.org/sqlite"
)

func TestCov100hConsoleCommandArgsExecute(t *testing.T) {
	cmd := consoleCommandArgs()
	require.NoError(t, cmd.ExecuteContext(context.Background()))
}

func TestCov100hShowCommandArgs(t *testing.T) {
	cmd := showCommandArgs()
	require.Equal(t, "show", cmd.Name())
	t.Chdir(t.TempDir()) // not a project
	require.Error(t, cmd.ExecuteContext(context.Background()))
}

func TestCov100hConfigGetSettingsFail(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	err := configCommandAction(nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get config")
}

func TestCov100hBuildDirectivesFromFlags(t *testing.T) {
	newCmd := func() *cobra.Command {
		cmd := &cobra.Command{}
		cmd.Flags().String("filter-config", "", "")
		cmd.Flags().String("include", "", "")
		cmd.Flags().String("exclude", "", "")
		cmd.Flags().StringArray("where", nil, "")
		cmd.Flags().StringArray("limit", nil, "")
		return cmd
	}

	cmd := newCmd()
	_ = cmd.Flags().Set("filter-config", "/no/such.yaml")
	_ = cmd.Flags().Set("include", "t")
	_, err := buildDirectivesFromFlags(cmd)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")

	cfg := filepath.Join(t.TempDir(), "f.yaml")
	require.NoError(t, os.WriteFile(cfg, []byte("include:\n  - Customer\n"), 0o600))
	cmd = newCmd()
	_ = cmd.Flags().Set("filter-config", cfg)
	d, err := buildDirectivesFromFlags(cmd)
	require.NoError(t, err)
	require.NotNil(t, d)

	cmd = newCmd()
	_ = cmd.Flags().Set("include", "A")
	_ = cmd.Flags().Set("where", "A:id:=:1")
	_ = cmd.Flags().Set("limit", "A:5")
	d, err = buildDirectivesFromFlags(cmd)
	require.NoError(t, err)
	require.NotNil(t, d)
}

func TestCov100hStageFiles(t *testing.T) {
	dir := t.TempDir()
	_, err := git.PlainInit(dir, false)
	require.NoError(t, err)
	require.NoError(t, stageFiles(dir, nil))

	f := filepath.Join(dir, "a.txt")
	require.NoError(t, os.WriteFile(f, []byte("x"), 0o600))
	require.NoError(t, stageFiles(dir, []string{f}))

	require.Error(t, stageFiles(t.TempDir(), []string{filepath.Join(t.TempDir(), "x")}))
}

func TestCov100hDefaultCloneOrUpdateRepo(t *testing.T) {
	exist := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(exist, "x"), []byte("1"), 0o600))
	require.Error(t, defaultCloneOrUpdateRepo(exist, "/tmp/nope"))

	repoDir := t.TempDir()
	_, err := git.PlainInit(repoDir, false)
	require.NoError(t, err)
	require.Error(t, defaultCloneOrUpdateRepo(repoDir, "https://example.invalid/repo.git"))
}

func TestCov100hDefaultServeDemoProjectFlagFailures(t *testing.T) {
	oldSet, oldServe := demoSetFlag, demoServeAction
	t.Cleanup(func() { demoSetFlag, demoServeAction = oldSet, oldServe })

	calls := 0
	demoSetFlag = func(cmd *cobra.Command, name, value string) error {
		calls++
		if calls == 2 {
			return errors.New("second flag fail")
		}
		return cmd.Flags().Set(name, value)
	}
	require.Error(t, defaultServeDemoProject("/tmp/p"))

	calls = 0
	demoSetFlag = func(cmd *cobra.Command, name, value string) error {
		calls++
		if calls == 3 {
			return errors.New("third flag fail")
		}
		return cmd.Flags().Set(name, value)
	}
	require.Error(t, defaultServeDemoProject("/tmp/p"))

	demoSetFlag = func(cmd *cobra.Command, name, value string) error {
		return cmd.Flags().Set(name, value)
	}
	demoServeAction = func(*cobra.Command, []string) error { return errors.New("serve fail") }
	require.Error(t, defaultServeDemoProject("/tmp/p"))
}

type tokenCtxSource struct{}

func (tokenCtxSource) Token() (*oauth2.Token, error) { return nil, errors.New("unused") }
func (tokenCtxSource) TokenContext(context.Context) (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: "ctx"}, nil
}

func TestCov100hTokenWithContext(t *testing.T) {
	src := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "t", Expiry: time.Now().Add(time.Hour)})
	tok, err := tokenWithContext(context.Background(), src)
	require.NoError(t, err)
	assert.Equal(t, "t", tok.AccessToken)

	tok, err = tokenWithContext(context.Background(), tokenCtxSource{})
	require.NoError(t, err)
	assert.Equal(t, "ctx", tok.AccessToken)
}

func TestCov100hCloudChatClientErrorBranches(t *testing.T) {
	_, _, err := cloudChatClient(context.Background(), chatOptions{apiKey: "k"})
	require.Error(t, err)

	oldDir, oldSrc := chatUserConfigDir, chatSavedTokenSource
	t.Cleanup(func() { chatUserConfigDir, chatSavedTokenSource = oldDir, oldSrc })

	chatUserConfigDir = func() (string, error) { return "", errors.New("cfg boom") }
	_, _, err = cloudChatClient(context.Background(), chatOptions{baseURL: "https://example.com/v0/"})
	require.Error(t, err)

	cfg := t.TempDir()
	chatUserConfigDir = func() (string, error) { return cfg, nil }
	chatSavedTokenSource = func(context.Context, bool) (oauth2.TokenSource, error) {
		return nil, errors.New("login boom")
	}
	_, _, err = cloudChatClient(context.Background(), chatOptions{baseURL: "https://example.com/v0/"})
	require.Error(t, err)

	chatSavedTokenSource = func(context.Context, bool) (oauth2.TokenSource, error) {
		return oauth2.StaticTokenSource(&oauth2.Token{}), nil
	}
	_, _, err = cloudChatClient(context.Background(), chatOptions{baseURL: "https://example.com/v0/"})
	require.Error(t, err)
}

func TestCov100hIncidentFactSignalsAndMutation(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().StringSlice("entity", nil, "")
	cmd.Flags().String(incidentMutationFlag, "", "")
	_ = cmd.Flags().Set("entity", "Order.id=7")
	facts, err := incidentFactSignals(cmd)
	require.NoError(t, err)
	require.Len(t, facts, 1)

	cmd2 := &cobra.Command{}
	cmd2.Flags().StringSlice("entity", nil, "")
	_ = cmd2.Flags().Set("entity", "bad")
	_, err = incidentFactSignals(cmd2)
	require.Error(t, err)

	id := incidentMutationID(cmd)
	assert.NotEmpty(t, id)
	_ = cmd.Flags().Set(incidentMutationFlag, "fixed-id")
	assert.Equal(t, "fixed-id", incidentMutationID(cmd))
}

func TestCov100hIncidentJSONStreamAndResponseError(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String(incidentFormatFlag, "", "")
	cmd.Flags().Bool(executionJSONFlag, false, "")
	asJSON, err := incidentJSONStream(cmd)
	require.NoError(t, err)
	assert.False(t, asJSON)

	_ = cmd.Flags().Set(executionJSONFlag, "true")
	asJSON, err = incidentJSONStream(cmd)
	require.NoError(t, err)
	assert.True(t, asJSON)

	cmd2 := &cobra.Command{}
	cmd2.Flags().String(incidentFormatFlag, "", "")
	cmd2.Flags().Bool(executionJSONFlag, false, "")
	_ = cmd2.Flags().Set(incidentFormatFlag, "yaml")
	_, err = incidentJSONStream(cmd2)
	require.Error(t, err)

	err = incidentResponseError(400, []byte(`{"error":{"code":"E","message":"m"}}`))
	assert.Contains(t, err.Error(), "m")
	err = incidentResponseError(500, []byte(`x`))
	assert.Contains(t, err.Error(), "HTTP 500")
}

func TestCov100hNewAgentHTTPClient(t *testing.T) {
	info := apicontract.AgentInfo{
		Version: "t", Principal: apicontract.AgentPrincipal{ID: "a"},
		SecurityContextID: "c", Projects: []apicontract.AgentProjectRef{{ID: "p"}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/datatug/agent-info" {
			_ = json.NewEncoder(w).Encode(info)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.Flags().String(compareAgentFlag, "", "")
	_ = cmd.Flags().Set(compareAgentFlag, srv.URL)
	client, got, err := newAgentHTTPClient(cmd, compareAgentFlag)
	require.NoError(t, err)
	assert.Equal(t, "t", got.Version)
	assert.NotEmpty(t, client.baseURL)

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`oops`))
	}))
	t.Cleanup(bad.Close)
	_ = cmd.Flags().Set(compareAgentFlag, bad.URL)
	_, _, err = newAgentHTTPClient(cmd, compareAgentFlag)
	require.Error(t, err)
}

func TestCov100hDatasetDataYAMLIndent(t *testing.T) {
	sample := datatug.Recordset{
		Columns: []datatug.RecordsetColumn{{Name: "id"}},
		Rows:    [][]any{{1}},
	}
	cov100fSetVar(t, &datasetDataInitProject, func(v *datasetDataCommand) error {
		v.File, v.Format, v.Indent = "x", "YAML", "3"
		v.store = cov100fStore{proj: cov100fRS{data: sample}}
		return nil
	})
	require.NoError(t, datasetDataCommandAction(nil, nil))
}

func TestCov100hEnsureSQLiteFilesEmpty(t *testing.T) {
	require.NoError(t, (demoCommand{}).ensureSQLiteFiles(nil))
}

func TestCov100hVerifySQLiteEmptyCustomer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.sqlite")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE Customer (id INTEGER)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	err = verifySQLiteFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestCov100hDownloadSQLiteCreateFail(t *testing.T) {
	old := demoSQLiteSourceURL
	t.Cleanup(func() { demoSQLiteSourceURL = old })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("x"))
	}))
	t.Cleanup(srv.Close)
	demoSQLiteSourceURL = srv.URL
	// Destination parent missing and uncreatable: use a file as parent path.
	parent := filepath.Join(t.TempDir(), "as-file")
	require.NoError(t, os.WriteFile(parent, []byte("x"), 0o600))
	err := defaultDownloadSQLiteSource(filepath.Join(parent, "child.sqlite"))
	require.Error(t, err)
}
