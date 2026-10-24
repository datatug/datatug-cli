package watchdaemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"

	"github.com/datatug/datatug-cli/pkg/watch"
	"github.com/datatug/datatug-core/pkg/detective"
)

// CloudRunnerConfig is operator-owned. The endpoint remains pinned by the
// enclosing runner deployment; no cloud token is sent to a project manifest URL.
// Native bindings name local connections; OVDB bindings name the exact approved
// origin. Neither bundle nor coordinator receives monitored credentials.
type CloudRunnerConfig struct {
	SpaceID        string            `json:"spaceID"`
	ProjectID      string            `json:"projectID"`
	SourceBindings map[string]string `json:"sourceBindings"`
}
type CloudBundleQuery struct {
	Revision       string               `json:"revision"`
	JSON           json.RawMessage      `json:"json"`
	SourcePin      json.RawMessage      `json:"sourcePin,omitempty"`
	SourceIdentity *CloudSourceIdentity `json:"sourceIdentity,omitempty"`
}
type CloudSourceIdentity struct {
	SourceID        string `json:"sourceId"`
	Environment     string `json:"environment"`
	BindingRevision int64  `json:"bindingRevision"`
	Generation      string `json:"generation"`
	FixtureCommit   string `json:"fixtureCommit"`
	FixtureSHA256   string `json:"fixtureSHA256"`
	SchemaSHA256    string `json:"schemaSHA256"`
}

// CloudSourcePin contains only fields needed to verify the exact shipping
// artifact. Other backend revision metadata remains covered by BundleDigest.
type CloudSourcePin struct {
	SpaceID         string `json:"spaceID"`
	ProjectID       string `json:"projectId"`
	SourceID        string `json:"sourceId"`
	Environment     string `json:"environment"`
	BindingRevision int64  `json:"bindingRevision"`
	Revision        struct {
		Generation    string `json:"generation"`
		Revision      int    `json:"revision"`
		SHA256        string `json:"sha256"`
		Bytes         int    `json:"bytes"`
		FixtureCommit string `json:"fixtureCommit"`
		FixtureSHA256 string `json:"fixtureSHA256"`
		SchemaSHA256  string `json:"schemaSHA256"`
	} `json:"revision"`
}
type CloudBundleSource struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Origin     string `json:"origin,omitempty"`
	Database   string `json:"database,omitempty"`
	Collection string `json:"collection,omitempty"`
	SourceID   string `json:"sourceId,omitempty"`
}
type CloudBundleDependency struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Revision string `json:"revision"`
}

// CloudExecutionBundle is the versioned wire contract shared with the hosted
// facade. Core re-verifies the transitive catalog before evaluation.
type CloudExecutionBundle struct {
	Contract       string                      `json:"contract"`
	Scope          watch.Scope                 `json:"scope"`
	CheckID        string                      `json:"checkId"`
	CheckVersion   string                      `json:"checkVersion"`
	SourceRevision string                      `json:"sourceRevision"`
	BundleDigest   string                      `json:"bundleDigest"`
	Catalog        detective.Catalog           `json:"catalog"`
	Queries        map[string]CloudBundleQuery `json:"queries"`
	Sources        []CloudBundleSource         `json:"sources"`
	Dependencies   []CloudBundleDependency     `json:"dependencies"`
	Artifacts      map[string][]byte           `json:"artifacts,omitempty"`
}

func (c Client) Bundle(ctx context.Context, claim watch.Claim) (CloudExecutionBundle, error) {
	var result struct {
		Bundle CloudExecutionBundle `json:"bundle"`
	}
	err := c.call(ctx, "runner/bundle", struct {
		ClaimID string `json:"claimId"`
		Fence   int64  `json:"fence"`
	}{claim.RunID, claim.Fence}, &result)
	if err != nil {
		return CloudExecutionBundle{}, err
	}
	if _, err = result.Bundle.VerifiedCatalog(ctx, claim); err != nil {
		return CloudExecutionBundle{}, err
	}
	return result.Bundle, nil
}
func (b CloudExecutionBundle) VerifiedCatalog(ctx context.Context, claim watch.Claim) (detective.Catalog, error) {
	d := claim.Definition
	if b.Contract != "watch-execution-bundle/1" || b.Scope != d.Scope || b.CheckID != d.CheckID || d.CheckVersion != "" && b.CheckVersion != d.CheckVersion || b.SourceRevision == "" || b.SourceRevision != d.SourceRevision || len(b.Queries) > 50 || len(b.Sources) > 50 {
		return detective.Catalog{}, watch.ErrForbidden
	}
	expected := b.BundleDigest
	b.BundleDigest = ""
	raw, err := json.Marshal(b)
	if err != nil || len(raw) > 1<<20 {
		return detective.Catalog{}, watch.ErrInvalid
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != expected {
		return detective.Catalog{}, watch.ErrConflict
	}
	semantic := b
	semantic.SourceRevision, semantic.Artifacts = "", nil
	semantic.Queries = make(map[string]CloudBundleQuery, len(b.Queries))
	used := map[string]bool{}
	for id, query := range b.Queries {
		if query.SourceIdentity != nil {
			if _, err := b.ShippingArtifact(query); err != nil {
				return detective.Catalog{}, err
			}
			used[query.SourceIdentity.SourceID] = true
		} else if len(query.SourcePin) != 0 {
			return detective.Catalog{}, watch.ErrInvalid
		}
		query.SourcePin = nil
		semantic.Queries[id] = query
	}
	if len(used) != len(b.Artifacts) || len(used) > 1 {
		return detective.Catalog{}, watch.ErrInvalid
	}
	raw, err = json.Marshal(semantic)
	if err != nil || len(raw) > 256<<10 {
		return detective.Catalog{}, watch.ErrInvalid
	}
	sum = sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != b.SourceRevision {
		return detective.Catalog{}, watch.ErrConflict
	}
	catalog, err := detective.LoadCatalog(ctx, b.CheckID, "", cloudBundleCatalogReader{b})
	if err != nil {
		return detective.Catalog{}, watch.ErrInvalid
	}
	if strconv.FormatInt(catalog.Checks[b.CheckID].Version, 10) != b.CheckVersion {
		return detective.Catalog{}, watch.ErrConflict
	}
	return catalog, nil
}

func (b CloudExecutionBundle) ShippingArtifact(query CloudBundleQuery) ([]byte, error) {
	var pin CloudSourcePin
	if json.Unmarshal(query.SourcePin, &pin) != nil || query.SourceIdentity == nil {
		return nil, watch.ErrInvalid
	}
	identity := CloudSourceIdentity{pin.SourceID, pin.Environment, pin.BindingRevision, pin.Revision.Generation, pin.Revision.FixtureCommit, pin.Revision.FixtureSHA256, pin.Revision.SchemaSHA256}
	if pin.SpaceID != b.Scope.Namespace || pin.ProjectID != b.Scope.Project || pin.Environment != b.Scope.Environment || pin.SourceID != "shipping-working-copy" || identity != *query.SourceIdentity || pin.Revision.Generation == "" || pin.Revision.Revision < 1 {
		return nil, watch.ErrForbidden
	}
	data := b.Artifacts[pin.SourceID]
	if len(data) == 0 || len(data) > 512<<10 || len(data) != pin.Revision.Bytes {
		return nil, watch.ErrInvalid
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != pin.Revision.SHA256 {
		return nil, watch.ErrConflict
	}
	return data, nil
}

type cloudBundleCatalogReader struct{ bundle CloudExecutionBundle }

func (r cloudBundleCatalogReader) GetCheck(_ context.Context, id string) (detective.Check, error) {
	v, ok := r.bundle.Catalog.Checks[id]
	if !ok {
		return v, detective.ErrNotFound
	}
	return v, nil
}
func (r cloudBundleCatalogReader) GetPublishedValidator(_ context.Context, id string) (detective.PublishedValidator, error) {
	v, ok := r.bundle.Catalog.Validators[id]
	if !ok {
		return v, detective.ErrNotFound
	}
	return v, nil
}
func (r cloudBundleCatalogReader) GetQueryValidators(_ context.Context, id string) (detective.QueryValidators, bool, error) {
	v, ok := r.bundle.Catalog.QueryValidators[id]
	return v, ok, nil
}
func (r cloudBundleCatalogReader) QueryExists(_ context.Context, id string) (bool, error) {
	q, ok := r.bundle.Queries[id]
	return ok && q.Revision != "" && len(q.JSON) > 0, nil
}
