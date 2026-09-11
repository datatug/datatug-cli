//go:build !datatug_query_capture

package api

import (
	"context"

	"github.com/datatug/datatug-core/pkg/datatug"
)

// legacyStoreResolvesFolders reports whether the project store the legacy
// queries/create_query, update_query and delete_query routes write through
// puts a query in the folder its request names.
//
// The datatug-core release go.mod pins does not. Its SaveQuery ignores
// FolderPath and writes queries/<id>.query.json whatever folder was asked
// for, and its DeleteQuery joins the folder into a path without refusing a
// symlinked folder. Authorizing "x/revenue" while the store overwrote the
// root "revenue" is how a deny rule on the root query was bypassed. So this
// build fails closed: the legacy routes accept root queries only, the one
// location whose file the authorized resource names exactly, and refuse a
// folder with 400 rather than write somewhere the caller did not ask for.
const legacyStoreResolvesFolders = false

// storeLocationRefusal never finds one in this build: the pinned store has
// no typed location error, so its failures stay failures (500).
func storeLocationRefusal(error) (message string, ok bool) {
	return "", false
}

// refuseClientCapture has nothing to refuse in this build: the pinned
// datatug-core's QueryDef has no capture field, so a request's "capture"
// block is dropped when the body decodes and is never stored.
func refuseClientCapture(legacyWriteMode, *datatug.QueryDefWithFolderPath) error {
	return nil
}

// writeLegacyQuery is the pinned store's unconditional SaveQuery. That
// store knows no capture provenance: it writes the query's JSON from the
// decoded QueryDef, so a "capture" member that another build wrote into
// the stored file is not kept.
func writeLegacyQuery(ctx context.Context, store datatug.ProjectStore, _ string, query *datatug.QueryDefWithFolderPath) error {
	return store.SaveQuery(ctx, query)
}
