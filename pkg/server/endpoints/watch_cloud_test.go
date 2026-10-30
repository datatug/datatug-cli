package endpoints

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/watch"
	"github.com/datatug/datatug-cli/pkg/watchdaemon"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	core "github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/storage"
)

func cloudArtifactFixture(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE assignments(order_id INTEGER,assignment_id TEXT);INSERT INTO assignments VALUES(11045,'assignment-11045')"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func sealEndpointCloudBundle(t *testing.T, b *watchdaemon.CloudExecutionBundle) watch.Claim {
	t.Helper()
	b.SourceRevision, b.BundleDigest = "", ""
	semantic := *b
	semantic.Artifacts = nil
	semantic.Queries = map[string]watchdaemon.CloudBundleQuery{}
	for id, q := range b.Queries {
		q.SourcePin = nil
		semantic.Queries[id] = q
	}
	raw, err := json.Marshal(semantic)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	b.SourceRevision = hex.EncodeToString(sum[:])
	raw, err = json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	sum = sha256.Sum256(raw)
	b.BundleDigest = hex.EncodeToString(sum[:])
	value := apicontract.NewIntegerValue("11045")
	return watch.Claim{Definition: watch.Definition{Scope: b.Scope, CheckID: b.CheckID, CheckVersion: b.CheckVersion, SourceRevision: b.SourceRevision, Inputs: map[string]core.InputValue{"order_id": {Scalar: &value}}}}
}
func endpointCloudBundle(t *testing.T) (watchdaemon.CloudExecutionBundle, watch.Claim) {
	t.Helper()
	data := cloudArtifactFixture(t)
	sum := sha256.Sum256(data)
	identity := &watchdaemon.CloudSourceIdentity{SourceID: "shipping-working-copy", Environment: semanticTestEnv, BindingRevision: 1, Generation: "g1"}
	var pin watchdaemon.CloudSourcePin
	pin.SpaceID, pin.ProjectID, pin.Environment, pin.SourceID, pin.BindingRevision = "space", "cloud-project", semanticTestEnv, identity.SourceID, 1
	pin.Revision.Generation, pin.Revision.Revision, pin.Revision.Bytes, pin.Revision.SHA256 = "g1", 1, len(data), hex.EncodeToString(sum[:])
	pinJSON, err := json.Marshal(pin)
	if err != nil {
		t.Fatal(err)
	}
	query := datatug.QueryDefWithFolderPath{FolderPath: "shipping"}
	query.Title = "Carrier assignment"
	query.ID, query.CreatedAt, query.Type, query.Text, query.ConnectionID = "assignment", "2026-10-24T00:00:00Z", datatug.QueryTypeSQL, "SELECT assignment_id FROM assignments WHERE order_id=@order_id", identity.SourceID
	query.Parameters = datatug.Parameters{{ID: "order_id", Type: "integer", IsRequired: true}}
	query.Recordsets = []datatug.RecordsetDefinition{{Type: "recordset", Columns: datatug.RecordsetColumnDefs{{Name: "assignment_id", Type: "string"}}}}
	if err := query.Validate(); err != nil {
		t.Fatalf("fixture query invalid: %v", err)
	}
	raw, err := json.Marshal(query)
	if err != nil {
		t.Fatal(err)
	}
	one := apicontract.NewIntegerValue("1")
	check := core.Check{ID: "cloud-check", Title: "Carrier assignment", Version: 1, Inputs: []core.Input{{Name: "order_id", Type: "integer", Cardinality: "scalar", Required: true}}, Steps: []core.Step{{ID: "read", Kind: "query", QueryID: "shipping/" + query.ID, Bindings: []core.Binding{{Input: "order_id", FromInput: "order_id"}}, Validators: []core.Validator{{ID: "assigned", Scope: "set", Kind: "row-count", Severity: "error", Expected: &one}}}}}
	if err := core.ValidateCheck(check); err != nil {
		t.Fatalf("fixture Check invalid: %v", err)
	}
	b := watchdaemon.CloudExecutionBundle{Contract: "watch-execution-bundle/1", Scope: watch.Scope{Namespace: "space", Project: "cloud-project", Environment: semanticTestEnv}, CheckID: check.ID, CheckVersion: "1", Catalog: core.Catalog{Checks: map[string]core.Check{check.ID: check}}, Queries: map[string]watchdaemon.CloudBundleQuery{"shipping/" + query.ID: {Revision: "saved-query-revision", JSON: raw, SourcePin: pinJSON, SourceIdentity: identity}}, Sources: []watchdaemon.CloudBundleSource{{ID: "source-grant", Kind: "native", SourceID: identity.SourceID}}, Artifacts: map[string][]byte{identity.SourceID: data}}
	return b, sealEndpointCloudBundle(t, &b)
}
func TestCloudWatchCheckExecutesExactArtifactAndAliasIndependentBaseline(t *testing.T) {
	ctx := context.Background()
	dir, project := writeRunQueryTestProject(t)
	previous := storage.NewDatatugStore
	defer func() { storage.NewDatatugStore = previous }()
	configureSemanticSession(t, dir, project, "alice", []string{"admin"})
	api.ConfigureSecureSession(secureread.Session{Unrestricted: true, AllowOpaqueSQL: true}, map[string]string{project: dir}, api.Capabilities{AllowWrites: true, EvidencePrivateDir: t.TempDir()})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	configureExecutionEvidence(t, project, dir)
	bundle, claim := endpointCloudBundle(t)
	first, fingerprint, err := ExecuteCloudWatchCheck(ctx, project, "cloud-first", claim, bundle, map[string]string{"source-grant": "chinook-local"})
	if err != nil || first.ExecutionStatus != "completed" || first.Outcome != "pass" {
		t.Fatalf("immutable artifact did not execute through existing runner: %+v %v", first, err)
	}
	claim.Definition.SourceFingerprint = fingerprint
	second, secondFingerprint, err := ExecuteCloudWatchCheck(ctx, project, "cloud-second", claim, bundle, map[string]string{"source-grant": "chinook", "unused-grant": "other-local-path"})
	if err != nil || second.Outcome != "pass" || secondFingerprint != fingerprint {
		t.Fatalf("operator aliases changed semantic baseline: %+v %q %v", second, secondFingerprint, err)
	}
	// The local Chinook file has no assignments table: passing above proves the
	// evaluator used the admitted image, not a mutable local fixture fallback.
	denied, _, err := ExecuteCloudWatchCheck(ctx, project, "cloud-denied", claim, bundle, map[string]string{"source-grant": "missing"})
	if err == nil && (denied.ExecutionStatus != "error" || denied.Outcome != "unknown") {
		t.Fatal("unauthorized local source accepted")
	}
	bundle.Artifacts["shipping-working-copy"][0] ^= 1
	if _, _, err = ExecuteCloudWatchCheck(ctx, project, "cloud-corrupt", claim, bundle, map[string]string{"source-grant": "chinook"}); !errors.Is(err, watch.ErrConflict) {
		t.Fatal(err)
	}
}
func TestCloudArtifactRejectsMalformedDatabaseBeforeEvaluation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "artifact.sqlite")
	for _, data := range [][]byte{[]byte("not sqlite"), append([]byte("SQLite format 3\x00"), make([]byte, 120)...)} {
		if err := prepareWatchArtifact(ctx, path, data); err == nil {
			t.Fatal("malformed image accepted")
		}
	}
	if err := prepareWatchArtifact(ctx, filepath.Join(path, "missing", "file"), cloudArtifactFixture(t)); err == nil {
		t.Fatal("write error lost")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := prepareWatchArtifact(cancelled, path, cloudArtifactFixture(t)); err == nil {
		t.Fatal("cancellation lost")
	}
}
