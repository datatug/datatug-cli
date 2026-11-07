package api

import (
	"context"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/policyscope"
)

// InstanceTarget is the instance a read of catalog in environment opens, for
// local rule selectors (backstage spec/features/perimeter,
// REQ:local-rule-selectors), named exactly as the project stores them, with
// the environment's kind. A name that matches no stored environment or
// catalog exactly — a case variant a case-insensitive disk would still
// open, for one — gives a target with no instance, which every policy with
// selectors denies. So does a store that cannot be read.
func InstanceTarget(ctx context.Context, store datatug.ProjectStore, environment, catalog string) policyscope.Target {
	envs, err := store.LoadEnvironments(ctx)
	if err != nil {
		return policyscope.Target{}
	}
	for _, env := range envs {
		if env == nil || env.ID != environment {
			continue
		}
		catalogs, err := store.LoadEnvDbCatalogs(ctx, env.ID)
		if err != nil {
			return policyscope.Target{}
		}
		for _, c := range catalogs {
			if c != nil && c.ID == catalog {
				return policyscope.Target{Environment: env.ID, Catalog: c.ID, Kind: env.Kind}
			}
		}
	}
	return policyscope.Target{}
}
