package api

import (
	"context"
	"log"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
)

// WarnMissingSourceFiles logs one warning line per environment source
// (SQL/inGitDB catalog) whose file-backed path does not exist, for every
// project in pathsByID. `datatug serve --project` used to surface this only
// as a raw 500 on the first request that happened to touch the missing
// source (S77's finding: a fresh checkout with no prior `datatug demo` run
// has no ~/datatug/dbs/chinook-local.sqlite) — this gives the operator the
// same signal at startup, before the browser does. Best-effort: a project or
// environment this cannot enumerate is skipped silently rather than failing
// serve's startup over a diagnostic.
func WarnMissingSourceFiles(ctx context.Context, pathsByID map[string]string) {
	for projectID, projectDir := range pathsByID {
		projStore, err := ProjectStoreFor(projectID)
		if err != nil {
			continue
		}
		environments, err := projStore.LoadEnvironments(ctx)
		if err != nil {
			continue
		}
		for _, env := range environments {
			warnMissingSourceFilesForEnvironment(ctx, projStore, projectID, projectDir, env)
		}
	}
}

func warnMissingSourceFilesForEnvironment(ctx context.Context, projStore datatug.ProjectStore, projectID, projectDir string, env *datatug.Environment) {
	if env == nil {
		return
	}
	sources, err := ListSources(ctx, projStore, projectDir, env.ID)
	if err != nil {
		return
	}
	for _, source := range sources {
		if source.Kind != SourceKindSQL && source.Kind != SourceKindInGitDB {
			continue
		}
		ref, err := dbcopy.Parse(source.URL)
		if err != nil {
			continue
		}
		if err := dbcopy.CheckSourceFile(ref.Path); err != nil {
			log.Printf("serve: WARNING: project %q environment %q source %q: %v", projectID, env.ID, source.ID, err)
		}
	}
}
