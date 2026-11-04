package endpoints

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/meaninglabelsfile"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/meaninglabels"
	"github.com/datatug/datatug-core/pkg/semantic"
)

// agentQueries writes a saved SQL query and two saved TugQL queries (a wildcard
// one and an explicit-column one) into the project.
func agentQueries(t *testing.T, dir string) {
	t.Helper()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, "queries", name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "queries"), 0o755); err != nil {
		t.Fatal(err)
	}
	write("agent-sql.query.json", `{"createdAt":"2026-01-01T00:00:00Z","id":"agent-sql","type":"SQL"}`)
	write("agent-sql.query.sql", "SELECT 1;")
	write("agent-wild.query.json", `{"createdAt":"2026-01-01T00:00:00Z","id":"agent-wild","type":"TugQL"}`)
	write("agent-wild.query.tugql.yaml", "from: {name: Cards}\n")
	write("agent-cols.query.json", `{"createdAt":"2026-01-01T00:00:00Z","id":"agent-cols","type":"TugQL"}`)
	write("agent-cols.query.tugql.yaml", "from: {name: Cards}\ncolumns: [{field: Id}, {field: Note}]\n")
}

func agentRunRequest(project, queryID, tugql string) apicontract.ExecutionRequest {
	return apicontract.ExecutionRequest{
		Project: project, Environment: "local", Source: "chinook", SecurityContextID: api.SecurityContextID(),
		QueryID: queryID, TugQL: tugql, Mode: apicontract.ProvenanceModeLive,
		Parameters: map[string]apicontract.TypedValueOrSet{}, BindingOrigins: []apicontract.BindingOriginEntry{},
	}
}

func errCode(err error) string {
	var ce *contractError
	if errors.As(err, &ce) {
		return string(ce.Code)
	}
	return ""
}

func TestRunQueryAgentGuards(t *testing.T) {
	project, dir := labelsFixture(t)
	agentQueries(t, dir)
	agentCtx := withAgentContext(context.Background(), true)

	// an agent's saved SQL that names no protected column runs (TestAgentSavedSQL covers refusals)
	_, err := computeRunQuery(agentCtx, agentRunRequest(project, "agent-sql", ""))
	if !errors.Is(err, secureread.ErrOpaqueSQLNotGranted) && errCode(err) != "UNSUPPORTED_PROTECTED_EXECUTION" {
		t.Fatalf("saved SQL must pass the guard and reach the opaque-SQL grant: %v", err)
	}
	// ad-hoc TugQL: a wildcard is refused, an explicit non-masked list runs
	_, err = computeRunQuery(agentCtx, agentRunRequest(project, "", "from: {name: Cards}\n"))
	if errCode(err) != meaninglabels.CodeFieldProtected {
		t.Fatalf("ad-hoc wildcard: %v", err)
	}
	res, err := computeRunQuery(agentCtx, agentRunRequest(project, "", "from: {name: Cards}\ncolumns: [{field: Id}, {field: Note}]\n"))
	if err != nil || len(res.Recordset.Rows) != 2 {
		t.Fatalf("ad-hoc explicit: %v %+v", err, res.Recordset)
	}
	// saved TugQL: the same two outcomes
	_, err = computeRunQuery(agentCtx, agentRunRequest(project, "agent-wild", ""))
	if errCode(err) != meaninglabels.CodeFieldProtected {
		t.Fatalf("saved wildcard: %v", err)
	}
	if _, err = computeRunQuery(agentCtx, agentRunRequest(project, "agent-cols", "")); err != nil {
		t.Fatalf("saved explicit: %v", err)
	}
	// a saved TugQL whose document cannot be loaded fails before the guard
	if _, err = computeRunQuery(agentCtx, agentRunRequest(project, "agent-missing", "")); err == nil {
		t.Fatal("missing query")
	}
	// a person is not guarded
	if _, err = computeRunQuery(context.Background(), agentRunRequest(project, "agent-wild", "")); err != nil {
		t.Fatalf("person wildcard: %v", err)
	}

	// through the handler the header marks the agent
	body, _ := json.Marshal(agentRunRequest(project, "", "from: {name: Cards}\n"))
	for agent, want := range map[bool]int{true: http.StatusForbidden, false: http.StatusOK} {
		r := httptest.NewRequest(http.MethodPost, "/datatug/exec/run_query", bytes.NewReader(body))
		if agent {
			r.Header.Set(agentHeader, "1")
		}
		w := httptest.NewRecorder()
		runQueryHandler(w, r)
		if w.Code != want {
			t.Fatalf("handler agent=%v: %d %s", agent, w.Code, w.Body.String())
		}
	}
}

// --- failure branches of meanings_labels.go ---

func setHooks(t *testing.T) {
	t.Helper()
	l, rs, st, ex, ar, ao, ds, ob, sb := listSourceObjectsHook, resolveSourceTableHook, relatedProjectStoreFor, secureExecutorHook, apiResolveSource, labelsStoreFor, dalAsSchemaReader, openBoundSchemaDB, relatedProjectDir
	t.Cleanup(func() {
		listSourceObjectsHook, resolveSourceTableHook, relatedProjectStoreFor, secureExecutorHook = l, rs, st, ex
		apiResolveSource, labelsStoreFor, dalAsSchemaReader, openBoundSchemaDB, relatedProjectDir = ar, ao, ds, ob, sb
	})
}

func TestOpenMeaningSourceFailures(t *testing.T) {
	project, _ := labelsFixture(t)
	setHooks(t)
	ctx := context.Background()
	req := meaningSourceRequest{Project: project, Env: "local", Source: "chinook"}

	if _, err := computeDescribe(ctx, meaningSourceRequest{Project: "a/b", Env: "local", Source: "chinook"}); err == nil {
		t.Fatal("bad name")
	}
	store := relatedProjectStoreFor
	relatedProjectStoreFor = func(string) (datatug.ProjectStore, error) { return nil, errors.New("no store") }
	if _, err := openMeaningSource(ctx, req); err == nil {
		t.Fatal("store")
	}
	relatedProjectStoreFor = store
	apiResolveSource = func(context.Context, datatug.ProjectStore, string, string, string) (api.ResolvedSource, error) {
		return api.ResolvedSource{}, errors.New("no source")
	}
	if _, err := openMeaningSource(ctx, req); err == nil {
		t.Fatal("source")
	}
	apiResolveSource = api.ResolveSource
	secureExecutorHook = func() (*secureread.Executor, bool) { return nil, false }
	if _, err := openMeaningSource(ctx, req); err == nil {
		t.Fatal("executor")
	}
}

type fakeEntitiesStore struct {
	datatug.ProjectStore
	entities datatug.Entities
	err      error
}

func (s fakeEntitiesStore) LoadEntities(context.Context, ...datatug.StoreOption) (datatug.Entities, error) {
	return s.entities, s.err
}

func TestDescribeVariants(t *testing.T) {
	project, _ := labelsFixture(t)
	setHooks(t)
	ctx := context.Background()
	req := meaningSourceRequest{Project: project, Env: "local", Source: "chinook"}
	realStore := relatedProjectStoreFor
	withEntities := func(entities datatug.Entities, err error) {
		relatedProjectStoreFor = func(id string) (datatug.ProjectStore, error) {
			s, e := realStore(id)
			return fakeEntitiesStore{ProjectStore: s, entities: entities, err: err}, e
		}
	}

	// entities that cannot be loaded do not fail the answer
	withEntities(nil, errors.New("entities down"))
	if resp, err := computeDescribe(ctx, req); err != nil || len(resp.Tables) == 0 {
		t.Fatalf("entities error: %v", err)
	}
	// nil entities are skipped; a table the entity names carries the meaning
	cards := datatug.TableKeys{datatug.NewTableKey("Cards", "", "", nil)}
	entity := &datatug.Entity{Tables: cards}
	entity.ID, entity.Title = "Card", "Card"
	other := &datatug.Entity{Tables: datatug.TableKeys{datatug.NewTableKey("Genre", "", "", nil)}}
	other.ID = "G"
	withEntities(datatug.Entities{nil, other, entity}, nil)
	resp, err := computeDescribe(ctx, meaningSourceRequest{Project: project, Env: "local", Source: "chinook", Tables: []string{"Cards"}})
	if err != nil || len(resp.Tables) != 1 || len(resp.Tables[0].Meanings) != 1 || resp.Tables[0].Meanings[0].Entity != "Card" {
		t.Fatalf("meanings: %v %+v", err, resp.Tables)
	}
	// find matches on the table's meaning words
	found, err := computeFind(ctx, findRequest{Project: project, Env: "local", Source: "chinook", Text: "card"})
	if err != nil || len(found.Matches) == 0 {
		t.Fatalf("find: %v", err)
	}
	relatedProjectStoreFor = realStore

	// a table that cannot be described is skipped
	resolveSourceTableHook = func(context.Context, datatug.ProjectStore, string, string, string, string) (resolvedSource, error) {
		return resolvedSource{}, errors.New("cannot describe")
	}
	listSourceObjectsHook = func(context.Context, string) ([]sourceObject, error) {
		return []sourceObject{{Name: "T1"}}, nil
	}
	if resp, err := computeDescribe(ctx, req); err != nil || len(resp.Tables) != 0 {
		t.Fatalf("skip: %v %+v", err, resp)
	}
	if _, err := (&meaningSource{}).describeTable(ctx, sourceObject{Name: "T1", View: true}, nil); err == nil {
		t.Fatal("a view with no columns fails")
	}

	// the object cap stops the list
	var many []sourceObject
	for i := 0; i < maxMeaningObjects+5; i++ {
		many = append(many, sourceObject{Name: fmt.Sprintf("T%d", i)})
	}
	listSourceObjectsHook = func(context.Context, string) ([]sourceObject, error) { return many, nil }
	m, err := openMeaningSource(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := m.objects(ctx, nil)
	if err != nil || len(objects) != maxMeaningObjects {
		t.Fatalf("cap: %v %d", err, len(objects))
	}
	// an unknown table counts as a view
	listSourceObjectsHook = func(context.Context, string) ([]sourceObject, error) { return nil, nil }
	if !m.isView(ctx, "Cards") {
		t.Fatal("unlisted table is a view")
	}
	listSourceObjectsHook = func(context.Context, string) ([]sourceObject, error) { return nil, errors.New("down") }
	if !m.isView(ctx, "Cards") {
		t.Fatal("unreadable list is a view")
	}
}

func TestObjectsSkipsUnauthorised(t *testing.T) {
	project, dir := labelsFixture(t)
	configureSemanticSession(t, dir, project, "bob", []string{"support"})
	resp, err := computeDescribe(context.Background(), meaningSourceRequest{Project: project, Env: "local", Source: "chinook"})
	if err != nil || len(resp.Tables) != 1 || resp.Tables[0].Name != "Customer" {
		t.Fatalf("a table the principal may not read is left out: %v %+v", err, resp.Tables)
	}
}

func TestFindStopsAtMaximum(t *testing.T) {
	project, _ := labelsFixture(t)
	setHooks(t)
	var objects []sourceObject
	for i := 0; i < 40; i++ {
		objects = append(objects, sourceObject{Name: fmt.Sprintf("T%d", i)})
	}
	listSourceObjectsHook = func(context.Context, string) ([]sourceObject, error) { return objects, nil }
	resolveSourceTableHook = func(context.Context, datatug.ProjectStore, string, string, string, string) (resolvedSource, error) {
		return resolvedSource{Columns: []semantic.Column{{Name: "tc1"}, {Name: "tc2"}, {Name: "tc3"}}}, nil
	}
	got, err := computeFind(context.Background(), findRequest{Project: project, Env: "local", Source: "chinook", Text: "t"})
	if err != nil || len(got.Matches) != maxFindMatches {
		t.Fatalf("cap: %v %d", err, len(got.Matches))
	}
	if _, err := computeFind(context.Background(), findRequest{Project: project, Env: "local", Source: "chinook", Text: strings.Repeat("x", 201)}); err == nil {
		t.Fatal("long text")
	}
}

func TestHandlersRejectBadBodies(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"describe": meaningsDescribeHandler, "find": meaningsFindHandler, "labels": meaningsLabelsHandler,
		"change": meaningsLabelsChangeHandler, "read": recordsReadHandler,
	} {
		if code, _ := post(t, h, `{"bogus":1}`, false); code != http.StatusBadRequest {
			t.Fatalf("%s: %d", name, code)
		}
	}
	// a body over the limit cannot be read
	big := `{"project":"` + strings.Repeat("x", recordsLookupMaxBody+10) + `"}`
	if code, _ := post(t, meaningsDescribeHandler, big, false); code != http.StatusBadRequest {
		t.Fatalf("big: %d", code)
	}
}

func TestLabelsHandlerSecondOpenFails(t *testing.T) {
	project, _ := labelsFixture(t)
	setHooks(t)
	real := relatedProjectStoreFor
	calls := 0
	relatedProjectStoreFor = func(id string) (datatug.ProjectStore, error) {
		calls++
		if calls > 1 {
			return nil, errors.New("gone")
		}
		return real(id)
	}
	if code, _ := post(t, meaningsLabelsHandler, `{`+src(project)+`}`, false); code != http.StatusBadRequest {
		t.Fatalf("labels: %d", code)
	}
	// the same for the raw refusal, which opens the source twice
	calls = 0
	if err := agentRawRefusal(context.Background(), project, "local", "chinook"); err == nil {
		t.Fatal("second open must refuse")
	}
	// and for the TugQL guard's own open
	relatedProjectStoreFor = func(string) (datatug.ProjectStore, error) { return nil, errors.New("gone") }
	if err := agentTugQLGuard(context.Background(), project, "local", "chinook", []byte("from: {name: Cards}\n")); err == nil {
		t.Fatal("open must refuse")
	}
}

func TestLabelChangeFailures(t *testing.T) {
	project, dir := labelsFixture(t)
	setHooks(t)
	ctx := context.Background()
	loosen := labelChangeRequest{Project: project, Env: "local", Source: "chinook", Change: meaninglabels.Change{
		Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: "chinook", Table: "Customer", Column: "Email"}, Class: meaninglabels.ClassNone, Reason: "r"}}

	// the source cannot be listed for the exact-target check
	listSourceObjectsHook = func(context.Context, string) ([]sourceObject, error) { return nil, errors.New("down") }
	if _, err := computeLabelChange(ctx, loosen, false); err == nil {
		t.Fatal("list failure")
	}
	// a matched table that cannot be resolved
	listSourceObjectsHook = listSourceObjects
	resolveSourceTableHook = func(context.Context, datatug.ProjectStore, string, string, string, string) (resolvedSource, error) {
		return resolvedSource{}, errors.New("cannot resolve")
	}
	if _, err := computeLabelChange(ctx, loosen, false); err == nil {
		t.Fatal("resolve failure")
	}
	// a matched table with no columns reports an empty list
	resolveSourceTableHook = func(context.Context, datatug.ProjectStore, string, string, string, string) (resolvedSource, error) {
		return resolvedSource{}, nil
	}
	if _, err := computeLabelChange(ctx, loosen, false); err == nil {
		t.Fatal("no columns: the target does not exist")
	}
	resolveSourceTableHook = resolveSource
	// an audit that fails does not fail the saved change
	labelsStoreFor = func(string) labelStore { return failingAudit{meaninglabelsfile.Store{Dir: dir}} }
	propose := labelChangeRequest{Project: project, Env: "local", Source: "chinook", Change: meaninglabels.Change{
		Action: meaninglabels.ActionPropose, Target: meaninglabels.Target{Source: "chinook", Table: "Cards", Column: "Note"}, Class: meaninglabels.ClassPII}}
	if _, err := computeLabelChange(ctx, propose, true); err != nil {
		t.Fatalf("audit failure must be logged only: %v", err)
	}
}

func TestListSourceObjectsFailures(t *testing.T) {
	setHooks(t)
	root, _ := boundEndpointFixture(t)
	ctx := context.Background()
	url := dbcopy.LocalSourceURL("sqlite", filepath.Join(root, "chinook.sqlite"))

	// bound: the real open works
	if objects, err := listSourceObjects(ctx, url); err != nil || len(objects) == 0 {
		t.Fatalf("bound list: %v", err)
	}
	// bound: the open fails
	openBoundSchemaDB = func(context.Context, dbcopy.BackendRef) (dal.DB, error) { return nil, errors.New("open") }
	if _, err := listSourceObjects(ctx, url); err == nil {
		t.Fatal("bound open")
	}
	// the source does not support introspection
	openBoundSchemaDB = func(ctx context.Context, ref dbcopy.BackendRef) (dal.DB, error) {
		return ref.OpenProtected(ctx)
	}
	dalAsSchemaReader = func(dal.DB) (dbschema.SchemaReader, bool) { return nil, false }
	if _, err := listSourceObjects(ctx, url); err == nil {
		t.Fatal("no reader")
	}
	// the collections cannot be listed
	dalAsSchemaReader = func(dal.DB) (dbschema.SchemaReader, bool) { return failingReader{}, true }
	if _, err := listSourceObjects(ctx, url); err == nil {
		t.Fatal("list collections")
	}
	// the views cannot be listed
	openBoundSchemaDB = func(context.Context, dbcopy.BackendRef) (dal.DB, error) { return viewFailingDB{}, nil }
	dalAsSchemaReader = func(dal.DB) (dbschema.SchemaReader, bool) { return emptyReader{}, true }
	if _, err := listSourceObjects(ctx, url); err == nil {
		t.Fatal("list views")
	}
	// a view the collections do not list is still reported
	openBoundSchemaDB = func(context.Context, dbcopy.BackendRef) (dal.DB, error) { return viewsOnlyDB{}, nil }
	objects, err := listSourceObjects(ctx, url)
	if err != nil || len(objects) != 1 || !objects[0].View || objects[0].Name != "v" {
		t.Fatalf("views only: %v %+v", err, objects)
	}
	// the binding of the source changed: no facts
	if err := os.WriteFile(filepath.Join(root, "policies", "after.yaml"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := listSourceObjects(ctx, url); err == nil {
		t.Fatal("binding change")
	}
}

type failingReader struct{ dbschema.SchemaReader }

func (failingReader) ListCollections(context.Context, *record.Key) ([]dal.CollectionRef, error) {
	return nil, errors.New("collections")
}

type emptyReader struct{ dbschema.SchemaReader }

func (emptyReader) ListCollections(context.Context, *record.Key) ([]dal.CollectionRef, error) {
	return nil, nil
}

type viewFailingDB struct{ dal.DB }

func (viewFailingDB) ListSourceViews(context.Context) ([]dbschema.SourceViewDef, error) {
	return nil, errors.New("views")
}

type viewsOnlyDB struct{ dal.DB }

func (viewsOnlyDB) ListSourceViews(context.Context) ([]dbschema.SourceViewDef, error) {
	return []dbschema.SourceViewDef{{Name: "v", Columns: []string{"a"}}}, nil
}

func TestAgentGuardEdges(t *testing.T) {
	project, _ := labelsFixture(t)
	setHooks(t)
	ctx := context.Background()
	guard := func(doc string) error { return agentTugQLGuard(ctx, project, "local", "chinook", []byte(doc)) }

	// an aggregate expression on a table with masked columns cannot be inspected
	if err := guard("from: {name: Cards}\ncolumns: [{aggregate: {function: count, args: [{star: true}]}}]\n"); errCode(err) != meaninglabels.CodeFieldProtected {
		t.Fatalf("aggregate: %v", err)
	}
	// a source whose every column is unlabelled passes the raw refusal
	listSourceObjectsHook = func(context.Context, string) ([]sourceObject, error) { return []sourceObject{{Name: "Plain"}}, nil }
	resolveSourceTableHook = func(context.Context, datatug.ProjectStore, string, string, string, string) (resolvedSource, error) {
		return resolvedSource{Columns: []semantic.Column{{Name: "x"}}}, nil
	}
	if err := agentRawRefusal(ctx, project, "local", "chinook"); err != nil {
		t.Fatalf("unlabelled source: %v", err)
	}

	// baseCollectionOf: a collection by pointer, and a source that is no collection
	ref := dal.NewRootCollectionRef("Cards", "")
	q := dal.NewQueryBuilder(dal.From(&ref)).SelectColumns(dal.Column{Expression: dal.Field("Id")})
	if got, ok := baseCollectionOf(q); !ok || got.Name() != "Cards" {
		t.Fatalf("pointer base: %v %v", got, ok)
	}
	q = dal.NewQueryBuilder(dal.From(notACollection{})).SelectColumns(dal.Column{Expression: dal.Field("Id")})
	if _, ok := baseCollectionOf(q); ok {
		t.Fatal("not a collection")
	}
}

type notACollection struct{ dal.RecordsetSource }

func TestRecordsReadRevealAll(t *testing.T) {
	project, _ := labelsFixture(t)
	code, a, raw := doRead(t, `{`+src(project)+`,"table":"Cards","reveal":["*"]}`, true)
	if code != http.StatusForbidden || a.Error.Code != meaninglabels.CodeRevealNotForAgent {
		t.Fatalf("agent reveal-all: %d %s", code, raw)
	}
	code, _, raw = doRead(t, `{`+src(project)+`,"table":"Cards","reveal":["*"]}`, false)
	if code != http.StatusOK || !strings.Contains(raw, "4111111111111111") {
		t.Fatalf("person reveal-all: %d %s", code, raw)
	}
}
