package endpoints

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/watch"
	"github.com/datatug/datatug-cli/pkg/watchdaemon"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	core "github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
)

// ExecuteCloudWatchCheck evaluates an immutable hosted graph using the existing
// Core evaluator and accepted-query runner. Local source bindings are explicit
// operator grants and remain subject to the ordinary secured execution path.
func ExecuteCloudWatchCheck(ctx context.Context, localProject, operationID string, claim watch.Claim, bundle watchdaemon.CloudExecutionBundle, bindings map[string]string) (core.CheckRun, string, error) {
	return executeCloudWatchCheck(ctx, localProject, operationID, claim, bundle, bindings, json.Marshal)
}

// A private per-operation codec keeps encoding failure handling testable without
// mutable global hooks or any caller-configurable execution authority.
func executeCloudWatchCheck(ctx context.Context, localProject, operationID string, claim watch.Claim, bundle watchdaemon.CloudExecutionBundle, bindings map[string]string, encode func(any) ([]byte, error)) (core.CheckRun, string, error) {
	if api.ValidateIdentifier("environment", claim.Definition.Environment) != nil {
		return core.CheckRun{}, "", watch.ErrInvalid
	}
	if _, _, err := detectiveStore(localProject, api.SecurityContextID()); err != nil {
		return core.CheckRun{}, "", watch.ErrForbidden
	}
	catalog, err := bundle.VerifiedCatalog(ctx, claim)
	if err != nil {
		return core.CheckRun{}, "", err
	}
	_, ok := api.ProjectDir(localProject)
	if !ok {
		return core.CheckRun{}, "", watch.ErrForbidden
	}
	sources := map[string]watchdaemon.CloudBundleSource{}
	for _, source := range bundle.Sources {
		if source.ID == "" || bindings[source.ID] == "" {
			return core.CheckRun{}, "", watch.ErrForbidden
		}
		sources[source.ID] = source
	}
	accepted := map[string]sessioncontext.DetectiveAcceptedQueryPin{}
	overrides := map[string]watchCloudSource{}
	tempDir := ""
	defer func() {
		if tempDir != "" {
			_ = os.RemoveAll(tempDir)
		}
	}()
	for id, pin := range bundle.Queries {
		var q datatug.QueryDefWithFolderPath
		var e error
		if json.Unmarshal(pin.JSON, &q) != nil || q.QueryDef.Validate() != nil || len(q.Targets) != 0 {
			return core.CheckRun{}, "", watch.ErrInvalid
		}
		if q.Federation != nil {
			for _, table := range q.Federation.Tables {
				admitted := false
				for _, source := range sources {
					if source.Kind == "ovdb" && source.Origin == q.Federation.OVDBBaseURL && source.Database == table.Database && source.Collection == table.Name && bindings[source.ID] == source.Origin {
						admitted = true
					}
				}
				if !admitted {
					return core.CheckRun{}, "", watch.ErrForbidden
				}
			}
		} else {
			localConnection := ""
			for _, source := range sources {
				if source.Kind == "native" && source.SourceID == q.ConnectionID {
					localConnection = bindings[source.ID]
				}
			}
			if api.ValidateIdentifier("connection", localConnection) != nil {
				return core.CheckRun{}, "", watch.ErrForbidden
			}
			if q.Type != datatug.QueryTypeSQL || pin.SourceIdentity == nil || pin.SourceIdentity.SourceID != q.ConnectionID {
				return core.CheckRun{}, "", watch.ErrForbidden
			}
			if tempDir == "" {
				tempDir, e = os.MkdirTemp("", "datatug-watch-artifact-")
				if e != nil {
					return core.CheckRun{}, "", e
				}
			}
			artifactPath := filepath.Join(tempDir, "shipping.sqlite")
			if e = prepareCloudWatchArtifact(ctx, artifactPath, bundle, pin); e != nil {
				return core.CheckRun{}, "", e
			}
			overrides[id] = watchCloudSource{localConnection, (&url.URL{Scheme: "sqlite", Path: artifactPath}).String()}
			q.ConnectionID = localConnection
		}
		body, e := encode(q)
		if e != nil {
			return core.CheckRun{}, "", watch.ErrInvalid
		}
		accepted[id] = sessioncontext.DetectiveAcceptedQueryPin{QueryID: id, Revision: pin.Revision, DefinitionJSON: body}
	}
	// Baseline identity is the admitted logical source/configuration. Operator
	// aliases, paths and unrelated grants do not change Watch semantics.
	fingerprint := bundle.SourceRevision
	if expected := claim.Definition.SourceFingerprint; expected != "" && expected != fingerprint {
		return core.CheckRun{}, fingerprint, detective.ErrConflict
	}
	ctx = context.WithValue(detective.WithWatchAcceptedQueries(ctx, accepted), watchCloudSourcesKey{}, overrides)
	run, err := core.EvaluateCheck(ctx, core.RunRequest{ID: operationID, CheckID: bundle.CheckID, Environment: claim.Definition.Environment, Actor: api.SecurePrincipalID(), Inputs: claim.Definition.Inputs}, catalog, detectiveQueryRunner(localProject, api.SecurityContextID()), detectiveExecutionMetadata(localProject))
	return run, fingerprint, err
}

func prepareCloudWatchArtifact(ctx context.Context, path string, bundle watchdaemon.CloudExecutionBundle, query watchdaemon.CloudBundleQuery) error {
	data, err := bundle.ShippingArtifact(query)
	if err != nil {
		return err
	}
	return prepareWatchArtifact(ctx, path, data)
}

// This package-private key is never populated from an HTTP request. Ordinary
// source resolution/authorization must succeed before the override is applied.
type watchCloudSourcesKey struct{}

type watchCloudSource struct{ LocalConnection, ArtifactURL string }

func resolveWatchExecutionSource(ctx context.Context, store datatug.ProjectStore, dir string, req apicontract.ExecutionRequest, query *datatug.QueryDef, accepted bool) (api.ResolvedSource, error) {
	paths, _ := ctx.Value(watchCloudSourcesKey{}).(map[string]watchCloudSource)
	binding, override := paths[req.QueryID]
	if !override {
		return resolveExecutionSource(ctx, store, dir, req, query)
	}
	if !accepted || query == nil || query.Type != datatug.QueryTypeSQL {
		return api.ResolvedSource{}, watch.ErrForbidden
	}
	// Resolve the operator's alias through the ordinary project source registry
	// and ensure that the same catalog is an eligible query target.
	local, err := resolveSourceHook(ctx, store, dir, req.Environment, binding.LocalConnection)
	if err != nil {
		return api.ResolvedSource{}, newSourceUnavailable("cloud local source is unavailable")
	}
	eligible, err := eligibleTargetsHook(ctx, store, dir, req.Environment, query)
	if err != nil {
		return api.ResolvedSource{}, newSourceUnavailable("cloud local source admission is unavailable")
	}
	allowed := false
	for _, source := range eligible {
		allowed = allowed || source.URL == local.URL && source.Kind == local.Kind
	}
	original, err := url.Parse(local.URL)
	if !allowed || err != nil || local.Kind != api.SourceKindSQL || (strings.ToLower(original.Scheme) != "sqlite3" && strings.ToLower(original.Scheme) != "sqlite") {
		return api.ResolvedSource{}, newSourceUnavailable("cloud source binding does not authorize SQLite")
	}
	local.URL = binding.ArtifactURL
	return local, nil
}

func prepareWatchArtifact(ctx context.Context, path string, data []byte) error {
	return prepareWatchArtifactWithOpen(ctx, path, data, sql.Open)
}

func prepareWatchArtifactWithOpen(ctx context.Context, path string, data []byte, open func(string, string) (*sql.DB, error)) error {
	if !bytes.HasPrefix(data, []byte("SQLite format 3\x00")) {
		return watch.ErrInvalid
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	// Validate the downloaded image before the Check engine can run a query.
	db, err := open("sqlite", "file:"+path+"?mode=ro&immutable=1")
	if err != nil {
		return watch.ErrInvalid
	}
	defer func() { _ = db.Close() }()
	var result string
	if db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result) != nil || result != "ok" {
		return watch.ErrInvalid
	}
	return nil
}
