package commands

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/investigation"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestCov100gCompareHelpers(t *testing.T) {
	assert.Equal(t, "proj", compareSideProject(apicontract.CompareSideSpec{Project: "proj"}))
	assert.Equal(t, "from-exec", compareSideProject(apicontract.CompareSideSpec{
		Execution: &apicontract.ExecutionRef{ProjectID: "from-exec"},
	}))

	for _, tc := range []struct {
		v    apicontract.TypedValue
		want string
	}{
		{apicontract.TypedValue{Type: apicontract.ValueTypeString, Str: "a"}, `"a"`},
		{apicontract.TypedValue{Type: apicontract.ValueTypeDate, Str: "2020-01-01"}, `"2020-01-01"`},
		{apicontract.TypedValue{Type: apicontract.ValueTypeDatetime, Str: "t"}, `"t"`},
		{apicontract.TypedValue{Type: apicontract.ValueTypeInteger, Str: "7"}, "7"},
		{apicontract.TypedValue{Type: apicontract.ValueTypeDecimal, Str: "1.5"}, "1.5"},
		{apicontract.TypedValue{Type: apicontract.ValueTypeNumber, Num: 3.14}, "3.14"},
		{apicontract.TypedValue{Type: apicontract.ValueTypeBoolean, Bool: true}, "true"},
		{apicontract.TypedValue{Type: apicontract.ValueTypeNull}, "null"},
		{apicontract.TypedValue{Type: "nope"}, "<invalid>"},
	} {
		assert.Equal(t, tc.want, formatCompareValue(tc.v), tc.v.Type)
	}

	err := agentResponseError(500, []byte(`{"error":{"code":"X","message":"boom"}}`))
	assert.Contains(t, err.Error(), "boom")
	err = agentResponseError(404, []byte(`not-json`))
	assert.Contains(t, err.Error(), "HTTP 404")
}

func TestCov100gAgentHTTPClientDo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/err":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"bad","message":"nope"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := agentHTTPClient{baseURL: srv.URL, client: srv.Client()}
	body, status, err := c.get(context.Background(), "/ok")
	require.NoError(t, err)
	assert.Equal(t, 200, status)
	assert.Contains(t, string(body), "ok")

	_, _, err = c.post(context.Background(), "/err", map[string]string{"a": "b"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nope")
}

func TestCov100gIncidentHelpers(t *testing.T) {
	assert.Equal(t, investigation.NewBooleanValue(true), incidentTypedValue("true"))
	assert.Equal(t, investigation.NewIntegerValue("42"), incidentTypedValue("42"))
	assert.Equal(t, investigation.NewNumberValue(1.5), incidentTypedValue("1.5"))
	assert.Equal(t, investigation.NewStringValue("hello"), incidentTypedValue("hello"))

	assert.Equal(t, "kind:a,kind:b", matchedSignalText([]incidents.MatchedSignal{
		{Kind: "kind", Value: "a"}, {Kind: "kind", Value: "b"},
	}))

	var buf bytes.Buffer
	require.NoError(t, writeIncidentGrid(&buf, incidents.IncidentView{
		Ref: incidents.IncidentRef{StoreID: "ops", IncidentID: "INC-1"}, Status: "open", Title: "t",
		Description: "d",
		CanonicalContext: investigation.ContextView{Facts: []investigation.FactView{
			{Entity: "Order", Field: "id", Value: investigation.VisibleValue(investigation.NewStringValue("1"))},
		}},
	}))
	assert.Contains(t, buf.String(), "INC-1")
	assert.Contains(t, buf.String(), "Order.id")

	detail := incidentEventDetail(incidents.Event{Type: incidents.EventNoteAdded, Payload: json.RawMessage(`{"body":"hi"}`), ID: "e1"})
	assert.Equal(t, "hi", detail)
	detail = incidentEventDetail(incidents.Event{Type: incidents.EventIncidentStatus, Payload: json.RawMessage(`{"status":"closed"}`), ID: "e1"})
	assert.Equal(t, "closed", detail)
	detail = incidentEventDetail(incidents.Event{Type: incidents.EventIncidentOutcome, Payload: json.RawMessage(`{"outcome":"resolved"}`), ID: "e1"})
	assert.Equal(t, "resolved", detail)
	detail = incidentEventDetail(incidents.Event{Type: incidents.EventIncidentCreated, Payload: json.RawMessage(`{"title":"created"}`), ID: "e1"})
	assert.Equal(t, "created", detail)
	detail = incidentEventDetail(incidents.Event{Type: incidents.EventIncidentMerged, Payload: json.RawMessage(`{"into":{"storeId":"ops","incidentId":"INC-2"}}`), ID: "e1"})
	assert.Contains(t, detail, "INC-2")
	detail = incidentEventDetail(incidents.Event{Type: "unknown", ID: "fallback"})
	assert.Equal(t, "fallback", detail)

	cmd := &cobra.Command{}
	cmd.Flags().String(incidentFormatFlag, "", "")
	cmd.Flags().Bool(executionJSONFlag, false, "")
	_ = cmd.Flags().Set(incidentFormatFlag, "yaml")
	format, err := incidentOutputFormat(cmd)
	require.NoError(t, err)
	assert.Equal(t, "yaml", format)

	_ = cmd.Flags().Set(executionJSONFlag, "true")
	_, err = incidentOutputFormat(cmd)
	require.Error(t, err) // json conflicts with yaml

	cmd2 := &cobra.Command{}
	cmd2.Flags().String(incidentFormatFlag, "", "")
	cmd2.Flags().Bool(executionJSONFlag, false, "")
	_ = cmd2.Flags().Set(executionJSONFlag, "true")
	format, err = incidentOutputFormat(cmd2)
	require.NoError(t, err)
	assert.Equal(t, "json", format)

	cmd3 := &cobra.Command{}
	cmd3.Flags().String(incidentFormatFlag, "", "")
	cmd3.Flags().Bool(executionJSONFlag, false, "")
	_ = cmd3.Flags().Set(incidentFormatFlag, "bad")
	_, err = incidentOutputFormat(cmd3)
	require.Error(t, err)

	raw := []byte(`{"a":1}`)
	out := &bytes.Buffer{}
	cmdJSON := &cobra.Command{}
	cmdJSON.SetOut(out)
	cmdJSON.Flags().String(incidentFormatFlag, "", "")
	cmdJSON.Flags().Bool(executionJSONFlag, false, "")
	_ = cmdJSON.Flags().Set(incidentFormatFlag, "json")
	require.NoError(t, writeIncidentOutput(cmdJSON, raw, nil, nil))
	assert.Equal(t, string(raw), out.String())

	out.Reset()
	_ = cmdJSON.Flags().Set(incidentFormatFlag, "yaml")
	require.NoError(t, writeIncidentOutput(cmdJSON, raw, nil, nil))
	assert.Contains(t, out.String(), "a:")

	out.Reset()
	_ = cmdJSON.Flags().Set(incidentFormatFlag, "grid")
	require.NoError(t, writeIncidentOutput(cmdJSON, raw, nil, func(w io.Writer) error {
		_, err := w.Write([]byte("grid"))
		return err
	}))
}

func TestCov100gSqlQueryArgsAndSavedQueryHelpers(t *testing.T) {
	q := &datatug.QueryDef{
		Parameters: datatug.Parameters{
			{ID: "a", IsRequired: true},
			{ID: "b", IsRequired: false},
		},
	}
	_, err := sqlQueryArgs(q, nil)
	require.Error(t, err)
	args, err := sqlQueryArgs(q, map[string]any{"a": 1, "b": 2})
	require.NoError(t, err)
	require.Len(t, args, 2)

	var buf bytes.Buffer
	writeSavedQueryLimitations(&buf, []secureread.Limitation{
		{Kind: secureread.LimitationPolicy, Note: "policy note"},
		{Kind: secureread.LimitationNativeSQL, Note: "sql note"},
		{Kind: secureread.LimitationRowsFiltered},
		{Kind: secureread.LimitationHiddenColumns, Columns: []string{"x", "y"}},
	})
	s := buf.String()
	assert.Contains(t, s, "policy note")
	assert.Contains(t, s, "rows filtered")
	assert.Contains(t, s, "hidden columns: x, y")

	err = savedQueryFailure(secureread.ErrAccessDenied)
	var exit ExitCoder
	require.True(t, errors.As(err, &exit))
	assert.Equal(t, exitCodeAccessDenied, exit.ExitCode())
	err = savedQueryFailure(secureread.ErrNoPrincipal)
	require.True(t, errors.As(err, &exit))
	assert.Equal(t, exitCodeUsage, exit.ExitCode())
	err = savedQueryFailure(errors.New("db boom"))
	require.True(t, errors.As(err, &exit))
	assert.Equal(t, exitCodeDatabase, exit.ExitCode())
}

func TestCov100gChatSavedQueryRunAndLookup(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(projectDir, "queries"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "datatug-project.json"), []byte(`{"id":"saved-run"}`), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(projectDir, "environments", "local"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "environments", "local", "local.env.json"),
		[]byte(`{"id":"local","dbServers":[{"driver":"sqlite3","catalogs":["mini"]}]}`), 0o600))

	dbPath := filepath.Join(projectDir, "db.sqlite")
	writeMiniCustomerDB(t, dbPath)
	catalogDir := filepath.Join(projectDir, "environments", "local", "catalogs", "mini")
	require.NoError(t, os.MkdirAll(catalogDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(catalogDir, "mini.db.json"),
		[]byte(fmt.Sprintf(`{"id":"mini","driver":"sqlite3","path":%q}`, dbPath)), 0o600))

	store, id := filestore.NewSingleProjectStore(projectDir, "saved-run")
	proj := store.GetProjectStore(id)
	writer := proj.(datatug.RevisionedQueriesStore)

	dtqlText := "from: {name: Customer}\nlimit: 10\n"
	_, err := writer.PutQuery(ctx, &datatug.QueryDefWithFolderPath{QueryDef: datatug.QueryDef{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "customers", Title: "Customers"}},
		Type:        datatug.QueryTypeDTQL, Text: dtqlText,
		Targets: []datatug.QueryDefTarget{{Catalog: "mini"}},
		Parameters: datatug.Parameters{{
			ID: "CustomerId", Type: "string", IsRequired: true,
			Meta: &datatug.EntityFieldRef{Entity: "Customer", Field: "CustomerId"},
		}},
	}}, datatug.QueryWriteCondition{IfNoneMatch: true})
	require.NoError(t, err)

	_, err = writer.PutQuery(ctx, &datatug.QueryDefWithFolderPath{QueryDef: datatug.QueryDef{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "sql-q", Title: "SQL"}},
		Type:        datatug.QueryTypeSQL, Text: "SELECT 1 AS n",
		Targets: []datatug.QueryDefTarget{{Catalog: "mini"}},
	}}, datatug.QueryWriteCondition{IfNoneMatch: true})
	require.NoError(t, err)

	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"ok": true}})
	}))
	t.Cleanup(httpSrv.Close)
	_, err = writer.PutQuery(ctx, &datatug.QueryDefWithFolderPath{QueryDef: datatug.QueryDef{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "http-q", Title: "HTTP"}},
		Type:        datatug.QueryTypeHTTP, Text: httpSrv.URL,
	}}, datatug.QueryWriteCondition{IfNoneMatch: true})
	require.NoError(t, err)

	executor := secureread.NewExecutor(secureread.Session{Unrestricted: true})
	svc := chatSavedQueries{projectDir: projectDir, store: proj, executor: executor, env: "local", projectID: id, session: secureread.Session{Unrestricted: true}}

	result, err := svc.Run(ctx, "customers")
	require.NoError(t, err)
	assert.NotEmpty(t, result.Result.Rows)

	_, err = svc.RunWithVariables(ctx, "customers", map[string]string{"CustomerId": "1"})
	require.NoError(t, err)

	_, err = svc.RunDTQLWithVariables(ctx, "customers", map[string]string{"CustomerId": "1"})
	require.NoError(t, err)

	_, err = svc.RunDTQLWithVariables(ctx, "sql-q", nil)
	require.Error(t, err) // wrong type

	// The real HTTP source needs a keyField config this fixture does not
	// have, so the structured HTTP runner is replaced with one that records
	// its inputs; the saved-query plumbing around it is what is under test.
	var httpProject string
	covDSetVar(t, &runStructuredHTTPQuery, func(_ context.Context, _ *secureread.Executor, projectDir string, _ dal.Query, _ map[string]any) (secureread.Result, error) {
		httpProject = projectDir
		return secureread.Result{Columns: []string{"ok"}, Rows: []secureread.Row{{Data: map[string]any{"ok": true}}}}, nil
	})
	httpResult, err := svc.RunHTTPWithVariables(ctx, "http-q", nil)
	require.NoError(t, err)
	assert.Equal(t, "http://"+projectDir, httpProject)
	require.Len(t, httpResult.Result.Rows, 1)
	assert.Equal(t, true, httpResult.Result.Rows[0].Data["ok"])

	_, err = svc.RunWithVariables(ctx, "customers", map[string]string{"=bad": "x"})
	require.Error(t, err)

	lookup, err := svc.LookupParameter(ctx, "customers", "CustomerId")
	require.NoError(t, err)
	assert.Nil(t, lookup, "the fixture database has no foreign keys, so no lookup is discovered")

	lookup, err = svc.LookupParameter(ctx, "sql-q", "CustomerId")
	require.NoError(t, err)
	assert.Nil(t, lookup) // non-DTQL

	// encodeParameterDefault nil + marshal edge
	s, err := encodeParameterDefault(nil)
	require.NoError(t, err)
	assert.Equal(t, "", s)

	// savedQueryID empty slug
	id2 := savedQueryID("!!!")
	assert.True(t, strings.HasPrefix(id2, "query-"))
}

func writeMiniCustomerDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`CREATE TABLE Customer (CustomerId INTEGER PRIMARY KEY, FirstName TEXT);
INSERT INTO Customer VALUES (1, 'Ada');`)
	require.NoError(t, err)
}

func TestCov100gRunSQLAndHTTPSavedQueryDirect(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	dbPath := filepath.Join(projectDir, "db.sqlite")
	writeMiniCustomerDB(t, dbPath)
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "datatug-project.json"), []byte(`{"id":"direct"}`), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(projectDir, "environments", "local", "catalogs", "mini"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "environments", "local", "local.env.json"),
		[]byte(`{"id":"local","dbServers":[{"driver":"sqlite3","catalogs":["mini"]}]}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "environments", "local", "catalogs", "mini", "mini.db.json"),
		[]byte(fmt.Sprintf(`{"id":"mini","driver":"sqlite3","path":%q}`, dbPath)), 0o600))

	store, id := filestore.NewSingleProjectStore(projectDir, "direct")
	proj := store.GetProjectStore(id)
	executor := secureread.NewExecutor(secureread.Session{Unrestricted: true})

	q := &datatug.QueryDef{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "s1"}},
		Type:        datatug.QueryTypeSQL, Text: "SELECT CustomerId FROM Customer",
		Targets: []datatug.QueryDefTarget{{Catalog: "mini"}},
	}
	url, err := resolveSQLOrDTQLSourceURL(ctx, proj, projectDir, "local", q)
	require.NoError(t, err)
	assert.Contains(t, url, "sqlite://")

	res, err := runSQLSavedQuery(ctx, executor, proj, projectDir, "local", q, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, res.Rows)

	qHTTP := &datatug.QueryDef{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "h1"}},
		Type:        datatug.QueryTypeHTTP, Text: "https://example.test/x",
		Parameters: datatug.Parameters{{ID: "name", IsRequired: true}},
	}
	_, err = runHTTPSavedQuery(ctx, executor, projectDir, qHTTP, nil)
	require.Error(t, err) // missing required var

	orig := runStructuredHTTPQuery
	t.Cleanup(func() { runStructuredHTTPQuery = orig })
	runStructuredHTTPQuery = func(context.Context, *secureread.Executor, string, dal.Query, map[string]any) (secureread.Result, error) {
		return secureread.Result{Columns: []string{"ok"}, Rows: []secureread.Row{{Data: map[string]any{"ok": true}}}}, nil
	}
	res, err = runHTTPSavedQuery(ctx, executor, projectDir, qHTTP, map[string]any{"name": "x"})
	require.NoError(t, err)
	assert.Len(t, res.Rows, 1)
}

func TestCov100gResolveQueryEnvironmentDatabase(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "datatug-project.json"), []byte(`{"id":"env"}`), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "environments", "a"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "environments", "a", "a.env.json"), []byte(`{"id":"a"}`), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "environments", "b"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "environments", "b", "b.env.json"), []byte(`{"id":"b"}`), 0o600))
	store, id := filestore.NewSingleProjectStore(dir, "env")
	proj := store.GetProjectStore(id)

	env, err := resolveQueryEnvironment(ctx, proj, "explicit")
	require.NoError(t, err)
	assert.Equal(t, "explicit", env)

	_, err = resolveQueryEnvironment(ctx, proj, "")
	require.Error(t, err) // ambiguous

	empty := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(empty, "datatug-project.json"), []byte(`{"id":"e"}`), 0o600))
	estore, eid := filestore.NewSingleProjectStore(empty, "e")
	_, err = resolveQueryEnvironment(ctx, estore.GetProjectStore(eid), "")
	require.Error(t, err)
}
