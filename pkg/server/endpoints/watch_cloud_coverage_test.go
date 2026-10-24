package endpoints

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/watch"
	"github.com/datatug/datatug-cli/pkg/watchdaemon"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
)

func cloudCoverageLocalSession(t *testing.T) string {
	t.Helper()
	dir, project := writeRunQueryTestProject(t)
	previous := storage.NewDatatugStore
	t.Cleanup(func() {
		storage.NewDatatugStore = previous
		api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	})
	configureSemanticSession(t, dir, project, "alice", []string{"admin"})
	api.ConfigureSecureSession(secureread.Session{Unrestricted: true, AllowOpaqueSQL: true}, map[string]string{project: dir}, api.Capabilities{AllowWrites: true, EvidencePrivateDir: t.TempDir()})
	configureExecutionEvidence(t, project, dir)
	return project
}

func TestCloudWatchCheckRejectsUntrustedBindingsAndArtifactFailures(t *testing.T) {
	for _, mode := range []string{"environment", "asset-permission", "project-removed", "empty-source", "missing-binding", "malformed-query", "targets", "local-alias", "native-type", "missing-identity", "temporary-directory", "corrupt-image", "fingerprint"} {
		t.Run(mode, func(t *testing.T) {
			project := cloudCoverageLocalSession(t)
			b, claim := endpointCloudBundle(t)
			bindings := map[string]string{"source-grant": "chinook"}
			want := watch.ErrForbidden
			q := b.Queries["shipping/assignment"]
			var definition datatug.QueryDefWithFolderPath
			if err := json.Unmarshal(q.JSON, &definition); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "environment":
				claim.Definition.Environment = "../foreign"
				want = watch.ErrInvalid
			case "asset-permission":
				api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
			case "project-removed":
				old := detectiveResolvePrivateDir
				defer func() { detectiveResolvePrivateDir = old }()
				detectiveResolvePrivateDir = func(base string) (string, error) {
					api.ConfigureSecureSession(secureread.Session{Unrestricted: true}, nil, api.Capabilities{})
					return base, nil
				}
			case "empty-source":
				b.Sources[0].ID = ""
			case "missing-binding":
				bindings = nil
			case "malformed-query":
				q.JSON = json.RawMessage(`[]`)
				want = watch.ErrInvalid
			case "targets":
				definition.Targets = []datatug.QueryDefTarget{{}}
				q.JSON, _ = json.Marshal(definition)
				want = watch.ErrInvalid
			case "local-alias":
				bindings["source-grant"] = "../escape"
			case "native-type":
				definition.Type = datatug.QueryTypeTugQL
				q.JSON, _ = json.Marshal(definition)
			case "missing-identity":
				q.SourceIdentity, q.SourcePin, b.Artifacts = nil, nil, nil
			case "temporary-directory":
				blocked := filepath.Join(t.TempDir(), "not-a-directory")
				if err := os.WriteFile(blocked, []byte("blocked"), 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("TMPDIR", blocked)
				want = nil
			case "corrupt-image":
				data := append([]byte("SQLite format 3\x00"), make([]byte, 120)...)
				b.Artifacts["shipping-working-copy"] = data
				var pin watchdaemon.CloudSourcePin
				if err := json.Unmarshal(q.SourcePin, &pin); err != nil {
					t.Fatal(err)
				}
				sum := sha256.Sum256(data)
				pin.Revision.Bytes, pin.Revision.SHA256 = len(data), hex.EncodeToString(sum[:])
				q.SourcePin, _ = json.Marshal(pin)
				want = watch.ErrInvalid
			case "fingerprint":
				want = detective.ErrConflict
			}
			b.Queries["shipping/assignment"] = q
			if mode != "environment" {
				claim = sealEndpointCloudBundle(t, &b)
			}
			if mode == "fingerprint" {
				claim.Definition.SourceFingerprint = "older-admission"
			}
			run, _, err := ExecuteCloudWatchCheck(context.Background(), project, "cloud-boundary", claim, b, bindings)
			if err == nil || run.ExecutionStatus == "completed" || want != nil && !errors.Is(err, want) {
				t.Fatal("untrusted source produced a business result", mode, run, err, want)
			}
		})
	}
}

func TestCloudWatchOVDBRequiresExactOperatorOriginAndUsesAcceptedQuery(t *testing.T) {
	project := cloudCoverageLocalSession(t)
	b, _ := endpointCloudBundle(t)
	q := b.Queries["shipping/assignment"]
	var definition datatug.QueryDefWithFolderPath
	if err := json.Unmarshal(q.JSON, &definition); err != nil {
		t.Fatal(err)
	}
	definition.ConnectionID = ""
	definition.Federation = &datatug.QueryFederation{OVDBBaseURL: "https://approved.example", Tables: []datatug.QueryFederationTable{{Database: "northwind", Name: "orders"}}}
	q.SourcePin, q.SourceIdentity, b.Artifacts = nil, nil, nil
	q.JSON, _ = json.Marshal(definition)
	b.Queries["shipping/assignment"] = q
	b.Sources = []watchdaemon.CloudBundleSource{{ID: "ovdb-grant", Kind: "ovdb", Origin: "https://approved.example", Database: "northwind", Collection: "orders"}}
	claim := sealEndpointCloudBundle(t, &b)
	old := detectiveComputeRunQueryWithOptions
	defer func() { detectiveComputeRunQueryWithOptions = old }()
	calls := 0
	detectiveComputeRunQueryWithOptions = func(_ context.Context, req apicontract.ExecutionRequest, options runQueryOptions) (apicontract.Result, error) {
		calls++
		if options.acceptedQuery == nil || options.expectedQueryRevision != q.Revision || options.acceptedQuery.Federation.OVDBBaseURL != "https://approved.example" || req.QueryID != "shipping/assignment" {
			t.Fatal("immutable query admission was replaced by a local asset", req, options)
		}
		return apicontract.Result{Recordset: apicontract.Recordset{Columns: []apicontract.Column{{Name: "assignment_id", Type: "string"}}, Rows: [][]apicontract.TypedValue{{apicontract.NewStringValue("assignment-11045")}}}}, nil
	}
	if _, _, err := ExecuteCloudWatchCheck(context.Background(), project, "wrong-origin", claim, b, map[string]string{"ovdb-grant": "https://other.example"}); !errors.Is(err, watch.ErrForbidden) || calls != 0 {
		t.Fatal("operator origin was widened", err, calls)
	}
	run, fingerprint, err := ExecuteCloudWatchCheck(context.Background(), project, "admitted-origin", claim, b, map[string]string{"ovdb-grant": "https://approved.example"})
	// The fake query transport intentionally provides no retained execution
	// receipt. Its valid row-count result must remain unknown overall until
	// ordinary evidence metadata can be verified.
	if err != nil || run.ExecutionStatus != "error" || run.Outcome != "unknown" || fingerprint != b.SourceRevision || calls != 1 {
		t.Fatal(run, fingerprint, calls, err)
	}
}

func TestCloudWatchArtifactOverrideRetainsOrdinaryLocalAuthorization(t *testing.T) {
	oldResolve, oldTargets := resolveSourceHook, eligibleTargetsHook
	defer func() { resolveSourceHook, eligibleTargetsHook = oldResolve, oldTargets }()
	local := api.ResolvedSource{Kind: api.SourceKindSQL, URL: "sqlite:///operator-source.sqlite"}
	ctx := context.WithValue(context.Background(), watchCloudSourcesKey{}, map[string]watchCloudSource{"query": {LocalConnection: "local", ArtifactURL: "sqlite:///admitted-image.sqlite"}})
	req := apicontract.ExecutionRequest{QueryID: "query", Environment: "training"}
	query := &datatug.QueryDef{Type: datatug.QueryTypeSQL}
	for _, mode := range []string{"unaccepted", "nil-query", "http-query", "source-fails", "admission-fails", "not-eligible", "wrong-kind", "wrong-scheme", "bad-url", "allowed", "ordinary"} {
		t.Run(mode, func(t *testing.T) {
			candidate := local
			resolveSourceHook = func(context.Context, datatug.ProjectStore, string, string, string) (api.ResolvedSource, error) {
				return candidate, nil
			}
			eligibleTargetsHook = func(context.Context, datatug.ProjectStore, string, string, *datatug.QueryDef) ([]api.ResolvedSource, error) {
				return []api.ResolvedSource{candidate}, nil
			}
			requestCtx, accepted, q := ctx, true, query
			switch mode {
			case "unaccepted":
				accepted = false
			case "nil-query":
				q = nil
			case "http-query":
				q = &datatug.QueryDef{Type: datatug.QueryTypeHTTP}
			case "source-fails":
				resolveSourceHook = func(context.Context, datatug.ProjectStore, string, string, string) (api.ResolvedSource, error) {
					return api.ResolvedSource{}, errors.New("offline")
				}
			case "admission-fails":
				eligibleTargetsHook = func(context.Context, datatug.ProjectStore, string, string, *datatug.QueryDef) ([]api.ResolvedSource, error) {
					return nil, errors.New("policy unavailable")
				}
			case "not-eligible":
				eligibleTargetsHook = func(context.Context, datatug.ProjectStore, string, string, *datatug.QueryDef) ([]api.ResolvedSource, error) {
					return nil, nil
				}
			case "wrong-kind":
				candidate.Kind = "http"
			case "wrong-scheme":
				candidate.URL = "postgres://operator/source"
			case "bad-url":
				candidate.URL = "%"
			case "ordinary":
				requestCtx = context.Background()
			}
			got, err := resolveWatchExecutionSource(requestCtx, nil, "", req, q, accepted)
			if mode == "allowed" || mode == "ordinary" {
				expected := "sqlite:///admitted-image.sqlite"
				if mode == "ordinary" {
					expected = local.URL
				}
				if err != nil || got.URL != expected {
					t.Fatal("ordinary source permission or artifact routing lost", got, err)
				}
			} else if err == nil || got.URL != "" {
				t.Fatal("artifact bypassed operator authorization", mode, got, err)
			}
		})
	}
}
