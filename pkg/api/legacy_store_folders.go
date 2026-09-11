//go:build datatug_query_capture

package api

import "github.com/datatug/datatug-core/pkg/datatug"

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
