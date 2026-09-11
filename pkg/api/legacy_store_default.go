//go:build !datatug_query_capture

package api

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
