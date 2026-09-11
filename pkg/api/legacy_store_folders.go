//go:build datatug_query_capture

package api

import (
	"errors"

	"github.com/datatug/datatug-cli/pkg/querywrite"
	"github.com/datatug/datatug-core/pkg/datatug"
)

// This build compiles only against a datatug-core whose filestore resolves
// a query's folder (the Phase 2 task 2 storage: SaveQuery honours
// FolderPath, and every write refuses a symlinked or non-directory folder).
// The blank declaration makes the build tag fail to compile against a core
// without that store, so the tag can never claim a capability the store
// lacks.
var _ datatug.RevisionedQueriesStore

// legacyStoreResolvesFolders reports whether the project store the legacy
// query routes write through puts a query in the folder its request names.
// See legacy_store_default.go.
const legacyStoreResolvesFolders = true

// storeLocationRefusal reports the store's refusal of a query location
// (datatug.InvalidQueryLocationError: a symlinked, non-directory or
// unreadable folder found on disk) as the fixed message a 400 may carry,
// naming the offending segment only - never the store's reason text,
// which can hold an absolute server path and OS error text.
func storeLocationRefusal(err error) (message string, ok bool) {
	var location *datatug.InvalidQueryLocationError
	if !errors.As(err, &location) {
		return "", false
	}
	return querywrite.LocationMessage(location.FolderPath, location.ID, location.Reason), true
}
