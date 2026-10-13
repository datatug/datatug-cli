package endpoints

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-core/pkg/datatug"
	core "github.com/datatug/datatug-core/pkg/detective"
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
	"github.com/datatug/datatug-core/pkg/investigation"
)

// detectiveAISnapshot is rebuilt from authorized local project/catalog reads
// for generation and every subsequent review or confirmation. The agent's
// Detective grant is deliberately project-wide and currently owner-only.
func detectiveAISnapshot(ctx context.Context, project string, store *detective.LocalStore) (ai.Snapshot, error) {
	state, err := store.State(ctx)
	if err != nil {
		return ai.Snapshot{}, err
	}
	return detectiveAISnapshotFromState(ctx, project, state)
}

func detectiveAISnapshotFromState(ctx context.Context, project string, state detective.State) (ai.Snapshot, error) {
	environments, err := detectiveEnvironments(ctx, project)
	if err != nil {
		return ai.Snapshot{}, err
	}
	dir, ok := api.ProjectDir(project)
	if !ok {
		return ai.Snapshot{}, detective.ErrNotFound
	}
	queries, canonicalIDs, err := loadModuleQueries(dir)
	if err != nil {
		return ai.Snapshot{}, err
	}
	if len(queries) > 200 || len(state.Checks) > 200 || len(state.Validators) > 200 || len(state.Scenarios) > 200 || len(state.Cases) > 200 || len(state.Runs) > 200 {
		return ai.Snapshot{}, detective.ErrCapacity
	}
	projectStore, err := api.ProjectStoreFor(project)
	if err != nil {
		return ai.Snapshot{}, err
	}
	revisioned, ok := projectStore.(datatug.RevisionedQueriesStore)
	if !ok {
		return ai.Snapshot{}, detective.ErrInvalid
	}
	snapshot := ai.Snapshot{Environments: environments}
	for _, query := range queries {
		id := canonicalIDs[query]
		stored, err := revisioned.LoadQueryRevision(ctx, id)
		if err != nil || stored == nil {
			return ai.Snapshot{}, detective.ErrInvalid
		}
		resource := ai.Resource{ID: id, Title: query.Title, Revision: string(stored.Revision)}
		if resource.Title == "" {
			resource.Title = id
		}
		for _, parameter := range query.Parameters {
			cardinality := "scalar"
			if parameter.IsMultiValue {
				cardinality = "set"
			}
			resource.Inputs = append(resource.Inputs, core.Input{Name: parameter.ID, Type: investigation.ValueType(parameter.Type), Cardinality: cardinality, Required: parameter.IsRequired})
		}
		snapshot.Queries = append(snapshot.Queries, resource)
	}
	for _, item := range state.Checks {
		snapshot.Checks = append(snapshot.Checks, ai.Resource{ID: item.ID, Title: item.Title, Revision: strconv.FormatInt(item.Version, 10), Inputs: item.Inputs})
	}
	for _, item := range state.Validators {
		snapshot.Validators = append(snapshot.Validators, ai.Resource{ID: item.ID, Title: item.Title, Revision: strconv.FormatInt(item.Version, 10)})
	}
	for _, item := range state.Scenarios {
		snapshot.Scenarios = append(snapshot.Scenarios, ai.Resource{ID: item.ID, Title: item.Title, Revision: strconv.FormatInt(item.Version, 10), Inputs: item.Inputs})
	}
	for _, item := range state.Cases {
		snapshot.Cases = append(snapshot.Cases, ai.Resource{ID: item.ID, Title: item.Title, Revision: strconv.FormatInt(item.Version, 10)})
	}
	for _, item := range state.Runs {
		snapshot.Runs = append(snapshot.Runs, ai.Resource{ID: item.ID, Title: item.CheckID, Revision: item.StartedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")})
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return ai.Snapshot{}, err
	}
	sum := sha256.Sum256(raw)
	snapshot.CatalogRevision = hex.EncodeToString(sum[:])
	return snapshot, nil
}
