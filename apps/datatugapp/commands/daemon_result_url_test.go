package commands

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/agentaccess"
	"github.com/datatug/datatug-cli/pkg/daemon"
	"github.com/datatug/datatug-cli/pkg/queryresults"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDaemonURLOfAResult(t *testing.T) {
	root := resultsHome(t)
	useDaemonFake(t, &daemonFake{})
	t.Setenv(daemon.UIBaseEnv, "")
	_, stderr, code := runQuery(t, "", "--db", "sqlite://"+chinookFixture(t), "--from", "Genre", "--no-policies", "--result-id", "t4")
	require.Zero(t, code, stderr)

	out, err := runDaemon(t, "url", "--result", "t4", "--view=sources", "--row", "2")
	require.NoError(t, err)
	var link daemon.ResultLink
	require.NoError(t, json.Unmarshal([]byte(out), &link))
	assert.Equal(t, daemon.ResultLink{Contract: daemon.ContractVersion, Warnings: []string{}, URL: "https://datatug.app/store/http-127.0.0.1:8989/result/t4?view=sources&row=2", Kind: "query-result", UISupport: "available", ResultExists: true}, link)
	assert.NotContains(t, out, root)

	out, err = runDaemon(t, "url", "--result", "t4", "--format", "text")
	require.NoError(t, err)
	assert.Equal(t, "https://datatug.app/store/http-127.0.0.1:8989/result/t4?view=table\n", out)

	out, err = runDaemon(t, "url", "--result", "missing")
	require.NoError(t, err)
	link = daemon.ResultLink{}
	require.NoError(t, json.Unmarshal([]byte(out), &link))
	assert.False(t, link.ResultExists)
	assert.Empty(t, link.URL)
	assert.NotContains(t, out, `"url"`)

	for _, argv := range [][]string{
		{"url", "--result", "../x"},
		{"url", "--result", "t4", "--view=chart"},
		{"url", "--result", "t4", "--table", "Genre"},
		{"url", "--result", "t4", "--records"},
		{"url", "--result", "t4", "--format", "x"},
		{"url", "--result", "t4", "--row", "0"},
		{"url", "--result", "t4", "--result-view", "chart"},
		{"url", "--result", "t4", "--result-view", "table", "--view=sources"},
		{"url", "--result", "t4", "--result-view", "table", "--view"},
		{"url", "--result-view", "sources"},
		{"url", "--project", "p", "--env", "e", "--db", "d", "--table", "t", "--row", "5"},
		{"url", "--project", "p", "--env", "e", "--db", "d", "--table", "t", "--view=sources"},
	} {
		_, err := runDaemon(t, argv...)
		assert.Equal(t, exitCodeUsage, exitCodeOf(t, err), strings.Join(argv, " "))
	}
	// --result-view takes a space-separated value, alone or beside the same --view=<x>
	for _, argv := range [][]string{
		{"url", "--result", "t4", "--result-view", "sources", "--row", "2"},
		{"url", "--result", "t4", "--result-view=sources", "--view=sources", "--row", "2"},
	} {
		out, err = runDaemon(t, argv...)
		require.NoError(t, err, strings.Join(argv, " "))
		assert.Contains(t, out, "/result/t4?view=sources&row=2")
	}
	// a table link still takes a bare --view, and --view=true
	out, err = runDaemon(t, "url", "--project", "p", "--env", "e", "--db", "d", "--table", "t", "--view=true")
	require.NoError(t, err)
	assert.Contains(t, out, "/view/t?schema=")
	out, err = runDaemon(t, "url", "--project", "p", "--env", "e", "--db", "d", "--table", "t", "--view=false")
	require.NoError(t, err)
	assert.Contains(t, out, "/table/t?schema=")

	// a store that cannot be opened reports the result as not there
	orig := openResultsStore
	t.Cleanup(func() { openResultsStore = orig })
	openResultsStore = func() (*queryresults.Store, error) { return nil, &queryresults.Error{Reason: queryresults.ReasonOwner} }
	out, err = runDaemon(t, "url", "--result", "t4")
	require.NoError(t, err)
	assert.Contains(t, out, `"resultExists": false`)
}

// A running agent's result link carries a one-time code in its fragment; a
// result that is not there gets no link and so no code is minted.
func TestDaemonURLOfAResultMintsTheCode(t *testing.T) {
	resultsHome(t)
	useDaemonFake(t, &daemonFake{up: true})
	t.Setenv(daemon.UIBaseEnv, "")
	dir := t.TempDir()
	t.Setenv(agentaccess.DirEnv, dir)
	secret, _ := agentaccess.NewSecret()
	require.NoError(t, agentaccess.WriteKey(dir, daemon.DefaultPort, secret))
	_, stderr, code := runQuery(t, "", "--db", "sqlite://"+chinookFixture(t), "--from", "Genre", "--no-policies", "--result-id", "r1")
	require.Zero(t, code, stderr)

	out, err := runDaemon(t, "url", "--result", "r1", "--format", "text")
	require.NoError(t, err)
	page, frag, _ := strings.Cut(strings.TrimSpace(out), "#")
	assert.Equal(t, "https://datatug.app/store/http-127.0.0.1:8989/result/r1?view=table", page)
	require.True(t, strings.HasPrefix(frag, "code="), out)
	_, err = agentaccess.New(secret).Exchange(strings.TrimPrefix(frag, "code="))
	assert.NoError(t, err)

	out, err = runDaemon(t, "url", "--result", "gone")
	require.NoError(t, err)
	assert.NotContains(t, out, "code=")
	assert.Contains(t, out, `"running": true`)
}
