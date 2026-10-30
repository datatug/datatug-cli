package watchdaemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/watch"
	"github.com/datatug/datatug-core/pkg/detective"
)

func sealCloudTestBundle(t *testing.T, b *CloudExecutionBundle) watch.Claim {
	t.Helper()
	b.SourceRevision, b.BundleDigest = "", ""
	semantic := *b
	semantic.Artifacts = nil
	semantic.Queries = map[string]CloudBundleQuery{}
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
	return watch.Claim{RunID: "run", Fence: 1, Definition: watch.Definition{Scope: b.Scope, CheckID: b.CheckID, CheckVersion: b.CheckVersion, SourceRevision: b.SourceRevision}}
}
func cloudTestBundle(t *testing.T) (CloudExecutionBundle, watch.Claim) {
	t.Helper()
	b := CloudExecutionBundle{Contract: "watch-execution-bundle/1", Scope: watch.Scope{Namespace: "space", Project: "project", Environment: "training"}, CheckID: "check", CheckVersion: "1", Catalog: detective.Catalog{Checks: map[string]detective.Check{"check": {ID: "check", Title: "Check", Version: 1, Steps: []detective.Step{{ID: "read", Kind: "query", QueryID: "query"}}}}}, Queries: map[string]CloudBundleQuery{"query": {Revision: "revision", JSON: json.RawMessage(`{"id":"query"}`)}}}
	return b, sealCloudTestBundle(t, &b)
}
func TestCloudBundleVerifiesSemanticAndTransportDigests(t *testing.T) {
	b, claim := cloudTestBundle(t)
	if _, err := b.VerifiedCatalog(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	b.BundleDigest = "wrong"
	if _, err := b.VerifiedCatalog(context.Background(), claim); !errors.Is(err, watch.ErrConflict) {
		t.Fatal(err)
	}
	b, claim = cloudTestBundle(t)
	b.Queries["query"] = CloudBundleQuery{Revision: "changed", JSON: json.RawMessage(`{"id":"query"}`)}
	if _, err := b.VerifiedCatalog(context.Background(), claim); !errors.Is(err, watch.ErrConflict) {
		t.Fatal(err)
	}
	b, claim = cloudTestBundle(t)
	claim.Definition.SourceRevision = "foreign"
	if _, err := b.VerifiedCatalog(context.Background(), claim); !errors.Is(err, watch.ErrForbidden) {
		t.Fatal(err)
	}
}
func TestCloudBundleArtifactRequiresExactPinAndNoUnrequestedData(t *testing.T) {
	b, _ := cloudTestBundle(t)
	data := []byte("immutable bounded artifact")
	sum := sha256.Sum256(data)
	var pin CloudSourcePin
	pin.SpaceID, pin.ProjectID, pin.Environment, pin.SourceID, pin.BindingRevision = "space", "project", "training", "shipping-working-copy", 1
	pin.Revision.Generation, pin.Revision.Revision, pin.Revision.Bytes, pin.Revision.SHA256 = "generation", 1, len(data), hex.EncodeToString(sum[:])
	identity := CloudSourceIdentity{SourceID: pin.SourceID, Environment: pin.Environment, BindingRevision: 1, Generation: "generation"}
	raw, _ := json.Marshal(pin)
	b.Queries["query"] = CloudBundleQuery{Revision: "revision", JSON: json.RawMessage(`{"id":"query"}`), SourcePin: raw, SourceIdentity: &identity}
	b.Artifacts = map[string][]byte{pin.SourceID: data}
	claim := sealCloudTestBundle(t, &b)
	if _, err := b.VerifiedCatalog(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	b.Artifacts[pin.SourceID] = []byte("modified")
	claim = sealCloudTestBundle(t, &b)
	if _, err := b.VerifiedCatalog(context.Background(), claim); !errors.Is(err, watch.ErrInvalid) {
		t.Fatal(err)
	}
	b.Artifacts[pin.SourceID] = data
	b.Artifacts["unrequested"] = data
	claim = sealCloudTestBundle(t, &b)
	if _, err := b.VerifiedCatalog(context.Background(), claim); !errors.Is(err, watch.ErrInvalid) {
		t.Fatal(err)
	}
}
func TestCloudBundleHTTPUsesOnlyClaimAndAuthenticatedEndpoint(t *testing.T) {
	b, claim := cloudTestBundle(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/watch/runner/bundle" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("x", 32) {
			t.Error("wrong authenticated route")
		}
		var request struct {
			ClaimID string `json:"claimId"`
			Fence   int64  `json:"fence"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.ClaimID != claim.RunID || request.Fence != claim.Fence {
			t.Error("wrong claim")
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"bundle": b}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	if _, err := (Client{Endpoint: server.URL, Token: strings.Repeat("x", 32)}).Bundle(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
}
