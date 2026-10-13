package endpoints

import (
	"bytes"
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
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/investigation"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

func TestDetectiveIncidentBridgeCreatesAndReplaysWithIndependentScope(t *testing.T) {
	project, private, dedicated := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, "environments", "qa"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "environments", "qa", "qa.env.json"), []byte(`{"id":"qa"}`), 0600); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{"p": project}
	previousStore := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
	t.Cleanup(func() { storage.NewDatatugStore = previousStore })
	api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "alice"}, Unrestricted: true}, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	if err := api.ConfigureExecutionEvidence(paths, []incidentstore.ConfiguredStore{{StoreID: "ops", Kind: incidents.StoreLocationDedicatedRepository, Repository: dedicated}}, executionstore.Options{PrivateDir: private}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = api.CloseExecutionEvidence() })
	contextID := api.SecurityContextID()
	stateResponse := httptest.NewRecorder()
	detectiveStateHandler(stateResponse, httptest.NewRequest(http.MethodGet, "/datatug/detective/state?project=p&securityContextId="+contextID, nil))
	var state detective.State
	if stateResponse.Code != http.StatusOK || json.Unmarshal(stateResponse.Body.Bytes(), &state) != nil || len(state.Environments) != 1 || state.Environments[0] != "qa" {
		t.Fatalf("logical environment choices = %d %s", stateResponse.Code, stateResponse.Body.String())
	}
	local, _, err := detectiveStore("p", contextID)
	if err != nil {
		t.Fatal(err)
	}
	invoiceIDs, err := apicontract.NewTypedValueSet(investigation.NewIntegerValue("1"), investigation.NewIntegerValue("2"), investigation.NewIntegerValue("3"), investigation.NewIntegerValue("4"), investigation.NewIntegerValue("5"), investigation.NewIntegerValue("6"), investigation.NewIntegerValue("7"))
	if err != nil {
		t.Fatal(err)
	}
	customer := investigation.NewStringValue("C-7")
	created, err := local.SaveCase(t.Context(), detective.Case{ID: "case-1", Title: "Investigate", Context: map[string]detective.InputValue{"invoice_ids": {Set: &invoiceIDs}, "orderDate": {Range: &detective.ValueRange{Type: "range", Start: investigation.NewDateValue("2026-10-01"), End: investigation.NewDateValue("2026-10-12")}}, "customerID": {Scalar: &customer}}}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	post := func(body detectiveIncidentAction) (int, detectiveIncidentResponse, string) {
		t.Helper()
		body.Project, body.SecurityContextID = "p", contextID
		data, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		recorder := httptest.NewRecorder()
		detectiveIncidentActionHandler(recorder, httptest.NewRequest(http.MethodPost, "/datatug/detective/incident-action", bytes.NewReader(data)))
		var response detectiveIncidentResponse
		_ = json.Unmarshal(recorder.Body.Bytes(), &response)
		return recorder.Code, response, recorder.Body.String()
	}
	action := detectiveIncidentAction{Type: "create", OperationID: "create-incident-1", CaseID: created.ID, IfMatch: created.Version, Environment: "qa", Title: "Invoice issue"}
	code, result, body := post(action)
	if code != http.StatusOK || result.Case == nil || len(result.Case.IncidentRefs) != 1 || result.Incident.Ref.StoreID != "p" || len(result.Incident.CanonicalContext.Facts) != 10 {
		t.Fatalf("create = %d %s", code, body)
	}
	code, replay, body := post(action)
	if code != http.StatusOK || replay.Incident.Ref != result.Incident.Ref || replay.Case == nil || replay.Case.Version != result.Case.Version {
		t.Fatalf("replay = %d %s", code, body)
	}
	action.Title = "changed payload"
	if code, _, _ = post(action); code != http.StatusConflict {
		t.Fatalf("same operation accepted changed payload: %d", code)
	}
	forged := investigation.NewStringValue("not from incident")
	fromContext := detectiveIncidentAction{Type: "start-case", OperationID: "start-context-case", IfMatch: 0, Environment: "qa", IncidentRef: result.Incident.Ref.String(), Case: &detective.Case{ID: "case-context", Title: "Context follow-up", Context: map[string]detective.InputValue{"forged": {Scalar: &forged}}, Evidence: []detective.EvidenceRef{{ID: "forged-note", Kind: "external", Ref: "caller"}}}}
	if code, _, _ = post(fromContext); code != http.StatusBadRequest {
		t.Fatalf("caller-forged start-case content accepted: %d", code)
	}
	fromContext.Case = &detective.Case{ID: "case-context", Title: "Context follow-up"}
	code, resumed, body := post(fromContext)
	if code != http.StatusOK || resumed.Case == nil || resumed.Case.Context["invoice_ids"].Set == nil || len(resumed.Case.Context["invoice_ids"].Set.Values) != 7 || resumed.Case.Context["orderDate"].Range == nil || resumed.Case.Context["customerID"].Scalar == nil || len(resumed.Case.Evidence) != 0 {
		t.Fatalf("typed Case context resume = %d %s", code, body)
	}
	path := "/datatug/detective/incidents?project=p&caseId=case-1&securityContextId=" + contextID
	read := httptest.NewRecorder()
	detectiveIncidentsHandler(read, httptest.NewRequest(http.MethodGet, path, nil))
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), "Invoice issue") {
		t.Fatalf("linked read = %d %s", read.Code, read.Body.String())
	}
	// The Case grant alone never authorizes a foreign incident. Its repository
	// may be configured, but the incident must independently belong to p/qa.
	otherStore, err := api.IncidentStoreByID("p", "ops")
	if err != nil {
		t.Fatal(err)
	}
	other, err := otherStore.Create(t.Context(), incidents.CreateMutation{MutationID: "foreign-create", StoreID: "ops", UID: "foreign", Title: "Foreign", At: time.Now().UTC(), Reporter: incidents.Actor{Kind: incidents.ActorHuman, ID: "alice", Via: incidents.ActorViaAPI}, PrimaryProject: incidents.ProjectRef{StoreID: "local", ProjectID: "other", Environment: "qa"}})
	if err != nil {
		t.Fatal(err)
	}
	foreign := detectiveIncidentAction{Type: "link", OperationID: "foreign-link-1", CaseID: "case-1", IfMatch: result.Case.Version, Environment: "qa", IncidentRef: other.Projection.Ref.String()}
	if code, _, _ = post(foreign); code != http.StatusForbidden {
		t.Fatalf("foreign incident link = %d", code)
	}
	authorized, err := otherStore.Create(t.Context(), incidents.CreateMutation{MutationID: "dedicated-create", StoreID: "ops", UID: "dedicated", Title: "Shared invoice issue", At: time.Now().UTC(), Reporter: incidents.Actor{Kind: incidents.ActorHuman, ID: "alice", Via: incidents.ActorViaAPI}, PrimaryProject: incidents.ProjectRef{StoreID: "local", ProjectID: "p", Environment: "qa"}})
	if err != nil {
		t.Fatal(err)
	}
	linkedAction := detectiveIncidentAction{Type: "link", OperationID: "dedicated-link-1", CaseID: "case-1", IfMatch: result.Case.Version, Environment: "qa", IncidentRef: authorized.Projection.Ref.String()}
	code, linked, body := post(linkedAction)
	if code != http.StatusOK || linked.Case == nil || len(linked.Case.IncidentRefs) != 2 || linked.Incident.Ref.StoreID != "ops" {
		t.Fatalf("dedicated link = %d %s", code, body)
	}
	read = httptest.NewRecorder()
	detectiveIncidentsHandler(read, httptest.NewRequest(http.MethodGet, path, nil))
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), "Shared invoice issue") {
		t.Fatalf("linked dedicated read = %d %s", read.Code, read.Body.String())
	}
	startAction := detectiveIncidentAction{Type: "start-case", OperationID: "dedicated-start-1", IfMatch: 0, Environment: "qa", IncidentRef: authorized.Projection.Ref.String(), Case: &detective.Case{ID: "case-2", Title: "Follow up"}}
	code, started, body := post(startAction)
	if code != http.StatusOK || started.Case == nil || started.Case.ID != "case-2" || len(started.Case.IncidentRefs) != 1 {
		t.Fatalf("start case from incident = %d %s", code, body)
	}
}

func TestDetectiveIncidentContextRejectsTruncationAndRedaction(t *testing.T) {
	scope := incidents.ProjectRef{StoreID: "local", ProjectID: "p", Environment: "qa"}
	values := make([]apicontract.TypedValue, 201)
	for i := range values {
		values[i] = investigation.NewIntegerValue(fmt.Sprint(i))
	}
	set, err := apicontract.NewTypedValueSet(values...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := detectiveCaseFacts(map[string]detective.InputValue{"invoice_ids": {Set: &set}}, scope); !errors.Is(err, detective.ErrCapacity) {
		t.Fatalf("large Case set silently truncated: %v", err)
	}
	view := incidents.IncidentView{CanonicalContext: investigation.ContextView{Facts: []investigation.FactView{{ID: "case-customerID", Entity: "case", Field: "customerID", Value: investigation.RedactedValue(), Enabled: true, Scope: &scope}}}}
	if _, err := detectiveContextFromIncident(view, scope); !errors.Is(err, detective.ErrInvalid) {
		t.Fatalf("redacted Case fact silently copied: %v", err)
	}
	view.CanonicalContext.Facts = []investigation.FactView{{ID: "case-orderDate-start", Entity: "case", Field: "orderDate", Value: investigation.VisibleValue(investigation.NewDateValue("2026-10-01")), Condition: investigation.FactConditionGreaterThanOrEqual, Enabled: true, Scope: &scope}}
	if _, err := detectiveContextFromIncident(view, scope); !errors.Is(err, detective.ErrInvalid) {
		t.Fatalf("incomplete Case range silently copied: %v", err)
	}
}
