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
	"time"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/incidentstore"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	coredetective "github.com/datatug/datatug-core/pkg/detective"
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/investigation"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockUnrevisionedDatatugStore struct {
	storage.Store
	projStore datatug.ProjectStore
}

func (m *mockUnrevisionedDatatugStore) GetProjectStore(string) datatug.ProjectStore {
	return &mockUnrevisionedProjectStore{ProjectStore: m.projStore}
}

type mockUnrevisionedProjectStore struct {
	datatug.ProjectStore
}

// Not parallel: tests sessionQueryResolveSessionContext error branch
func TestCoverage100_SessionQueryResolveSessionContextError(t *testing.T) {
	_, _, _, err := sessionQueryResolveSessionContext(context.Background(), "unknown_storage", "p")
	assert.Error(t, err)
}

// Not parallel: tests sessionContextQueryHandler when ResolvedParameters already contains binding parameter
func TestCoverage100_SessionContextQueryDuplicateResolvedParameter(t *testing.T) {
	projectDir := t.TempDir()
	queriesDir := filepath.Join(projectDir, "queries")
	require.NoError(t, os.MkdirAll(queriesDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(queriesDir, "q1.query.json"), []byte(`{"id":"q1","type":"SQL","parameters":[{"id":"p1","type":"string","isRequired":true}]}`), 0600))
	paths := map[string]string{"p1": projectDir}
	fStore, err := filestore.NewStore("files", paths)
	require.NoError(t, err)
	projStore := fStore.GetProjectStore("p1")
	mockRevStore := &mockSessionQueryRevisionedStore{ProjectStore: projStore}
	origProjectStoreFor := sessionQueryProjectStoreFor
	sessionQueryProjectStoreFor = func(projectID string) (datatug.ProjectStore, error) {
		return mockRevStore, nil
	}
	defer func() { sessionQueryProjectStoreFor = origProjectStoreFor }()

	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: true})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})

	origResolve := sessionQueryResolveSessionContext
	mockSvc := &mockSessionContextService{}
	mockSvc.getSnapshotFn = func(ctx context.Context, p sessioncontext.Principal, id string) (sessioncontext.Snapshot, error) {
		return sessioncontext.Snapshot{
			Session: sessioncontext.Session{ID: "s1", Version: 1},
		}, nil
	}
	mockSvc.readEntryRevFn = func(ctx context.Context, p sessioncontext.Principal, sessionID, entryID, revisionID string) (sessioncontext.EntryRevision, error) {
		scalarVal := apicontract.NewStringValue("val1")
		return sessioncontext.EntryRevision{
			Value: sessioncontext.ValueEnvelope{Kind: "scalar", Scalar: &scalarVal},
		}, nil
	}
	sessionQueryResolveSessionContext = func(ctx context.Context, storage, project string) (sessionContextService, sessioncontext.Principal, api.SessionContextCapabilities, error) {
		return mockSvc, sessioncontext.Principal{DatabaseID: "db1", ProjectID: "p1"}, api.SessionContextCapabilities{
			Environments: []api.SessionContextEnvironment{{EnvironmentID: "uat", IdentityDomain: "dom1"}},
		}, nil
	}
	defer func() { sessionQueryResolveSessionContext = origResolve }()

	req := sessionQueryRequest{
		Command: sessioncontext.Command{
			Action:          "pinInput",
			Kind:            "query",
			SessionID:       "s1",
			ExpectedVersion: 1,
			RunID:           "run1",
			Scope:           &sessioncontext.Scope{EnvironmentID: "uat", IdentityDomain: "dom1"},
			Attachments:     []sessioncontext.EntryRef{{EntryID: "e1", RevisionID: "r1"}},
		},
		QueryID: "q1",
		Bindings: []sessionQueryBinding{
			{EntryID: "e1", ParameterID: "p1"},
			{EntryID: "e1", ParameterID: "p1"},
		},
	}
	body, _ := json.Marshal(req)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/datatug/session-context/query?storage=local&project=p1&databaseId=db1&projectId=p1", bytes.NewReader(body))
	sessionContextQueryHandler(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// Not parallel: tests detective_ai_snapshot error branches
func TestCoverage100_DetectiveAISnapshotBranches(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	privateDir := t.TempDir()
	envDir := filepath.Join(projectDir, "environments", "qa")
	require.NoError(t, os.MkdirAll(envDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(envDir, "qa.env.json"), []byte(`{"id":"qa"}`), 0600))
	paths := map[string]string{"p": projectDir}
	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: privateDir})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})

	origDatatugStore := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
	defer func() { storage.NewDatatugStore = origDatatugStore }()

	store, err := detective.NewLocalStore("p", projectDir, privateDir)
	require.NoError(t, err)

	// 1. store.State error -> line 26
	checksDir := filepath.Join(projectDir, "checks")
	require.NoError(t, os.MkdirAll(checksDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(checksDir, "bad.json"), []byte(`{invalid`), 0600))
	_, err = detectiveAISnapshot(ctx, "p", store)
	assert.Error(t, err)
	_ = os.Remove(filepath.Join(checksDir, "bad.json"))

	// 2. loadModuleQueries error -> line 42
	queriesDir := filepath.Join(projectDir, "queries")
	require.NoError(t, os.MkdirAll(queriesDir, 0700))
	badQueryFile := filepath.Join(queriesDir, "bad.query.json")
	require.NoError(t, os.WriteFile(badQueryFile, []byte(`{bad`), 0600))
	_, err = detectiveAISnapshotFromState(ctx, "p", detective.State{})
	assert.Error(t, err)
	_ = os.Remove(badQueryFile)

	// 3. projectStore not RevisionedQueriesStore -> line 53
	fStore, _ := filestore.NewStore("files", paths)
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return &mockUnrevisionedDatatugStore{projStore: fStore.GetProjectStore("p")}, nil
	}
	_, err = detectiveAISnapshotFromState(ctx, "p", detective.State{})
	assert.ErrorIs(t, err, detective.ErrInvalid)

	storage.NewDatatugStore = origDatatugStore
}

// Not parallel: tests detective_ai.go remaining branches
func TestCoverage100_DetectiveAIBranches(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	privateDir := t.TempDir()
	envDir := filepath.Join(projectDir, "environments", "qa")
	require.NoError(t, os.MkdirAll(envDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(envDir, "qa.env.json"), []byte(`{"id":"qa"}`), 0600))
	paths := map[string]string{"p": projectDir}

	origStore := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
	defer func() { storage.NewDatatugStore = origStore }()

	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	prof := &dtconfig.AIProfile{Model: "gpt-4"}
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: privateDir, DetectiveAIProfile: prof})
	localStore, err := detective.NewLocalStore("p", projectDir, privateDir)
	require.NoError(t, err)

	// 1. local.SavePaste error -> lines 124-127
	matches, _ := filepath.Glob(filepath.Join(privateDir, "detective", "*"))
	require.NotEmpty(t, matches)
	aiSourcesDir := filepath.Join(matches[0], "ai-sources")
	require.NoError(t, os.WriteFile(aiSourcesDir, []byte("file-not-dir"), 0600))
	reqSavePasteErr := aiGenerateRequest{
		Project:           "p",
		SecurityContextID: api.SecurityContextID(),
		OperationID:       "op-save-paste-fail",
		Text:              "valid text",
	}
	bSavePasteErr, _ := json.Marshal(reqSavePasteErr)
	wSavePasteErr := httptest.NewRecorder()
	rSavePasteErr := httptest.NewRequest(http.MethodPost, "/datatug/detective/ai_proposals?project=p", bytes.NewReader(bSavePasteErr))
	detectiveAIProposalsHandler(wSavePasteErr, rSavePasteErr)
	assert.Equal(t, http.StatusBadRequest, wSavePasteErr.Code)
	_ = os.Remove(aiSourcesDir)

	// 2. detectiveAISnapshot error -> line 124
	queriesDir := filepath.Join(projectDir, "queries")
	require.NoError(t, os.MkdirAll(queriesDir, 0700))
	badQ := filepath.Join(queriesDir, "bad.query.json")
	require.NoError(t, os.WriteFile(badQ, []byte(`{bad`), 0600))
	reqSnapErr := aiGenerateRequest{
		Project:           "p",
		SecurityContextID: api.SecurityContextID(),
		OperationID:       "op-snap",
		Text:              "hello",
	}
	bSnap, _ := json.Marshal(reqSnapErr)
	wSnap := httptest.NewRecorder()
	rSnap := httptest.NewRequest(http.MethodPost, "/datatug/detective/ai_proposals?project=p", bytes.NewReader(bSnap))
	detectiveAIProposalsHandler(wSnap, rSnap)
	assert.Equal(t, http.StatusInternalServerError, wSnap.Code)
	_ = os.Remove(badQ)

	// 3. localAIPorts methods: verifyBasis, Current
	scope := localAIScope("p", "alice")

	// verifyBasis invalid securityContext -> line 166
	badPorts := newLocalAIPorts(localStore, scope, "p", "bad_sec")
	assert.ErrorIs(t, badPorts.verifyBasis(ctx, ai.Snapshot{}, detective.State{}), ai.ErrUnauthorized)

	// verifyBasis with anonymous actor ("") -> lines 162-163
	api.ConfigureSecureSession(secureread.Session{Unrestricted: true}, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: privateDir})
	anonScope := localAIScope("p", "local-owner")
	anonPorts := newLocalAIPorts(localStore, anonScope, "p", api.SecurityContextID())
	assert.ErrorIs(t, anonPorts.verifyBasis(ctx, ai.Snapshot{}, detective.State{}), ai.ErrStale)

	// verifyBasis with snapshot error -> line 170
	require.NoError(t, os.WriteFile(badQ, []byte(`{bad`), 0600))
	assert.Error(t, anonPorts.verifyBasis(ctx, ai.Snapshot{}, detective.State{}))
	_ = os.Remove(badQ)

	// Current with actor mismatch -> line 196
	portsWrongActor := newLocalAIPorts(localStore, localAIScope("p", "wrong-actor"), "p", api.SecurityContextID())
	_, err = portsWrongActor.Current(ctx, localAIScope("p", "wrong-actor"))
	assert.ErrorIs(t, err, ai.ErrUnauthorized)
}

// Not parallel: tests detective_incidents.go remaining branches
func TestCoverage100_DetectiveIncidentsBranches(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	privateDir := t.TempDir()
	dedicated := t.TempDir()
	envDir := filepath.Join(projectDir, "environments", "qa")
	require.NoError(t, os.MkdirAll(envDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(envDir, "qa.env.json"), []byte(`{"id":"qa"}`), 0600))
	paths := map[string]string{"p": projectDir}

	origStore := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
	defer func() {
		storage.NewDatatugStore = origStore
		_ = api.CloseExecutionEvidence()
		api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	}()

	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: privateDir})
	require.NoError(t, api.ConfigureExecutionEvidence(paths, []incidentstore.ConfiguredStore{{StoreID: "ops", Kind: incidents.StoreLocationDedicatedRepository, Repository: dedicated}}, executionstore.Options{PrivateDir: privateDir}))

	opsStore, err := api.IncidentStoreByID("p", "ops")
	require.NoError(t, err)
	scope := incidents.ProjectRef{StoreID: "local", ProjectID: "p", Environment: "qa"}

	// 1. detectiveIncidentView unconfigured secure session -> line 90
	rReq := httptest.NewRequest(http.MethodGet, "/test", nil)
	item := incidents.Incident{Ref: incidents.IncidentRef{StoreID: "ops", IncidentID: "inc1"}, Projects: []incidents.ProjectRef{scope}}
	api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	_, err = detectiveIncidentView(rReq, opsStore, item, scope)
	assert.Error(t, err)
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: privateDir})

	// Create incident with non-matching project -> lines 134, 145
	otherProjMut, err := opsStore.Create(ctx, incidents.CreateMutation{
		MutationID: "mut-other-proj", StoreID: "ops", UID: "inc-other", Title: "Other", Description: "Desc", At: time.Now(),
		Reporter:       incidents.Actor{Kind: incidents.ActorHuman, ID: "alice", Via: incidents.ActorViaAPI},
		PrimaryProject: incidents.ProjectRef{StoreID: "local", ProjectID: "other_project", Environment: "qa"},
	})
	require.NoError(t, err)

	// Create incident with unadmitted environment -> lines 138, 145
	unadmittedMut, err := opsStore.Create(ctx, incidents.CreateMutation{
		MutationID: "mut-unadmitted", StoreID: "ops", UID: "inc-unadmitted", Title: "Unadmitted", Description: "Desc", At: time.Now(),
		Reporter:       incidents.Actor{Kind: incidents.ActorHuman, ID: "alice", Via: incidents.ActorViaAPI},
		PrimaryProject: incidents.ProjectRef{StoreID: "local", ProjectID: "p", Environment: "unadmitted_env"},
	})
	require.NoError(t, err)

	localStore, _, err := detectiveStore("p", api.SecurityContextID())
	require.NoError(t, err)

	putCaseWithRefs := func(id, title string, refs []string) {
		_, err := localStore.SaveCase(ctx, detective.Case{ID: id, Title: title}, "alice", "create-case")
		require.NoError(t, err)
		matches, _ := filepath.Glob(filepath.Join(privateDir, "detective", "*", "cases", id+".json"))
		require.NotEmpty(t, matches)
		data, err := os.ReadFile(matches[0])
		require.NoError(t, err)
		var cObj map[string]any
		require.NoError(t, json.Unmarshal(data, &cObj))
		cObj["incidentRefs"] = refs
		newData, err := json.Marshal(cObj)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(matches[0], newData, 0600))
	}

	// Case referencing other project -> hits line 134 and 145
	putCaseWithRefs("case-other-proj", "OtherProj", []string{"ops/" + otherProjMut.Projection.Ref.IncidentID})
	wOther := httptest.NewRecorder()
	rOther := httptest.NewRequest(http.MethodGet, "/datatug/detective/incidents?project=p&caseId=case-other-proj&securityContextId="+api.SecurityContextID(), nil)
	detectiveIncidentsHandler(wOther, rOther)
	assert.Equal(t, http.StatusForbidden, wOther.Code)

	// Case referencing unadmitted env -> hits line 138 and 145
	putCaseWithRefs("case-unadmitted", "Unadmitted", []string{"ops/" + unadmittedMut.Projection.Ref.IncidentID})
	wUnadmitted := httptest.NewRecorder()
	rUnadmitted := httptest.NewRequest(http.MethodGet, "/datatug/detective/incidents?project=p&caseId=case-unadmitted&securityContextId="+api.SecurityContextID(), nil)
	detectiveIncidentsHandler(wUnadmitted, rUnadmitted)
	assert.Equal(t, http.StatusForbidden, wUnadmitted.Code)

	// 2. detectiveIncidentActionHandler: store.Projection error in non-create -> lines 205-207
	actProjErr := detectiveIncidentAction{
		OperationID:       "op-proj-err",
		Type:              "link",
		CaseID:            "case-other-proj",
		Project:           "p",
		SecurityContextID: api.SecurityContextID(),
		Environment:       "qa",
		IncidentRef:       "ops/INC-NONEXISTENT",
	}
	bProjErr, _ := json.Marshal(actProjErr)
	wProjErr := httptest.NewRecorder()
	rProjErr := httptest.NewRequest(http.MethodPost, "/datatug/detective/incident-action", bytes.NewReader(bProjErr))
	detectiveIncidentActionHandler(wProjErr, rProjErr)
	assert.Equal(t, http.StatusNotFound, wProjErr.Code)

	// 3. detectiveIncidentActionHandler: store.Create error (empty Title) -> lines 269-271
	actCreateErr := detectiveIncidentAction{
		OperationID:       "op-empty-title",
		Type:              "create",
		CaseID:            "case-other-proj",
		Title:             "",
		Project:           "p",
		SecurityContextID: api.SecurityContextID(),
		Environment:       "qa",
	}
	bCreateErr, _ := json.Marshal(actCreateErr)
	wCreateErr := httptest.NewRecorder()
	rCreateErr := httptest.NewRequest(http.MethodPost, "/datatug/detective/incident-action", bytes.NewReader(bCreateErr))
	detectiveIncidentActionHandler(wCreateErr, rCreateErr)
	assert.Equal(t, http.StatusBadRequest, wCreateErr.Code)

	// 4. detectiveIncidentActionHandler: incidentActor.Validate error (whitespace actor) -> lines 259-261
	whiteActorSession, err := secureread.NewSession(secureread.SessionOptions{As: "   ", NoPolicies: true})
	require.NoError(t, err)
	whiteActorSession.Unrestricted = true
	whiteActorSession.Principal = &access.Principal{ID: "   "}
	api.ConfigureSecureSession(whiteActorSession, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: privateDir})
	actWhiteActor := detectiveIncidentAction{
		OperationID:       "op-white-actor",
		Type:              "create",
		CaseID:            "case-other-proj",
		Title:             "Valid Title",
		Project:           "p",
		SecurityContextID: api.SecurityContextID(),
		Environment:       "qa",
		IfMatch:           1,
	}
	bWhite, _ := json.Marshal(actWhiteActor)
	wWhite := httptest.NewRecorder()
	rWhite := httptest.NewRequest(http.MethodPost, "/datatug/detective/incident-action", bytes.NewReader(bWhite))
	detectiveIncidentActionHandler(wWhite, rWhite)
	assert.Equal(t, http.StatusForbidden, wWhite.Code)
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: privateDir})

	// 5. detectiveIncidentActionHandler: CompleteIncidentOperation error on archived case -> line 303
	_, err = localStore.SaveCase(ctx, detective.Case{ID: "archived-case", Title: "Archived"}, "alice", "create-case")
	require.NoError(t, err)
	matches, _ := filepath.Glob(filepath.Join(privateDir, "detective", "*", "cases", "archived-case.json"))
	require.NotEmpty(t, matches)
	dataArch, err := os.ReadFile(matches[0])
	require.NoError(t, err)
	var cObjArch map[string]any
	require.NoError(t, json.Unmarshal(dataArch, &cObjArch))
	cObjArch["status"] = "archived"
	newArchData, err := json.Marshal(cObjArch)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(matches[0], newArchData, 0600))

	actArch := detectiveIncidentAction{
		OperationID:       "op-arch",
		Type:              "create",
		CaseID:            "archived-case",
		Title:             "Title 1",
		Project:           "p",
		SecurityContextID: api.SecurityContextID(),
		Environment:       "qa",
		IfMatch:           1,
	}
	bArch, _ := json.Marshal(actArch)
	wArch := httptest.NewRecorder()
	rArch := httptest.NewRequest(http.MethodPost, "/datatug/detective/incident-action", bytes.NewReader(bArch))
	detectiveIncidentActionHandler(wArch, rArch)
	assert.Equal(t, http.StatusOK, wArch.Code)
	var respArch detectiveIncidentResponse
	require.NoError(t, json.Unmarshal(wArch.Body.Bytes(), &respArch))
	assert.Equal(t, "link-pending", respArch.CaseLinkStatus)
	assert.Equal(t, "case-archived", respArch.LinkPendingReason)

	// 6. detectiveIncidentActionHandler: CompleteIncidentOperation error on link with conflict -> lines 309-310
	validMut, err := opsStore.Create(ctx, incidents.CreateMutation{
		MutationID: "mut-valid", StoreID: "ops", UID: "inc-valid", Title: "Valid", Description: "Desc", At: time.Now(),
		Reporter:       incidents.Actor{Kind: incidents.ActorHuman, ID: "alice", Via: incidents.ActorViaAPI},
		PrimaryProject: scope,
	})
	require.NoError(t, err)
	actLinkConflict := detectiveIncidentAction{
		OperationID:       "op-link-conflict",
		Type:              "link",
		CaseID:            "case-other-proj",
		IncidentRef:       "ops/" + validMut.Projection.Ref.IncidentID,
		Project:           "p",
		SecurityContextID: api.SecurityContextID(),
		Environment:       "qa",
		IfMatch:           9999, // Force conflict
	}
	bConflict, _ := json.Marshal(actLinkConflict)
	wConflict := httptest.NewRecorder()
	rConflict := httptest.NewRequest(http.MethodPost, "/datatug/detective/incident-action", bytes.NewReader(bConflict))
	detectiveIncidentActionHandler(wConflict, rConflict)
	assert.Equal(t, http.StatusConflict, wConflict.Code)

	// 7. detectiveIncidentActionHandler: replay authorization fails -> lines 253-254
	actLinkSuccess := detectiveIncidentAction{
		OperationID:       "op-link-success",
		Type:              "link",
		CaseID:            "case-other-proj",
		IncidentRef:       "ops/" + validMut.Projection.Ref.IncidentID,
		Project:           "p",
		SecurityContextID: api.SecurityContextID(),
		Environment:       "qa",
		IfMatch:           1,
	}
	bSucc, _ := json.Marshal(actLinkSuccess)
	wSucc := httptest.NewRecorder()
	rSucc := httptest.NewRequest(http.MethodPost, "/datatug/detective/incident-action", bytes.NewReader(bSucc))
	detectiveIncidentActionHandler(wSucc, rSucc)
	assert.Equal(t, http.StatusOK, wSucc.Code)

	// Alter op-link-success.json Case.IncidentRefs to a different store to trigger replay authorization failure
	opMatches, _ := filepath.Glob(filepath.Join(privateDir, "detective", "*", "incident-operations", "op-link-success.json"))
	require.NotEmpty(t, opMatches)
	opData, err := os.ReadFile(opMatches[0])
	require.NoError(t, err)
	var opObj map[string]any
	require.NoError(t, json.Unmarshal(opData, &opObj))
	caseObj := opObj["case"].(map[string]any)
	caseObj["incidentRefs"] = []any{"other-store/INC-1"}
	newOpData, err := json.Marshal(opObj)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(opMatches[0], newOpData, 0600))

	wReplayBad := httptest.NewRecorder()
	rReplayBad := httptest.NewRequest(http.MethodPost, "/datatug/detective/incident-action", bytes.NewReader(bSucc))
	detectiveIncidentActionHandler(wReplayBad, rReplayBad)
	assert.Equal(t, http.StatusForbidden, wReplayBad.Code)

	// detectiveIncidentActionHandler create with invalid context facts (exceeds capacity) -> line 254
	valCap := apicontract.NewIntegerValue("1")
	ctxCapMap := make(map[string]detective.InputValue, 201)
	for i := 0; i < 201; i++ {
		ctxCapMap[fmt.Sprintf("k%03d", i)] = detective.InputValue{Scalar: &valCap}
	}
	savedCapCase, err := localStore.SaveCase(ctx, detective.Case{ID: "case-capacity", Title: "Cap Case", Context: ctxCapMap}, "alice", "create-case")
	require.NoError(t, err)

	actBadFacts := detectiveIncidentAction{
		OperationID:       "op-bad-facts",
		Type:              "create",
		CaseID:            "case-capacity",
		Title:             "Valid Title",
		Project:           "p",
		SecurityContextID: api.SecurityContextID(),
		Environment:       "qa",
		IfMatch:           savedCapCase.Version,
	}
	bBadFacts, _ := json.Marshal(actBadFacts)
	wBadFacts := httptest.NewRecorder()
	detectiveIncidentActionHandler(wBadFacts, httptest.NewRequest(http.MethodPost, "/datatug/detective/incident-action", bytes.NewReader(bBadFacts)))
	assert.Equal(t, http.StatusBadRequest, wBadFacts.Code)

	// detectiveIncidentActionHandler create with store.Create conflict -> line 259
	savedValidCase, err := localStore.SaveCase(ctx, detective.Case{ID: "case-create-conflict", Title: "Valid Case"}, "alice", "create-case")
	require.NoError(t, err)
	mergeIntentDir := filepath.Join(projectDir, "incidents", ".store", "merge-intents")
	require.NoError(t, os.MkdirAll(mergeIntentDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(mergeIntentDir, "op-create-conflict.json"), []byte(`{}`), 0600))
	actCreateConflict := detectiveIncidentAction{
		OperationID:       "op-create-conflict",
		Type:              "create",
		CaseID:            "case-create-conflict",
		Title:             "Valid Title",
		Project:           "p",
		SecurityContextID: api.SecurityContextID(),
		Environment:       "qa",
		IfMatch:           savedValidCase.Version,
	}
	bCreateConflict, _ := json.Marshal(actCreateConflict)
	wCreateConflict := httptest.NewRecorder()
	detectiveIncidentActionHandler(wCreateConflict, httptest.NewRequest(http.MethodPost, "/datatug/detective/incident-action", bytes.NewReader(bCreateConflict)))
	assert.Equal(t, http.StatusConflict, wCreateConflict.Code)

	// detectiveIncidentActionHandler link to archived case fails in CompleteIncidentOperation -> line 292
	actLinkArchived := detectiveIncidentAction{
		OperationID:       "op-link-archived",
		Type:              "link",
		CaseID:            "archived-case",
		IncidentRef:       "ops/" + validMut.Projection.Ref.IncidentID,
		Project:           "p",
		SecurityContextID: api.SecurityContextID(),
		Environment:       "qa",
		IfMatch:           1,
	}
	bLinkArchived, _ := json.Marshal(actLinkArchived)
	wLinkArchived := httptest.NewRecorder()
	detectiveIncidentActionHandler(wLinkArchived, httptest.NewRequest(http.MethodPost, "/datatug/detective/incident-action", bytes.NewReader(bLinkArchived)))
	assert.Equal(t, http.StatusBadRequest, wLinkArchived.Code)

	// detectiveIncidentActionHandler start-case with invalid incident context -> line 271
	scopeFact := investigation.ProjectScope(scope)
	badIncMut, err := opsStore.Create(ctx, incidents.CreateMutation{
		MutationID: "mut-bad-facts", StoreID: "ops", UID: "inc-bad-facts", Title: "Bad Facts Inc", Description: "Desc", At: time.Now(),
		Reporter:       incidents.Actor{Kind: incidents.ActorHuman, ID: "alice", Via: incidents.ActorViaAPI},
		PrimaryProject: scope,
		CanonicalContext: incidents.CanonicalContext{
			Facts: []investigation.Fact{
				{ID: "case-r1-start", Entity: "case", Field: "r1", Value: investigation.NewIntegerValue("1"), Condition: investigation.FactConditionGreaterThanOrEqual, Origin: "context", Enabled: true, Scope: &scopeFact},
				{ID: "case-r1-end", Entity: "case", Field: "r1", Value: investigation.NewIntegerValue("1"), Condition: investigation.FactConditionGreaterThanOrEqual, Origin: "context", Enabled: true, Scope: &scopeFact},
			},
		},
	})
	require.NoError(t, err)
	actStartBadCtx := detectiveIncidentAction{
		OperationID:       "op-start-bad-ctx",
		Type:              "start-case",
		Case:              &detective.Case{ID: "new-case-1", Title: "New Case"},
		IncidentRef:       "ops/" + badIncMut.Projection.Ref.IncidentID,
		Project:           "p",
		SecurityContextID: api.SecurityContextID(),
		Environment:       "qa",
		IfMatch:           0,
	}
	bStartBadCtx, _ := json.Marshal(actStartBadCtx)
	wStartBadCtx := httptest.NewRecorder()
	detectiveIncidentActionHandler(wStartBadCtx, httptest.NewRequest(http.MethodPost, "/datatug/detective/incident-action", bytes.NewReader(bStartBadCtx)))
	assert.Equal(t, http.StatusBadRequest, wStartBadCtx.Code)

	// detectiveIncidentActionHandler start-case with invalid candidate Title -> line 292
	actStartBadTitle := detectiveIncidentAction{
		OperationID:       "op-start-bad-title",
		Type:              "start-case",
		Case:              &detective.Case{ID: "new-case-empty-title", Title: ""},
		IncidentRef:       "ops/" + validMut.Projection.Ref.IncidentID,
		Project:           "p",
		SecurityContextID: api.SecurityContextID(),
		Environment:       "qa",
		IfMatch:           0,
	}
	bStartBadTitle, _ := json.Marshal(actStartBadTitle)
	wStartBadTitle := httptest.NewRecorder()
	detectiveIncidentActionHandler(wStartBadTitle, httptest.NewRequest(http.MethodPost, "/datatug/detective/incident-action", bytes.NewReader(bStartBadTitle)))
	assert.Equal(t, http.StatusBadRequest, wStartBadTitle.Code)

	// 8. detectiveContextFromIncident remaining branches:
	// line 356: ignore facts where Scope is nil, Scope doesn't match, Entity != case, or !Enabled
	valInt := apicontract.NewIntegerValue("1")
	otherScope := incidents.ProjectRef{StoreID: "local", ProjectID: "p2", Environment: "qa"}
	mixedView := incidents.IncidentView{
		CanonicalContext: investigation.ContextView{
			Facts: []investigation.FactView{
				{ID: "f-nil-scope", Entity: "case", Field: "f", Value: investigation.VisibleValue(valInt), Enabled: true, Scope: nil},
				{ID: "f-other-scope", Entity: "case", Field: "f", Value: investigation.VisibleValue(valInt), Enabled: true, Scope: &otherScope},
				{ID: "f-not-case", Entity: "ticket", Field: "f", Value: investigation.VisibleValue(valInt), Enabled: true, Scope: &scope},
				{ID: "f-not-enabled", Entity: "case", Field: "f", Value: investigation.VisibleValue(valInt), Enabled: false, Scope: &scope},
				{ID: "case-f", Entity: "case", Field: "f", Value: investigation.VisibleValue(valInt), Enabled: true, Scope: &scope},
			},
		},
	}
	ctxMixed, err := detectiveContextFromIncident(mixedView, scope)
	require.NoError(t, err)
	assert.Len(t, ctxMixed, 1)

	// line 378: two range facts with both >= -> start != nil, end == nil -> ErrInvalid
	doubleGteView := incidents.IncidentView{
		CanonicalContext: investigation.ContextView{
			Facts: []investigation.FactView{
				{ID: "case-r1-start", Entity: "case", Field: "r1", Value: investigation.VisibleValue(valInt), Condition: investigation.FactConditionGreaterThanOrEqual, Enabled: true, Scope: &scope},
				{ID: "case-r1-end", Entity: "case", Field: "r1", Value: investigation.VisibleValue(valInt), Condition: investigation.FactConditionGreaterThanOrEqual, Enabled: true, Scope: &scope},
			},
		},
	}
	_, err = detectiveContextFromIncident(doubleGteView, scope)
	assert.ErrorIs(t, err, detective.ErrInvalid)

	// line 393: heterogeneous set values -> NewTypedValueSet error
	valStr := apicontract.NewStringValue("hello")
	hetSetView := incidents.IncidentView{
		CanonicalContext: investigation.ContextView{
			Facts: []investigation.FactView{
				{ID: "case-s1-000", Entity: "case", Field: "s1", Value: investigation.VisibleValue(valInt), Enabled: true, Scope: &scope},
				{ID: "case-s1-001", Entity: "case", Field: "s1", Value: investigation.VisibleValue(valStr), Enabled: true, Scope: &scope},
			},
		},
	}
	_, err = detectiveContextFromIncident(hetSetView, scope)
	assert.ErrorIs(t, err, detective.ErrInvalid)

	// line 398: value.Validate() != nil (e.g. invalid scalar value)
	badValScalar := apicontract.TypedValue{Type: "invalid-type"}
	badScalarView := incidents.IncidentView{
		CanonicalContext: investigation.ContextView{
			Facts: []investigation.FactView{
				{ID: "case-bad", Entity: "case", Field: "bad", Value: investigation.VisibleValue(badValScalar), Enabled: true, Scope: &scope},
			},
		},
	}
	_, err = detectiveContextFromIncident(badScalarView, scope)
	assert.ErrorIs(t, err, detective.ErrInvalid)

	// line 346-347: detectiveCaseFacts ValidateScoped error
	badScopeFact := incidents.ProjectRef{StoreID: "", ProjectID: "", Environment: ""}
	_, err = detectiveCaseFacts(map[string]detective.InputValue{
		"": {Scalar: &valInt}, // empty name
	}, badScopeFact)
	assert.Error(t, err)
}

// Not parallel: tests detective.go remaining branches
func TestCoverage100_DetectiveRemainingBranches(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	privateDir := t.TempDir()
	envDir := filepath.Join(projectDir, "environments", "qa")
	require.NoError(t, os.MkdirAll(envDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(envDir, "qa.env.json"), []byte(`{"id":"qa"}`), 0600))
	queriesDir := filepath.Join(projectDir, "queries")
	require.NoError(t, os.MkdirAll(queriesDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(queriesDir, "q1.query.json"), []byte(`{"id":"q1","type":"SQL"}`), 0600))
	paths := map[string]string{"p": projectDir}

	origStore := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
	defer func() {
		storage.NewDatatugStore = origStore
		_ = api.CloseExecutionEvidence()
		api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	}()

	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: privateDir})
	require.NoError(t, api.ConfigureExecutionEvidence(paths, nil, executionstore.Options{PrivateDir: privateDir}))

	// 1. save-query-validators: queryId unavailable -> line 156
	bodySaveBadQ, _ := json.Marshal(struct {
		OperationID string `json:"operationId"`
		detective.ActionRequest
	}{"op1", detective.ActionRequest{
		Type:              "save-query-validators",
		Project:           "p",
		SecurityContextID: api.SecurityContextID(),
		QueryValidators:   &detective.QueryValidators{QueryID: "nonexistent"},
	}})
	wSaveBad := httptest.NewRecorder()
	detectiveActionHandler(wSaveBad, httptest.NewRequest(http.MethodPost, "/datatug/detective/action?project=p", bytes.NewReader(bodySaveBadQ)))
	assert.Equal(t, http.StatusInternalServerError, wSaveBad.Code)

	// save-query-validators: valid queryId -> lines 158-159
	bodySaveGoodQ, _ := json.Marshal(struct {
		OperationID string `json:"operationId"`
		detective.ActionRequest
	}{"op2", detective.ActionRequest{
		Type:              "save-query-validators",
		Project:           "p",
		SecurityContextID: api.SecurityContextID(),
		QueryValidators:   &detective.QueryValidators{QueryID: "q1", Validators: []detective.Validator{{ID: "v1", Scope: "row", Kind: "non-null", Severity: "error", Field: "col1"}}},
	}})
	wSaveGood := httptest.NewRecorder()
	detectiveActionHandler(wSaveGood, httptest.NewRequest(http.MethodPost, "/datatug/detective/action?project=p", bytes.NewReader(bodySaveGoodQ)))
	assert.Equal(t, http.StatusOK, wSaveGood.Code)

	// 2. run-check invalid environment -> lines 162-164
	bodyRunCheckBadEnv, _ := json.Marshal(struct {
		OperationID string `json:"operationId"`
		detective.ActionRequest
	}{"op3", detective.ActionRequest{
		Type:              "run-check",
		Project:           "p",
		SecurityContextID: api.SecurityContextID(),
		Environment:       "invalid env!",
		CheckID:           "c1",
	}})
	wRunCheckBad := httptest.NewRecorder()
	detectiveActionHandler(wRunCheckBad, httptest.NewRequest(http.MethodPost, "/datatug/detective/action?project=p", bytes.NewReader(bodyRunCheckBadEnv)))
	assert.Equal(t, http.StatusBadRequest, wRunCheckBad.Code)

	// 3. run-query-validators invalid env -> lines 166-168
	bodyRunQValBadEnv, _ := json.Marshal(struct {
		OperationID string `json:"operationId"`
		detective.ActionRequest
	}{"op4", detective.ActionRequest{
		Type:              "run-query-validators",
		Project:           "p",
		SecurityContextID: api.SecurityContextID(),
		Environment:       "invalid env!",
		QueryID:           "q1",
	}})
	wRunQValBadEnv := httptest.NewRecorder()
	detectiveActionHandler(wRunQValBadEnv, httptest.NewRequest(http.MethodPost, "/datatug/detective/action?project=p", bytes.NewReader(bodyRunQValBadEnv)))
	assert.Equal(t, http.StatusBadRequest, wRunQValBadEnv.Code)

	// run-query-validators unavailable queryId -> lines 170-174
	bodyRunQValBadQ, _ := json.Marshal(struct {
		OperationID string `json:"operationId"`
		detective.ActionRequest
	}{"op5", detective.ActionRequest{
		Type:              "run-query-validators",
		Project:           "p",
		SecurityContextID: api.SecurityContextID(),
		Environment:       "qa",
		QueryID:           "nonexistent",
	}})
	wRunQValBadQ := httptest.NewRecorder()
	detectiveActionHandler(wRunQValBadQ, httptest.NewRequest(http.MethodPost, "/datatug/detective/action?project=p", bytes.NewReader(bodyRunQValBadQ)))
	assert.Equal(t, http.StatusInternalServerError, wRunQValBadQ.Code)

	// 4. record-scenario-step with StepStatus != completed -> line 185
	bodyStepBadStatus, _ := json.Marshal(struct {
		OperationID string `json:"operationId"`
		detective.ActionRequest
	}{"op6", detective.ActionRequest{
		Type:              "record-scenario-step",
		Project:           "p",
		SecurityContextID: api.SecurityContextID(),
		Execution:         &apicontract.ExecutionRef{ProjectID: "p", StoreID: "p", ExecutionID: "ex1"},
		StepStatus:        "failed",
	}})
	wStepBadStatus := httptest.NewRecorder()
	detectiveActionHandler(wStepBadStatus, httptest.NewRequest(http.MethodPost, "/datatug/detective/action?project=p", bytes.NewReader(bodyStepBadStatus)))
	assert.Equal(t, http.StatusBadRequest, wStepBadStatus.Code)

	// 5. detectiveQueryRevisionLookup branches -> lines 364, 368
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return &mockUnrevisionedDatatugStore{}, nil
	}
	lookupUnrev := detectiveQueryRevisionLookup("p")
	_, err = lookupUnrev(ctx, "q1")
	assert.Error(t, err) // line 364

	fStore, _ := filestore.NewStore("files", paths)
	mockRev := &mockSessionQueryRevisionedStore{ProjectStore: fStore.GetProjectStore("p"), loadErr: errors.New("load rev fail")}
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return &customProjectDatatugStore{Store: fStore, projStore: mockRev}, nil
	}
	lookupRevErr := detectiveQueryRevisionLookup("p")
	_, err = lookupRevErr(ctx, "q1")
	assert.Error(t, err) // line 368

	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }

	// 6. detectiveExecutionMetadata branches -> lines 384, 387
	evStore, err := api.ExecutionEvidenceStoreByID("p", "p")
	require.NoError(t, err)
	ref := apicontract.ExecutionRef{ProjectID: "p", StoreID: "p", ExecutionID: "ex-meta"}
	meta := detectiveExecutionMetadata("p")

	// store.Execution error -> line 384
	_, _, err = meta(ctx, &ref)
	assert.Error(t, err)

	// store.Execution success -> line 387
	complete := true
	fpHex := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	prov := apicontract.Provenance{Source: "sales", Collection: "orders", Mode: apicontract.ProvenanceModeLive, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), ExecutionProfile: apicontract.ExecutionProfileProtected, QueryID: "q1"}
	require.NoError(t, evStore.PutExecution(ctx, apicontract.ExecutionRecord{
		Ref: ref, Scope: apicontract.ExecutionRecordScope{StoreID: "p", Project: "p", Environment: "qa"},
		QueryID: "q1", QueryRevision: "rev1", ResultFingerprint: fpHex, ResultComplete: &complete,
		Principal: apicontract.ExecutionPrincipal{ID: "alice"}, ExecutedAt: time.Now().UTC().Format(time.RFC3339Nano),
		PolicyFingerprint: fpHex, Provenance: prov,
	}))
	rev, fp, err := meta(ctx, &ref)
	require.NoError(t, err)
	assert.Equal(t, "rev1", rev)
	assert.Equal(t, fpHex, fp)

	// 7. loadDetectiveScenarioQueryProof branches -> lines 218, 222, 229
	// StoreByID error -> line 218
	_, _, err = loadDetectiveScenarioQueryProof(ctx, "p", "alice", &apicontract.ExecutionRef{ProjectID: "p", StoreID: "unknown_store", ExecutionID: "ex1"})
	assert.Error(t, err)

	// Execution not found -> line 222
	_, _, err = loadDetectiveScenarioQueryProof(ctx, "p", "alice", &apicontract.ExecutionRef{ProjectID: "p", StoreID: "p", ExecutionID: "nonexistent"})
	assert.Error(t, err)

	// recordedDetectiveInputs error (invalid parameter) -> line 229
	refBadParam := apicontract.ExecutionRef{ProjectID: "p", StoreID: "p", ExecutionID: "ex-bad-param"}
	valOk := apicontract.NewStringValue("ok")
	require.NoError(t, evStore.PutExecution(ctx, apicontract.ExecutionRecord{
		Ref: refBadParam, Scope: apicontract.ExecutionRecordScope{StoreID: "p", Project: "p", Environment: "qa"},
		QueryID: "q1", QueryRevision: "rev1", ResultComplete: &complete, ResultFingerprint: fpHex,
		Principal: apicontract.ExecutionPrincipal{ID: "alice"}, ExecutedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Parameters:        map[string]apicontract.TypedValueOrSet{"bad": {Scalar: &valOk}},
		PolicyFingerprint: fpHex, Provenance: prov,
	}))
	var foundBadPath string
	_ = filepath.Walk(privateDir, func(path string, info os.FileInfo, err error) error {
		if strings.HasSuffix(path, "ex-bad-param.json") {
			foundBadPath = path
		}
		return nil
	})
	if foundBadPath == "" {
		_ = filepath.Walk(projectDir, func(path string, info os.FileInfo, err error) error {
			if strings.HasSuffix(path, "ex-bad-param.json") {
				foundBadPath = path
			}
			return nil
		})
	}
	require.NotEmpty(t, foundBadPath)
	badData, err := os.ReadFile(foundBadPath)
	require.NoError(t, err)
	var badObj map[string]any
	require.NoError(t, json.Unmarshal(badData, &badObj))
	params := badObj["parameters"].(map[string]any)
	params["bad"] = map[string]any{"scalar": map[string]any{"type": "invalid-type", "value": "x"}}
	newBadData, err := json.Marshal(badObj)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(foundBadPath, newBadData, 0600))
	_, _, err = loadDetectiveScenarioQueryProof(ctx, "p", "alice", &refBadParam)
	assert.Error(t, err)

	// 8. loadDetectiveExecutionResult branches -> lines 243, 247, 253, 259, 269, 273, 277, 282, 284, 287, 290
	// StoreByID error -> line 243
	_, _, err = loadDetectiveExecutionResult(ctx, "p", "alice", "q1", "qa", &apicontract.ExecutionRef{ProjectID: "p", StoreID: "unknown", ExecutionID: "ex1"}, nil)
	assert.Error(t, err)

	// Execution not found -> line 247
	_, _, err = loadDetectiveExecutionResult(ctx, "p", "alice", "q1", "qa", &apicontract.ExecutionRef{ProjectID: "p", StoreID: "p", ExecutionID: "nonexistent"}, nil)
	assert.Error(t, err)

	// ResultComplete == nil -> line 253
	refIncomplete := apicontract.ExecutionRef{ProjectID: "p", StoreID: "p", ExecutionID: "ex-incomplete"}
	require.NoError(t, evStore.PutExecution(ctx, apicontract.ExecutionRecord{
		Ref: refIncomplete, Scope: apicontract.ExecutionRecordScope{StoreID: "p", Project: "p", Environment: "qa"},
		QueryID: "q1", Principal: apicontract.ExecutionPrincipal{ID: "alice"}, ExecutedAt: time.Now().UTC().Format(time.RFC3339Nano),
		ResultFingerprint: fpHex, PolicyFingerprint: fpHex, Provenance: prov,
	}))
	_, _, err = loadDetectiveExecutionResult(ctx, "p", "alice", "q1", "qa", &refIncomplete, nil)
	assert.Error(t, err)

	// SnapshotRef == "" -> line 259
	refNoSnap := apicontract.ExecutionRef{ProjectID: "p", StoreID: "p", ExecutionID: "ex-no-snap"}
	require.NoError(t, evStore.PutExecution(ctx, apicontract.ExecutionRecord{
		Ref: refNoSnap, Scope: apicontract.ExecutionRecordScope{StoreID: "p", Project: "p", Environment: "qa"},
		QueryID: "q1", Principal: apicontract.ExecutionPrincipal{ID: "alice"}, ExecutedAt: time.Now().UTC().Format(time.RFC3339Nano),
		ResultComplete: &complete, ResultFingerprint: fpHex,
		PolicyFingerprint: fpHex, Provenance: prov,
	}))
	_, _, err = loadDetectiveExecutionResult(ctx, "p", "alice", "q1", "qa", &refNoSnap, nil)
	assert.Error(t, err)

	// Row count mismatch -> line 269
	refRowsMismatch := apicontract.ExecutionRef{ProjectID: "p", StoreID: "p", ExecutionID: "ex-rows-mismatch"}
	rows := apicontract.Recordset{Columns: []apicontract.Column{{Name: "id", Type: string(apicontract.ValueTypeInteger)}}, Rows: [][]apicontract.TypedValue{{apicontract.NewIntegerValue("1")}}}
	snapRef, stored, err := evStore.PutSnapshot(ctx, refRowsMismatch, rows, time.Now())
	require.NoError(t, err)
	require.True(t, stored)
	fp, _ = apicontract.FingerprintRecordset(rows)
	require.NoError(t, evStore.PutExecution(ctx, apicontract.ExecutionRecord{
		Ref: refRowsMismatch, Scope: apicontract.ExecutionRecordScope{StoreID: "p", Project: "p", Environment: "qa"},
		QueryID: "q1", Principal: apicontract.ExecutionPrincipal{ID: "alice"}, ExecutedAt: time.Now().UTC().Format(time.RFC3339Nano),
		ResultComplete: &complete, SnapshotRef: snapRef, RowCount: 99, ResultFingerprint: fp,
		PolicyFingerprint: fpHex, Provenance: prov,
	}))
	_, _, err = loadDetectiveExecutionResult(ctx, "p", "alice", "q1", "qa", &refRowsMismatch, nil)
	assert.Error(t, err)

	// Fingerprint mismatch -> line 273
	refFpMismatch := apicontract.ExecutionRef{ProjectID: "p", StoreID: "p", ExecutionID: "ex-fp-mismatch"}
	snapRef2, _, _ := evStore.PutSnapshot(ctx, refFpMismatch, rows, time.Now())
	require.NoError(t, evStore.PutExecution(ctx, apicontract.ExecutionRecord{
		Ref: refFpMismatch, Scope: apicontract.ExecutionRecordScope{StoreID: "p", Project: "p", Environment: "qa"},
		QueryID: "q1", Principal: apicontract.ExecutionPrincipal{ID: "alice"}, ExecutedAt: time.Now().UTC().Format(time.RFC3339Nano),
		ResultComplete: &complete, SnapshotRef: snapRef2, RowCount: 1, ResultFingerprint: strings.Repeat("f", 64),
		PolicyFingerprint: fpHex, Provenance: prov,
	}))
	_, _, err = loadDetectiveExecutionResult(ctx, "p", "alice", "q1", "qa", &refFpMismatch, nil)
	assert.Error(t, err)

	// Valid execution setup for compare hooks testing:
	refHooks := apicontract.ExecutionRef{ProjectID: "p", StoreID: "p", ExecutionID: "ex-hooks"}
	snapRefHooks, _, _ := evStore.PutSnapshot(ctx, refHooks, rows, time.Now())
	require.NoError(t, evStore.PutExecution(ctx, apicontract.ExecutionRecord{
		Ref: refHooks, Scope: apicontract.ExecutionRecordScope{StoreID: "p", Project: "p", Environment: "qa"},
		QueryID: "q1", Principal: apicontract.ExecutionPrincipal{ID: "alice"}, ExecutedAt: time.Now().UTC().Format(time.RFC3339Nano),
		ResultComplete: &complete, SnapshotRef: snapRefHooks, RowCount: 1, ResultFingerprint: fp,
		PolicyFingerprint: fpHex, Provenance: prov,
	}))

	// compareSecureExecutorHook !ok -> line 277
	origExecHook := compareSecureExecutorHook
	compareSecureExecutorHook = func() (*secureread.Executor, bool) { return nil, false }
	_, _, err = loadDetectiveExecutionResult(ctx, "p", "alice", "q1", "qa", &refHooks, nil)
	assert.Error(t, err)
	compareSecureExecutorHook = origExecHook

	// compareRunSnapshotHook policy denial error -> line 282
	origSnapHook := compareRunSnapshotHook
	compareRunSnapshotHook = func(executor *secureread.Executor, ctx context.Context, collection string, recordset apicontract.Recordset) (secureread.Result, error) {
		return secureread.Result{}, &customAccessDeniedError{}
	}
	_, _, err = loadDetectiveExecutionResult(ctx, "p", "alice", "q1", "qa", &refHooks, nil)
	assert.Error(t, err)

	// compareRunSnapshotHook generic error -> line 284
	compareRunSnapshotHook = func(executor *secureread.Executor, ctx context.Context, collection string, recordset apicontract.Recordset) (secureread.Result, error) {
		return secureread.Result{}, errors.New("other fail")
	}
	_, _, err = loadDetectiveExecutionResult(ctx, "p", "alice", "q1", "qa", &refHooks, nil)
	assert.Error(t, err)

	// compareRunSnapshotHook filtered.SnapshotRecordset == nil -> line 287
	compareRunSnapshotHook = func(executor *secureread.Executor, ctx context.Context, collection string, recordset apicontract.Recordset) (secureread.Result, error) {
		return secureread.Result{SnapshotRecordset: nil}, nil
	}
	_, _, err = loadDetectiveExecutionResult(ctx, "p", "alice", "q1", "qa", &refHooks, nil)
	assert.Error(t, err)

	// compareRunSnapshotHook rows mismatch -> line 290
	otherRows := apicontract.Recordset{Columns: []apicontract.Column{{Name: "id", Type: string(apicontract.ValueTypeInteger)}}, Rows: [][]apicontract.TypedValue{{apicontract.NewIntegerValue("2")}}}
	compareRunSnapshotHook = func(executor *secureread.Executor, ctx context.Context, collection string, recordset apicontract.Recordset) (secureread.Result, error) {
		return secureread.Result{SnapshotRecordset: &otherRows}, nil
	}
	_, _, err = loadDetectiveExecutionResult(ctx, "p", "alice", "q1", "qa", &refHooks, nil)
	assert.Error(t, err)

	compareRunSnapshotHook = origSnapHook

	// run-query-validators with Execution -> lines 176-181
	bodyRunQValExec, _ := json.Marshal(struct {
		OperationID string `json:"operationId"`
		detective.ActionRequest
	}{"op-run-val-exec", detective.ActionRequest{
		Type:              "run-query-validators",
		Project:           "p",
		SecurityContextID: api.SecurityContextID(),
		Environment:       "qa",
		QueryID:           "q1",
		Execution:         &apicontract.ExecutionRef{ProjectID: "p", StoreID: "unknown_store", ExecutionID: "ex1"},
	}})
	wRunQValExec := httptest.NewRecorder()
	detectiveActionHandler(wRunQValExec, httptest.NewRequest(http.MethodPost, "/datatug/detective/action?project=p", bytes.NewReader(bodyRunQValExec)))
	assert.Equal(t, http.StatusInternalServerError, wRunQValExec.Code)

	// record-scenario-step with mismatched queryId -> lines 187-192
	bodyStepMismatch, _ := json.Marshal(struct {
		OperationID string `json:"operationId"`
		detective.ActionRequest
	}{"op-step-mismatch", detective.ActionRequest{
		Type:              "record-scenario-step",
		Project:           "p",
		SecurityContextID: api.SecurityContextID(),
		Execution:         &ref,
		StepStatus:        "completed",
		QueryID:           "mismatched_query",
		Environment:       "qa",
	}})
	wStepMismatch := httptest.NewRecorder()
	detectiveActionHandler(wStepMismatch, httptest.NewRequest(http.MethodPost, "/datatug/detective/action?project=p", bytes.NewReader(bodyStepMismatch)))
	assert.Equal(t, http.StatusInternalServerError, wStepMismatch.Code)

	// run-check replay actionLinkStatus -> line 203
	localDetStore, err := detective.NewLocalStore("p", projectDir, privateDir)
	require.NoError(t, err)
	reqCheck := detective.ActionRequest{
		Type:              "run-check",
		Project:           "p",
		SecurityContextID: api.SecurityContextID(),
		CheckID:           "c1",
		Environment:       "qa",
	}
	bindCheck, err := localDetStore.ActionBinding(reqCheck, "alice")
	require.NoError(t, err)
	rcptCheck, err := coredetective.NewOperationReceipt("op-replay-check", bindCheck, 0, time.Now().UTC())
	require.NoError(t, err)
	matchesDet, _ := filepath.Glob(filepath.Join(privateDir, "detective", "*"))
	require.NotEmpty(t, matchesDet)
	opCheckDir := filepath.Join(matchesDet[0], "action-operations")
	require.NoError(t, os.MkdirAll(opCheckDir, 0700))
	opCheckData, _ := json.Marshal(struct {
		Kind           string                         `json:"kind"`
		ID             string                         `json:"id"`
		Receipt        coredetective.OperationReceipt `json:"receipt"`
		Produced       bool                           `json:"produced"`
		Done           bool                           `json:"done"`
		CaseLinkStatus string                         `json:"caseLinkStatus"`
		Response       detective.ActionResponse       `json:"response"`
	}{
		Kind:           "run",
		ID:             "p:alice:op-replay-check",
		Receipt:        rcptCheck,
		Produced:       true,
		Done:           true,
		CaseLinkStatus: "linked",
	})
	require.NoError(t, os.WriteFile(filepath.Join(opCheckDir, "op-replay-check.json"), opCheckData, 0600))
	bodyReplayCheck, _ := json.Marshal(struct {
		OperationID string `json:"operationId"`
		detective.ActionRequest
	}{"op-replay-check", reqCheck})
	wReplayCheck := httptest.NewRecorder()
	detectiveActionHandler(wReplayCheck, httptest.NewRequest(http.MethodPost, "/datatug/detective/action?project=p", bytes.NewReader(bodyReplayCheck)))
	assert.Equal(t, http.StatusOK, wReplayCheck.Code)

	// loadDetectiveExecutionResult Snapshot fails -> line 262
	refSnapMissing := apicontract.ExecutionRef{ProjectID: "p", StoreID: "p", ExecutionID: "ex-snap-missing"}
	require.NoError(t, evStore.PutExecution(ctx, apicontract.ExecutionRecord{
		Ref: refSnapMissing, Scope: apicontract.ExecutionRecordScope{StoreID: "p", Project: "p", Environment: "qa"},
		QueryID: "q1", QueryRevision: "rev1", ResultFingerprint: fpHex, ResultComplete: &complete,
		Principal: apicontract.ExecutionPrincipal{ID: "alice"}, ExecutedAt: time.Now().UTC().Format(time.RFC3339Nano),
		PolicyFingerprint: fpHex, Provenance: prov, SnapshotRef: "missing-snap",
	}))
	_, _, err = loadDetectiveExecutionResult(ctx, "p", "alice", "q1", "qa", &refSnapMissing, nil)
	assert.Error(t, err)

	// detectiveQueryRevisionLookup api.ProjectStoreFor fails -> line 351
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return nil, errors.New("cannot create store")
	}
	lookupProjErr := detectiveQueryRevisionLookup("p")
	_, err = lookupProjErr(ctx, "q1")
	assert.Error(t, err)
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
}

type customProjectDatatugStore struct {
	storage.Store
	projStore datatug.ProjectStore
}

func (c *customProjectDatatugStore) GetProjectStore(string) datatug.ProjectStore {
	return c.projStore
}

type customAccessDeniedError struct{}

func (c *customAccessDeniedError) Error() string { return "access denied" }
func (c *customAccessDeniedError) Is(target error) bool {
	return target != nil && strings.Contains(target.Error(), "denied")
}
