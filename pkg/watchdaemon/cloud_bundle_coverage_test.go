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

func cloudCoverageTransport(t *testing.T, b *CloudExecutionBundle) {
	t.Helper()
	b.BundleDigest = ""
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	b.BundleDigest = hex.EncodeToString(sum[:])
}

func TestCloudBundleRejectsMalformedOversizedAndObsoleteGraphs(t *testing.T) {
	for _, mode := range []string{"malformed-json", "transport-limit", "semantic-limit", "pin-without-identity", "bad-identity", "semantic-mismatch", "missing-check", "version-mismatch", "missing-query"} {
		t.Run(mode, func(t *testing.T) {
			b, claim := cloudTestBundle(t)
			want := watch.ErrInvalid
			switch mode {
			case "malformed-json":
				b.Queries["query"] = CloudBundleQuery{JSON: json.RawMessage(`!`)}
			case "transport-limit", "semantic-limit":
				size := 300 << 10
				if mode == "transport-limit" {
					size = 1 << 20
				}
				b.Queries["query"] = CloudBundleQuery{Revision: "revision", JSON: json.RawMessage(`{"pad":"` + strings.Repeat("x", size) + `"}`)}
				claim = sealCloudTestBundle(t, &b)
			case "pin-without-identity":
				q := b.Queries["query"]
				q.SourcePin = json.RawMessage(`{}`)
				b.Queries["query"] = q
				claim = sealCloudTestBundle(t, &b)
			case "bad-identity":
				q := b.Queries["query"]
				q.SourceIdentity = &CloudSourceIdentity{SourceID: "untrusted"}
				b.Queries["query"] = q
				claim = sealCloudTestBundle(t, &b)
			case "semantic-mismatch":
				b.SourceRevision = "unmatched-semantic-revision"
				claim.Definition.SourceRevision = b.SourceRevision
				cloudCoverageTransport(t, &b)
				want = watch.ErrConflict
			case "missing-check":
				delete(b.Catalog.Checks, b.CheckID)
				claim = sealCloudTestBundle(t, &b)
			case "version-mismatch":
				b.CheckVersion = "2"
				claim = sealCloudTestBundle(t, &b)
				want = watch.ErrConflict
			case "missing-query":
				delete(b.Queries, "query")
				claim = sealCloudTestBundle(t, &b)
			}
			if _, err := b.VerifiedCatalog(context.Background(), claim); !errors.Is(err, want) {
				t.Fatal(mode, err, want)
			}
		})
	}
}

func TestCloudBundleArtifactChecksScopeIdentityBytesAndDigest(t *testing.T) {
	for _, mode := range []string{"json", "identity", "scope", "size", "hash"} {
		t.Run(mode, func(t *testing.T) {
			b, _ := cloudTestBundle(t)
			data := []byte("bounded immutable bytes")
			sum := sha256.Sum256(data)
			pin := CloudSourcePin{SpaceID: b.Scope.Namespace, ProjectID: b.Scope.Project, Environment: b.Scope.Environment, SourceID: "shipping-working-copy", BindingRevision: 1}
			pin.Revision.Generation, pin.Revision.Revision, pin.Revision.Bytes, pin.Revision.SHA256 = "g", 1, len(data), hex.EncodeToString(sum[:])
			q := CloudBundleQuery{SourceIdentity: &CloudSourceIdentity{SourceID: pin.SourceID, Environment: pin.Environment, BindingRevision: 1, Generation: "g"}}
			b.Artifacts = map[string][]byte{pin.SourceID: data}
			want := watch.ErrInvalid
			switch mode {
			case "identity":
				q.SourceIdentity = nil
			case "scope":
				pin.SpaceID = "other-space"
				want = watch.ErrForbidden
			case "size":
				pin.Revision.Bytes++
			case "hash":
				pin.Revision.SHA256 = strings.Repeat("0", 64)
				want = watch.ErrConflict
			}
			q.SourcePin, _ = json.Marshal(pin)
			if mode == "json" {
				q.SourcePin = json.RawMessage(`!`)
			}
			if _, err := b.ShippingArtifact(q); !errors.Is(err, want) {
				t.Fatal(err, want)
			}
		})
	}
}

func TestCloudBundleReadersAndHTTPFailurePreserveAbsence(t *testing.T) {
	b, claim := cloudTestBundle(t)
	b.Catalog.Validators = map[string]detective.PublishedValidator{"validator": {ID: "validator"}}
	b.Catalog.QueryValidators = map[string]detective.QueryValidators{"query": {QueryID: "query"}}
	r := cloudBundleCatalogReader{b}
	if _, err := r.GetCheck(context.Background(), "absent"); !errors.Is(err, detective.ErrNotFound) {
		t.Fatal(err)
	}
	if v, err := r.GetPublishedValidator(context.Background(), "validator"); err != nil || v.ID != "validator" {
		t.Fatal(v, err)
	}
	if _, err := r.GetPublishedValidator(context.Background(), "absent"); !errors.Is(err, detective.ErrNotFound) {
		t.Fatal(err)
	}
	if _, ok, err := r.GetQueryValidators(context.Background(), "query"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	for _, mode := range []string{"unavailable", "invalid-bundle"} {
		t.Run(mode, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if mode == "unavailable" {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"bundle": CloudExecutionBundle{}})
			}))
			defer s.Close()
			got, err := (Client{Endpoint: s.URL, Token: strings.Repeat("x", 32)}).Bundle(context.Background(), claim)
			if err == nil || got.Contract != "" {
				t.Fatal("unverified bundle escaped", got, err)
			}
		})
	}
}
