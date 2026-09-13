package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/incidentstore"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/server/endpoints"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/investigation"
	"github.com/julienschmidt/httprouter"
	"github.com/sneat-co/sneat-go-core/apicore"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
	"github.com/stretchr/testify/require"
)

func TestIncidentCommandUsesSingularNamespaceAndBareHelp(t *testing.T) {
	command := incidentCommand()
	require.Equal(t, "incident", command.Name())
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs(nil)
	require.NoError(t, command.ExecuteContext(context.Background()))
	require.Contains(t, output.String(), "Available Commands")
	for _, verb := range []string{"list", "create", "show", "append", "events", "search", "similar", "merge", "watch"} {
		found, _, err := command.Find([]string{verb})
		require.NoError(t, err)
		require.Equal(t, verb, found.Name())
	}
}

func TestIncidentWatchUsesOneServerStreamAndSeparatesCursor(t *testing.T) {
	item := incidents.StreamItem{Cursor: "next-cursor", Event: incidents.Event{
		ID: "note-1", Seq: 2, At: time.Date(2026, 9, 13, 10, 43, 2, 0, time.UTC), VisibleAt: time.Date(2026, 9, 13, 10, 43, 2, 0, time.UTC),
		Incident: incidents.IncidentRef{StoreID: "ops", IncidentID: "INC-1"}, Actor: incidents.Actor{Kind: incidents.ActorHuman, ID: "alice", Via: incidents.ActorViaAPI},
		Type: incidents.EventNoteAdded, Assertion: incidents.Assertion{Kind: incidents.AssertionClaim}, Payload: json.RawMessage(`{"body":"database recovered"}`),
	}}
	rawItem, err := json.Marshal(item)
	require.NoError(t, err)
	var streamRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/datatug/agent-info":
			_ = json.NewEncoder(w).Encode(apicontract.AgentInfo{Version: "test", Principal: apicontract.AgentPrincipal{ID: "alice"}, SecurityContextID: "ctx", Projects: []apicontract.AgentProjectRef{{ID: "demo"}}})
		case "/datatug/incidents/INC-1/events":
			streamRequests.Add(1)
			require.Equal(t, "old-cursor", r.URL.Query().Get("since"))
			w.Header().Set("Content-Type", "application/x-ndjson")
			_, _ = w.Write(append(rawItem, '\n'))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	var stdout, stderr bytes.Buffer
	command := incidentCommand()
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"--agent", server.URL, "--project", "demo", "--environment", "prod", "--store", "ops", "watch", "INC-1", "--json", "--since", "old-cursor"})
	require.NoError(t, command.ExecuteContext(context.Background()))
	require.Equal(t, string(rawItem)+"\n", stdout.String())
	require.Equal(t, "next-cursor\n", stderr.String())
	stdout.Reset()
	stderr.Reset()
	command = incidentCommand()
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"--agent", server.URL, "--project", "demo", "--environment", "prod", "watch", "ops/INC-1", "--json", "--since", "old-cursor"})
	require.NoError(t, command.ExecuteContext(context.Background()))
	require.Equal(t, string(rawItem)+"\n", stdout.String())
	require.Equal(t, "next-cursor\n", stderr.String())
	require.Equal(t, int32(2), streamRequests.Load())
}

func TestIncidentWatchHumanOutputIsOneLinePerEvent(t *testing.T) {
	item := incidents.StreamItem{Cursor: "cursor-human", Event: incidents.Event{
		ID: "note-1", Seq: 2, At: time.Date(2026, 9, 13, 10, 43, 2, 0, time.UTC), VisibleAt: time.Date(2026, 9, 13, 10, 43, 2, 0, time.UTC),
		Incident: incidents.IncidentRef{StoreID: "ops", IncidentID: "INC-1"}, Actor: incidents.Actor{Kind: incidents.ActorHuman, ID: "alice", Via: incidents.ActorViaAPI},
		Type: incidents.EventNoteAdded, Assertion: incidents.Assertion{Kind: incidents.AssertionClaim}, Payload: json.RawMessage(`{"body":"database recovered"}`),
	}}
	server := incidentStreamTestServer(t, item)
	var stdout, stderr bytes.Buffer
	command := incidentCommand()
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"--agent", server.URL, "--project", "demo", "--environment", "prod", "--store", "ops", "watch", "INC-1"})
	require.NoError(t, command.ExecuteContext(context.Background()))
	require.Equal(t, "10:43:02  NOTE.ADDED  database recovered\n", stdout.String())
	require.Equal(t, "cursor-human\n", stderr.String())
}

func TestIncidentWatchHumanOutputFailureDoesNotAdvanceCursor(t *testing.T) {
	item := incidents.StreamItem{Cursor: "must-not-print", Event: incidents.Event{
		ID: "note-write-failure", Seq: 2, At: time.Date(2026, 9, 13, 10, 43, 2, 0, time.UTC), VisibleAt: time.Date(2026, 9, 13, 10, 43, 2, 0, time.UTC),
		Incident: incidents.IncidentRef{StoreID: "ops", IncidentID: "INC-1"}, Actor: incidents.Actor{Kind: incidents.ActorHuman, ID: "alice", Via: incidents.ActorViaAPI},
		Type: incidents.EventNoteAdded, Assertion: incidents.Assertion{Kind: incidents.AssertionClaim}, Payload: json.RawMessage(`{"body":"cannot print"}`),
	}}
	server := incidentStreamTestServer(t, item)
	writeErr := errors.New("stdout is full")
	var stderr bytes.Buffer
	command := incidentCommand()
	command.SetOut(failingIncidentWriter{err: writeErr})
	command.SetErr(&stderr)
	command.SetArgs([]string{"--agent", server.URL, "--project", "demo", "--environment", "prod", "--store", "ops", "watch", "INC-1"})
	err := command.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), writeErr.Error())
	var exit ExitCoder
	require.ErrorAs(t, err, &exit)
	require.Equal(t, 1, exit.ExitCode())
	require.NotContains(t, stderr.String(), string(item.Cursor))
}

type failingIncidentWriter struct{ err error }

func (w failingIncidentWriter) Write([]byte) (int, error) { return 0, w.err }

func TestIncidentShowGridOutputFailureIsReturned(t *testing.T) {
	server := realIncidentCommandServer(t)
	runIncidentCommand(t, server.URL, "create", "--title", "Output failure", "--mutation", "output-failure-create")
	writeErr := errors.New("stdout is full")
	var stderr bytes.Buffer
	command := incidentCommand()
	command.SetOut(failingIncidentWriter{err: writeErr})
	command.SetErr(&stderr)
	command.SetArgs([]string{"--agent", server.URL, "--project", "demo", "--environment", "prod", "--store", "demo", "--format", "grid", "show", "INC-1"})
	err := command.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), writeErr.Error())
}

func TestIncidentEventsInvalidCursorExitsNonzero(t *testing.T) {
	server := realIncidentCommandServer(t)
	runIncidentCommand(t, server.URL, "create", "--title", "Cursor", "--mutation", "cursor-create")
	var stdout, stderr bytes.Buffer
	command := incidentCommand()
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"--agent", server.URL, "--project", "demo", "--environment", "prod", "--store", "demo", "events", "INC-1", "--since", "%%%"})
	err := command.ExecuteContext(context.Background())
	require.Error(t, err)
	var exit ExitCoder
	require.ErrorAs(t, err, &exit)
	require.Equal(t, 1, exit.ExitCode())
	require.Contains(t, err.Error(), "INVALID_REQUEST")
	require.Contains(t, err.Error(), "invalid event cursor")
	require.Empty(t, stdout.String())
	require.NotContains(t, stderr.String(), "%%%")
}

func TestIncidentWatchAbortedStreamExitsNonzeroWithoutSuccessCursor(t *testing.T) {
	item := incidents.StreamItem{Cursor: "must-not-print", Event: incidents.Event{
		ID: "note-abort", Seq: 2, At: time.Date(2026, 9, 13, 10, 43, 2, 0, time.UTC), VisibleAt: time.Date(2026, 9, 13, 10, 43, 2, 0, time.UTC),
		Incident: incidents.IncidentRef{StoreID: "ops", IncidentID: "INC-1"}, Actor: incidents.Actor{Kind: incidents.ActorHuman, ID: "alice", Via: incidents.ActorViaAPI},
		Type: incidents.EventNoteAdded, Assertion: incidents.Assertion{Kind: incidents.AssertionClaim}, Payload: json.RawMessage(`{"body":"before abort"}`),
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/datatug/agent-info" {
			_ = json.NewEncoder(w).Encode(apicontract.AgentInfo{Version: "test", Principal: apicontract.AgentPrincipal{ID: "alice"}, SecurityContextID: "ctx", Projects: []apicontract.AgentProjectRef{{ID: "demo"}}})
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		require.NoError(t, json.NewEncoder(w).Encode(item))
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}))
	t.Cleanup(server.Close)

	var stdout, stderr bytes.Buffer
	command := incidentCommand()
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"--agent", server.URL, "--project", "demo", "--environment", "prod", "--store", "ops", "watch", "INC-1", "--json"})
	err := command.ExecuteContext(context.Background())
	require.Error(t, err)
	var exit ExitCoder
	require.ErrorAs(t, err, &exit)
	require.Equal(t, 1, exit.ExitCode())
	require.NotContains(t, stderr.String(), "must-not-print")
	require.Equal(t, mustJSONLine(t, item), stdout.String())
}

func TestIncidentQualifiedIDSelectsRoutingStoreUnlessStoreWasExplicit(t *testing.T) {
	server := realIncidentCommandServerWithStores(t, []incidentstore.ConfiguredStore{{StoreID: "ops", Kind: incidents.StoreLocationDedicatedRepository, Repository: t.TempDir()}})
	store, err := api.IncidentStoreByID("demo", "ops")
	require.NoError(t, err)
	scope := incidents.ProjectRef{StoreID: api.LocalStoreID, ProjectID: "demo", Environment: "prod"}
	created, err := store.Create(context.Background(), incidents.CreateMutation{
		MutationID: "qualified-create", StoreID: "ops", UID: "qualified-uid", Title: "Qualified", At: time.Now().UTC(),
		Reporter: incidents.Actor{Kind: incidents.ActorHuman, ID: "alice", Via: incidents.ActorViaAPI}, PrimaryProject: scope,
		CanonicalContext: incidents.CanonicalContext{Facts: []investigation.Fact{}},
	})
	require.NoError(t, err)

	var stdout, stderr bytes.Buffer
	command := incidentCommand()
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"--agent", server.URL, "--project", "demo", "--environment", "prod", "--json", "show", "ops/INC-1"})
	require.NoError(t, command.ExecuteContext(context.Background()), stderr.String())
	var response apicontract.IncidentResponse
	require.NoError(t, apicontract.DecodeStrict(stdout.Bytes(), &response))
	require.Equal(t, created.Projection.Ref, response.Incident.Ref)

	command = incidentCommand()
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"--agent", server.URL, "--project", "demo", "--environment", "prod", "--store", "demo", "show", "ops/INC-1"})
	err = command.ExecuteContext(context.Background())
	require.Error(t, err)
	var exit ExitCoder
	require.ErrorAs(t, err, &exit)
	require.Equal(t, exitCodeUsage, exit.ExitCode())

	cli := incidentCLI{scope: apicontract.IncidentScope{StoreID: "demo"}}
	_, err = cli.resolveRef("ops/INC-1")
	require.NoError(t, err)
	_, err = cli.resolveRef("other/INC-2")
	require.Error(t, err, "merge refs must not select two routing stores")
}

func TestIncidentCommandsExerciseRealServerTask3Contracts(t *testing.T) {
	server := realIncidentCommandServer(t)

	firstRaw, _ := runIncidentCommand(t, server.URL, "create", "--title", "Database checkout failure", "--description", "orders are stuck", "--mutation", "create-one")
	var first apicontract.IncidentResponse
	require.NoError(t, apicontract.DecodeStrict(firstRaw, &first))
	require.NoError(t, first.Validate())
	require.Equal(t, "INC-1", first.Incident.Ref.IncidentID)
	cutoff := time.Now().UTC()

	secondRaw, _ := runIncidentCommand(t, server.URL, "create", "--title", "Database checkout regression", "--mutation", "create-two")
	var second apicontract.IncidentResponse
	require.NoError(t, apicontract.DecodeStrict(secondRaw, &second))
	require.Equal(t, "INC-2", second.Incident.Ref.IncidentID)

	appendRaw, _ := runIncidentCommand(t, server.URL, "append", "INC-1", "--type", string(incidents.EventNoteAdded), "--payload", `{"body":"database recovered"}`, "--check-ref", "check-orders", "--mutation", "note-one")
	var appended apicontract.IncidentAppendResponse
	require.NoError(t, apicontract.DecodeStrict(appendRaw, &appended))
	require.NoError(t, appended.Validate())
	require.Equal(t, "alice", appended.Event.Actor.ID)
	qualifiedAppendRaw, _ := runIncidentCommand(t, server.URL, "append", "demo/INC-1", "--type", string(incidents.EventNoteAdded), "--payload", `{"body":"qualified route accepted"}`, "--mutation", "note-qualified")
	var qualifiedAppend apicontract.IncidentAppendResponse
	require.NoError(t, apicontract.DecodeStrict(qualifiedAppendRaw, &qualifiedAppend))
	require.False(t, qualifiedAppend.Replayed)
	require.Equal(t, first.Incident.Ref, qualifiedAppend.Event.Incident)

	showRaw, _ := runIncidentCommand(t, server.URL, "show", "INC-1")
	var shown apicontract.IncidentResponse
	require.NoError(t, apicontract.DecodeStrict(showRaw, &shown))
	require.Equal(t, []string{"database recovered", "qualified route accepted"}, shown.Incident.Notes)
	qualifiedShowRaw, _ := runIncidentCommand(t, server.URL, "show", "demo/INC-1")
	var qualifiedShown apicontract.IncidentResponse
	require.NoError(t, apicontract.DecodeStrict(qualifiedShowRaw, &qualifiedShown))
	require.Equal(t, shown.Incident, qualifiedShown.Incident)
	historicalRaw, _ := runIncidentCommand(t, server.URL, "show", "INC-1", "--at", cutoff.Format(time.RFC3339Nano))
	var historical apicontract.IncidentResponse
	require.NoError(t, apicontract.DecodeStrict(historicalRaw, &historical))
	require.Empty(t, historical.Incident.Notes)

	listRaw, _ := runIncidentCommand(t, server.URL, "list", "--check", "check-orders")
	var listed apicontract.IncidentListResponse
	require.NoError(t, apicontract.DecodeStrict(listRaw, &listed))
	require.Len(t, listed.Incidents, 1)
	require.Equal(t, first.Incident.Ref, listed.Incidents[0].Ref)

	eventsRaw, eventsErr := runIncidentCommand(t, server.URL, "events", "INC-1")
	decoder := json.NewDecoder(bytes.NewReader(eventsRaw))
	var createdItem, noteItem, qualifiedNoteItem incidents.StreamItem
	require.NoError(t, decoder.Decode(&createdItem))
	require.NoError(t, decoder.Decode(&noteItem))
	require.NoError(t, decoder.Decode(&qualifiedNoteItem))
	require.Equal(t, "create-one", createdItem.Event.ID)
	require.Equal(t, "note-one", noteItem.Event.ID)
	require.Equal(t, "note-qualified", qualifiedNoteItem.Event.ID)
	require.Equal(t, string(qualifiedNoteItem.Cursor)+"\n", string(eventsErr))
	qualifiedEventsRaw, _ := runIncidentCommand(t, server.URL, "events", "demo/INC-1", "--since", string(qualifiedNoteItem.Cursor))
	require.Empty(t, qualifiedEventsRaw)

	searchRaw, _ := runIncidentCommand(t, server.URL, "search", "recovered")
	var searched apicontract.IncidentSearchResponse
	require.NoError(t, apicontract.DecodeStrict(searchRaw, &searched))
	require.Len(t, searched.Matches, 1)
	require.Equal(t, first.Incident.Ref, searched.Matches[0].Incident.Ref)

	similarRaw, _ := runIncidentCommand(t, server.URL, "similar", "INC-2")
	var similar apicontract.IncidentSimilarResponse
	require.NoError(t, apicontract.DecodeStrict(similarRaw, &similar))
	require.NotEmpty(t, similar.Matches)
	require.Equal(t, first.Incident.Ref, similar.Matches[0].Incident.Ref)
	qualifiedSimilarRaw, _ := runIncidentCommand(t, server.URL, "similar", "demo/INC-2")
	var qualifiedSimilar apicontract.IncidentSimilarResponse
	require.NoError(t, apicontract.DecodeStrict(qualifiedSimilarRaw, &qualifiedSimilar))
	require.Equal(t, similar, qualifiedSimilar)

	mergeRaw, _ := runIncidentCommand(t, server.URL, "merge", "INC-2", "--into", "INC-1", "--mutation", "merge-two")
	var merged apicontract.IncidentMergeResponse
	require.NoError(t, apicontract.DecodeStrict(mergeRaw, &merged))
	require.NoError(t, merged.Validate())
	require.Equal(t, first.Incident.Ref, merged.Into.Ref)
	qualifiedMergeRaw, _ := runIncidentCommand(t, server.URL, "merge", "demo/INC-2", "--into", "demo/INC-1", "--mutation", "merge-two")
	var qualifiedMerged apicontract.IncidentMergeResponse
	require.NoError(t, apicontract.DecodeStrict(qualifiedMergeRaw, &qualifiedMerged))
	require.True(t, qualifiedMerged.Replayed)
	require.Equal(t, merged.Source, qualifiedMerged.Source)
	require.Equal(t, merged.Into, qualifiedMerged.Into)
}

func incidentStreamTestServer(t *testing.T, items ...incidents.StreamItem) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/datatug/agent-info" {
			_ = json.NewEncoder(w).Encode(apicontract.AgentInfo{Version: "test", Principal: apicontract.AgentPrincipal{ID: "alice"}, SecurityContextID: "ctx", Projects: []apicontract.AgentProjectRef{{ID: "demo"}}})
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/events") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		for _, item := range items {
			require.NoError(t, json.NewEncoder(w).Encode(item))
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func realIncidentCommandServer(t *testing.T) *httptest.Server {
	return realIncidentCommandServerWithStores(t, nil)
}

func realIncidentCommandServerWithStores(t *testing.T, stores []incidentstore.ConfiguredStore) *httptest.Server {
	t.Helper()
	const projectID = "demo"
	projectDir := t.TempDir()
	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	api.ConfigureSecureSession(session, map[string]string{projectID: projectDir}, api.Capabilities{AllowWrites: true})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	require.NoError(t, api.ConfigureExecutionEvidence(map[string]string{projectID: projectDir}, stores, executionstore.Options{PrivateDir: t.TempDir()}))
	t.Cleanup(func() { require.NoError(t, api.CloseExecutionEvidence()) })
	router := httprouter.New()
	endpoints.RegisterDatatugHandlersWithCapabilities("", router, endpoints.RegisterAllHandlers, nil, func(r *http.Request) (context.Context, error) {
		return r.Context(), nil
	}, func(http.ResponseWriter, *http.Request, apicore.RequestDTO, verify.RequestOptions, int, apicore.ContextProvider, apicore.Worker) {
		panic("legacy handler unexpectedly invoked")
	}, endpoints.Capabilities{AllowWrites: true})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server
}

func runIncidentCommand(t *testing.T, agent string, args ...string) ([]byte, []byte) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	command := incidentCommand()
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	base := []string{"--agent", agent, "--project", "demo", "--environment", "prod", "--store", "demo", "--json"}
	command.SetArgs(append(base, args...))
	require.NoError(t, command.ExecuteContext(context.Background()), stderr.String())
	return stdout.Bytes(), stderr.Bytes()
}

func mustJSONLine(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return string(raw) + "\n"
}
