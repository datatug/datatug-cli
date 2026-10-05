package api

import (
	"context"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
)

// The driver and the host of a db server are the folder and the file name its file is
// kept under, so every entry that takes a server reference checks them (and the project)
// before the store is asked (see validateDbServer), and none says what the store said
// when it fails (see itemNotFound).

// serverShown is a server's ID as it is named in an answer: its driver, host and port,
// which the checks of the entry have shown to be names a project can record.
func serverShown(server datatug.ServerRef) string {
	return server.GetID()
}

// AddDbServer adds db server to project
func AddDbServer(ctx context.Context, ref dto.ProjectRef, projDbServer datatug.ProjDbServer) error {
	return saveDbServer(ctx, ref, projDbServer)
}

// UpdateDbServer adds db server to project
//
//goland:noinspection GoUnusedExportedFunction
func UpdateDbServer(ctx context.Context, ref dto.ProjectRef, projDbServer datatug.ProjDbServer) error {
	return saveDbServer(ctx, ref, projDbServer)
}

func saveDbServer(ctx context.Context, ref dto.ProjectRef, projDbServer datatug.ProjDbServer) error {
	if err := ValidateProjectIdentifier("project", ref.ProjectID); err != nil {
		return err
	}
	if err := validateProjDbServer(projDbServer); err != nil {
		return err
	}
	store, err := projectStoreForID(ref.StoreID, ref.ProjectID)
	if err != nil {
		return err
	}
	if err = store.DbServersStore(projDbServer.Server.Driver).SaveProjDbServer(ctx, &projDbServer); err != nil {
		return itemWriteFailed("save", "db server", serverShown(projDbServer.Server), err)
	}
	return nil
}

// DeleteDbServer adds db server to project
func DeleteDbServer(ctx context.Context, ref dto.ProjectRef, dbServer datatug.ServerRef) (err error) {
	if err = ValidateProjectIdentifier("project", ref.ProjectID); err != nil {
		return err
	}
	if err = validateDbServer(dbServer); err != nil {
		return err
	}
	store, err := projectStoreForID(ref.StoreID, ref.ProjectID)
	if err != nil {
		return err
	}
	if err = store.DbServersStore(dbServer.Driver).DeleteProjDbServer(ctx, dbServer.GetID()); err != nil {
		return itemWriteFailed("delete", "db server", serverShown(dbServer), err)
	}
	return nil
}

// GetDbServerSummary returns summary on DB server
func GetDbServerSummary(ctx context.Context, ref dto.ProjectRef, dbServer datatug.ServerRef) (*datatug.ProjDbServer, error) {
	if err := ValidateProjectIdentifier("project", ref.ProjectID); err != nil {
		return nil, err
	}
	if err := validateDbServer(dbServer); err != nil {
		return nil, err
	}
	store, err := projectStoreForID(ref.StoreID, ref.ProjectID)
	if err != nil {
		return nil, err
	}
	summary, err := store.DbServersStore(dbServer.Driver).LoadProjDbServer(ctx, dbServer.GetID())
	if err != nil {
		return nil, itemNotFound("db server", serverShown(dbServer), err)
	}
	return summary, nil
}
