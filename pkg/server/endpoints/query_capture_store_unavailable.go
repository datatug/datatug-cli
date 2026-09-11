//go:build !datatug_query_capture

package endpoints

import "github.com/datatug/datatug-core/pkg/datatug"

// adaptRevisionedStore, in the default build, never finds a revisioned
// store: the datatug-core release go.mod pins predates
// RevisionedQueriesStore. queries/capture therefore fails closed with
// errCaptureStoreUnavailable (500) rather than falling back to the
// unconditional, unrevisioned legacy SaveQuery.
//
// To wire the real store, build with -tags datatug_query_capture against a
// datatug-core that has the revisioned store and the capture contract
// (query_capture_store_revisioned.go). Once go.mod moves to such a release,
// delete this file and drop that build tag everywhere.
func adaptRevisionedStore(datatug.ProjectStore) (queryCaptureStore, error) {
	return nil, errCaptureStoreUnavailable
}
