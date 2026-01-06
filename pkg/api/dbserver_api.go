package api

import (
	"context"

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/datatug-core/dto"
	"github.com/datatug/datatug-cli/pkg/datatug-core/storage"
	"github.com/strongo/validation"
)

// AddDbServer adds db server to project
func AddDbServer(ctx context.Context, ref dto.ProjectRef, projDbServer datatug.ProjDbServer) error {
	store, err := storage.GetProjectStore(ctx, ref.StoreID, ref.ProjectID)
	if err != nil {
		return err
	}
	return store.DbServersStore(projDbServer.Server.Driver).SaveProjDbServer(ctx, &projDbServer)
}

// UpdateDbServer adds db server to project
//
//goland:noinspection GoUnusedExportedFunction
func UpdateDbServer(ctx context.Context, ref dto.ProjectRef, projDbServer datatug.ProjDbServer) error {
	store, err := storage.GetProjectStore(ctx, ref.StoreID, ref.ProjectID)
	if err != nil {
		return err
	}
	return store.DbServersStore(projDbServer.Server.Driver).SaveProjDbServer(ctx, &projDbServer)
}

// DeleteDbServer adds db server to project
func DeleteDbServer(ctx context.Context, ref dto.ProjectRef, dbServer datatug.ServerRef) (err error) {
	store, err := storage.GetProjectStore(ctx, ref.StoreID, ref.ProjectID)
	if err != nil {
		return err
	}
	return store.DbServersStore(dbServer.Driver).DeleteProjDbServer(ctx, dbServer.GetID())
}

// GetDbServerSummary returns summary on DB server
func GetDbServerSummary(ctx context.Context, ref dto.ProjectRef, dbServer datatug.ServerRef) (*datatug.ProjDbServer, error) {
	if err := dbServer.Validate(); err != nil {
		err = validation.NewBadRequestError(err)
		return nil, err
	}
	store, err := storage.GetProjectStore(ctx, ref.StoreID, ref.ProjectID)
	if err != nil {
		return nil, err
	}
	return store.DbServersStore(dbServer.Driver).LoadProjDbServer(ctx, dbServer.GetID())
}
