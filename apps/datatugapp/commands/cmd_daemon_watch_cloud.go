package commands

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/server/endpoints"
	"github.com/datatug/datatug-cli/pkg/watch"
	"github.com/datatug/datatug-cli/pkg/watchdaemon"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

func configureCloudWatchRunner(cfg *watchdaemon.Config, override, namespace string) (*watchdaemon.Runner, func(), error) {
	cloud := cfg.Runner.Cloud
	if cloud == nil || !validCloudWatchID(cloud.SpaceID) || !validCloudWatchID(cloud.ProjectID) || api.ValidateIdentifier("environment", cfg.Runner.Environment) != nil || cfg.Runner.Principal == "" || len(cloud.SourceBindings) == 0 || len(cloud.SourceBindings) > 50 {
		return nil, nil, errors.New("cloud Watch runner requires an explicit project, environment, local principal and source bindings")
	}
	if cfg.Namespace != "" && cfg.Namespace != cloud.SpaceID || namespace != "" && namespace != cloud.SpaceID {
		return nil, nil, errors.New("cloud Watch namespace differs from trusted Space")
	}
	cfg.Namespace = cloud.SpaceID
	endpoint := cfg.Runner.CoordinatorEndpoint
	if override != "" {
		endpoint = override
	}
	parsed, err := url.Parse(endpoint)
	expected := "/v0/datatug/watch/" + url.PathEscape(cloud.SpaceID) + "/" + url.PathEscape(cloud.ProjectID) + "/" + url.PathEscape(cfg.Runner.Environment)
	if err != nil || watchdaemon.ValidateEndpoint(endpoint) != nil || parsed.Path != expected || parsed.RawPath != "" {
		return nil, nil, errors.New("cloud Watch endpoint does not match its trusted project scope")
	}
	for id, binding := range cloud.SourceBindings {
		if len(id) != 64 || binding == "" {
			return nil, nil, errors.New("cloud Watch source bindings are invalid")
		}
		for _, c := range id {
			if c < '0' || c > '9' {
				if c < 'a' || c > 'f' {
					return nil, nil, errors.New("cloud Watch source bindings are invalid")
				}
			}
		}
	}
	project, err := filestore.LoadProjectFile(cfg.Runner.ProjectDir)
	if err != nil {
		return nil, nil, errors.New("cloud Watch local source project is unavailable")
	}
	cleanup, err := watchConfigureExecution(*cfg, project.ID)
	if err != nil {
		return nil, nil, err
	}
	token := os.Getenv(cfg.Runner.TokenEnv)
	if len(token) < 32 {
		cleanup()
		return nil, nil, errors.New("cloud Watch enrollment credential is unavailable")
	}
	client := watchdaemon.Client{Endpoint: strings.TrimRight(endpoint, "/"), Token: token}
	runner := &watchdaemon.Runner{Client: client, Telegram: cfg.Runner.Telegram}
	runner.Executor = watchdaemon.ExecutorFunc(func(ctx context.Context, claim watch.Claim) (watch.Completion, error) {
		d := claim.Definition
		if d.Namespace != cloud.SpaceID || d.Project != cloud.ProjectID || d.Environment != cfg.Runner.Environment {
			return watch.Completion{}, watch.ErrForbidden
		}
		start := time.Now()
		bundle, e := client.Bundle(ctx, claim)
		if e != nil {
			return watch.Completion{}, e
		}
		operationID := "watch-" + claim.RunID + fmt.Sprintf("-%d", claim.Fence)
		run, fingerprint, e := endpoints.ExecuteCloudWatchCheck(ctx, project.ID, operationID, claim, bundle, cloud.SourceBindings)
		completion := watch.Completion{ExecutionStatus: run.ExecutionStatus, Outcome: run.Outcome, Severity: run.Severity, DurationMS: time.Since(start).Milliseconds(), CheckRunRef: run.ID, SourceFingerprint: fingerprint, SourceRevision: d.SourceRevision}
		if errors.Is(e, detective.ErrConflict) {
			completion.ExecutionStatus = "error"
			completion.Outcome = "unknown"
			completion.FailureCode = "SOURCE_CHANGED"
			return completion, nil
		}
		return completion, e
	})
	return runner, cleanup, nil
}

func validCloudWatchID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}
