package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

const compatibilityQuery = `{"createdAt":"2026-01-01T00:00:00Z","id":"items","title":"Query","type":"SQL","text":"SELECT 1 AS id"}`

func captureCompatibilityCheck(t *testing.T, project, private string) (*detective.LocalStore, detective.NativeCapture, error) {
	t.Helper()
	metadata, err := filestore.LoadProjectFile(project)
	if err != nil {
		t.Fatal(err)
	}
	store, err := detective.NewLocalStore(metadata.ID, project, private)
	if err != nil {
		t.Fatal(err)
	}
	reference := apicontract.SessionInputReference{SchemaVersion: 1, StorageID: "local", DatabaseID: "db", ProjectID: metadata.ID, NamespaceID: "ns", SessionID: "session", InputSnapshotID: "input", ContextSetID: "context", EnvironmentID: "local", IdentityDomain: "tenant"}
	capture, err := store.CaptureNativeInput(context.Background(), detective.NativeCaptureRequest{CheckID: "pass", NativeOperationID: "compat", Actor: "alice", Reference: reference}, func(context.Context, string) (sessioncontext.DetectiveAcceptedQueryPin, error) {
		return sessioncontext.DetectiveAcceptedQueryPin{QueryID: "items", Revision: "compat-v1", DefinitionJSON: json.RawMessage(compatibilityQuery)}, nil
	})
	return store, capture, err
}

func writeCompatibilityCreatedAt(t *testing.T, project, createdAt string) detective.Check {
	t.Helper()
	path := filepath.Join(project, "checks", "pass.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var check detective.Check
	if err = apicontract.DecodeStrict(raw, &check); err != nil {
		t.Fatal(err)
	}
	check.CreatedAt = createdAt
	raw, err = json.Marshal(check)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var decoded detective.Check
	if err = apicontract.DecodeStrict(raw, &decoded); err != nil || !reflect.DeepEqual(check, decoded) {
		t.Fatalf("definition decoding changed: %+v, %v", decoded, err)
	}
	return decoded
}

func TestChecksCreatedAtCompatibilityLegacyCanonicalDigest(t *testing.T) {
	project, private := checksDemo(t)
	before := checksProjectTree(t, project)
	_, capture, err := captureCompatibilityCheck(t, project, private)
	if err != nil {
		t.Fatal(err)
	}
	// Frozen output of Core v0.79.0 CanonicalDetectiveDefinition for the actual
	// checks-demo/pass.json fixture and compatibilityQuery at revision compat-v1.
	// Compute this baseline with v0.79.0, never with the dependency under test.
	const legacyDigest = "01176e0b5ab907ce31d5d552d61ea06290c37b397909802270280bc280ac07e2"
	if capture.Pin.Dispatch.DefinitionDigest != legacyDigest || capture.Pin.Reference.DefinitionDigest != legacyDigest {
		t.Fatalf("legacy executable identity changed: %s", capture.Pin.Dispatch.DefinitionDigest)
	}
	encoded, err := json.Marshal(capture.Pin.Definition.Check)
	if err != nil || capture.Pin.Definition.Check.CreatedAt != "" || strings.Contains(string(encoded), `"createdAt"`) {
		t.Fatalf("legacy Check was dated during capture: %s, %v", encoded, err)
	}
	if after := checksProjectTree(t, project); !reflect.DeepEqual(before, after) {
		t.Fatal("native capture rewrote the legacy project")
	}
}

func TestChecksCreatedAtCompatibilityDatedNativeAndSummary(t *testing.T) {
	project, private := checksDemo(t)
	const createdAt = "2026-10-25T01:02:03.123456789+02:00"
	check := writeCompatibilityCreatedAt(t, project, createdAt)
	before := checksProjectTree(t, project)
	store, capture, err := captureCompatibilityCheck(t, project, private)
	if err != nil || !reflect.DeepEqual(capture.Pin.Definition.Check, check) || check.Version != 1 {
		t.Fatalf("dated definition/version was not preserved: %+v, %v", capture.Pin.Definition.Check, err)
	}
	owned, digest, err := sessioncontext.CanonicalDetectiveDefinition(context.Background(), capture.Pin.Definition)
	if err != nil || digest != capture.Pin.Dispatch.DefinitionDigest || !reflect.DeepEqual(owned, capture.Pin.Definition) {
		t.Fatalf("dated canonical identity is unstable: %s, %v", digest, err)
	}
	owned.Check.CreatedAt = "2026-10-25T01:02:04.123456789+02:00"
	if _, changed, err := sessioncontext.CanonicalDetectiveDefinition(context.Background(), owned); err != nil || changed == digest {
		t.Fatalf("populated creation date disappeared from executable identity: %s, %v", changed, err)
	}
	if err = store.ConfigureNativeFence(func(context.Context, sessioncontext.DetectiveFence) error { return nil }); err != nil {
		t.Fatal(err)
	}
	reference := capture.Pin.Reference
	fence := sessioncontext.DetectiveFence{Generation: 1, Identity: sessioncontext.DetectiveIdentity{Namespace: sessioncontext.NativeNamespace{StorageID: reference.StorageID, DatabaseID: reference.DatabaseID, ProjectID: reference.ProjectID, NamespaceID: reference.NamespaceID}, Actor: "alice", SessionID: reference.SessionID, RunID: "compat-session-run", NativeStoreID: "local", NativeProjectID: reference.ProjectID, NativeOperationID: "compat", Digest: capture.Binding.PayloadDigest}}
	calls := 0
	response, err := store.ExecuteNativePin(context.Background(), fence, capture.Pin, true, func(ctx context.Context, queryID, environment string, _ map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		calls++
		query, ok := detective.NativeAcceptedQuery(ctx, queryID)
		if !ok || query.Revision != "compat-v1" || environment != "local" {
			t.Fatal("native execution lost the accepted query/environment")
		}
		return apicontract.Result{Recordset: apicontract.Recordset{Columns: []apicontract.Column{{Name: "id", Type: "integer"}}, Rows: [][]apicontract.TypedValue{{apicontract.NewIntegerValue("1")}}}}, nil
	}, nil)
	if err != nil || calls != 1 || response.Run == nil || response.Run.ExecutionStatus != "completed" || response.Run.Outcome != "pass" || !reflect.DeepEqual(response.Run.Definition, check) {
		t.Fatalf("dated native pin is not executable: %+v, calls=%d, %v", response.Run, calls, err)
	}
	// Run the real saved-query SQLite path used by `checks run`; its public DTO
	// must stay independent of the private Check definition and creation date.
	summary, output, err := executeChecks(t, context.Background(), project, private, "--check", "pass")
	if err != nil || summary.Status != "pass" || len(summary.Checks) != 1 || summary.Checks[0].ExecutionStatus != "completed" || summary.Checks[0].Outcome != "pass" {
		t.Fatalf("dated Check is not executable: %+v, %v", summary, err)
	}
	var envelope map[string]json.RawMessage
	if err = json.Unmarshal([]byte(output), &envelope); err != nil || len(envelope) != 6 {
		t.Fatalf("public envelope changed: %s, %v", output, err)
	}
	for _, key := range []string{"schemaVersion", "status", "exitCode", "checks", "deepLink", "deepLinkReason"} {
		if _, ok := envelope[key]; !ok {
			t.Fatalf("public envelope lost %s", key)
		}
	}
	var results []map[string]json.RawMessage
	if err = json.Unmarshal(envelope["checks"], &results); err != nil || len(results) != 1 || len(results[0]) != 6 {
		t.Fatalf("public result fields changed: %s, %v", output, err)
	}
	for _, key := range []string{"index", "status", "executionStatus", "outcome", "severity", "durationMs"} {
		if _, ok := results[0][key]; !ok {
			t.Fatalf("public result lost %s", key)
		}
	}
	if strings.Contains(output, "createdAt") || strings.Contains(output, createdAt) {
		t.Fatal("private creation date escaped into the summary")
	}
	if after := checksProjectTree(t, project); !reflect.DeepEqual(before, after) {
		t.Fatal("execution rewrote the dated project")
	}
}

func TestChecksCreatedAtCompatibilityMalformedDateRefused(t *testing.T) {
	project, private := checksDemo(t)
	writeCompatibilityCreatedAt(t, project, "yesterday-private-sentinel")
	if _, _, err := captureCompatibilityCheck(t, project, private); !errors.Is(err, detective.ErrInvalid) {
		t.Fatalf("native catalog accepted malformed creation date: %v", err)
	}
	summary, output, _ := executeChecks(t, context.Background(), project, private, "--check", "pass")
	if summary.ExitCode != 2 || summary.Status != "configuration_error" || strings.Contains(output, "yesterday-private-sentinel") {
		t.Fatalf("CLI admitted or exposed malformed creation date: %s", output)
	}
	// No saved-query execution is admitted after Core rejects the definition.
	receipts, err := filepath.Glob(filepath.Join(private, "check-executions", "*", "repository", "executions", "*", "*", "*.json"))
	if err != nil || len(receipts) != 0 {
		t.Fatalf("malformed Check executed a saved query: %v, %v", receipts, err)
	}
}
