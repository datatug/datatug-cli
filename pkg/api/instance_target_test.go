package api

import (
	"context"
	"errors"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/policyscope"
)

// instanceStore is a project store holding only environments and catalogs.
type instanceStore struct {
	datatug.ProjectStore
	envs        datatug.Environments
	catalogs    datatug.DbCatalogs
	envErr, err error
}

func (s instanceStore) LoadEnvironments(context.Context, ...datatug.StoreOption) (datatug.Environments, error) {
	return s.envs, s.envErr
}

func (s instanceStore) LoadEnvDbCatalogs(context.Context, string, ...datatug.StoreOption) (datatug.DbCatalogs, error) {
	return s.catalogs, s.err
}

func TestInstanceTargetNamesTheStoredInstance(t *testing.T) {
	prod := &datatug.Environment{Kind: datatug.EnvironmentKindProduction}
	prod.ID = "factory-e-prod"
	other := &datatug.Environment{}
	other.ID = "factory-a-prod"
	factory := &datatug.DbCatalog{}
	factory.ID = "factory"
	store := instanceStore{envs: datatug.Environments{nil, other, prod}, catalogs: datatug.DbCatalogs{nil, factory}}
	ctx := context.Background()
	want := policyscope.Target{Environment: "factory-e-prod", Catalog: "factory", Kind: datatug.EnvironmentKindProduction}
	if got := InstanceTarget(ctx, store, "factory-e-prod", "factory"); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	unknown := map[string]struct {
		store                instanceStore
		environment, catalog string
	}{
		"environment case":  {store, "Factory-E-Prod", "factory"},
		"catalog case":      {store, "factory-e-prod", "FACTORY"},
		"no such catalog":   {store, "factory-e-prod", "erp"},
		"environments fail": {instanceStore{envErr: errors.New("x")}, "factory-e-prod", "factory"},
		"catalogs fail":     {instanceStore{envs: store.envs, err: errors.New("x")}, "factory-e-prod", "factory"},
	}
	for name, c := range unknown {
		if got := InstanceTarget(ctx, c.store, c.environment, c.catalog); got.Instance() != "" {
			t.Errorf("%s: got %+v, want no instance", name, got)
		}
	}
}
