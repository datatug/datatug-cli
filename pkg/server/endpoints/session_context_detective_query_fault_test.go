package endpoints

import (
	"context"
	"errors"
	"github.com/datatug/datatug-core/pkg/datatug"
	"testing"
)

type nativeUnrevisionedProject struct{ datatug.ProjectStore }
type nativeRevisionedProject struct {
	datatug.ProjectStore
	datatug.RevisionedQueriesStore
	failure error
}

func (s nativeRevisionedProject) LoadQueryRevision(context.Context, string, ...datatug.StoreOption) (*datatug.StoredQuery, error) {
	if s.failure != nil {
		return nil, s.failure
	}
	return &datatug.StoredQuery{Revision: "exact-revision"}, nil
}
func TestNativeQuerySnapshotRequiresRevisionedAtomicRecord(t *testing.T) {
	oldStore, oldEncode := sessionDetectiveProjectStore, sessionDetectiveEncode
	t.Cleanup(func() { sessionDetectiveProjectStore, sessionDetectiveEncode = oldStore, oldEncode })
	sentinel := errors.New("private record unavailable")
	for _, kind := range []string{"project", "unrevisioned", "record", "encode", "success"} {
		t.Run(kind, func(t *testing.T) {
			sessionDetectiveEncode = oldEncode
			sessionDetectiveProjectStore = func(string) (datatug.ProjectStore, error) {
				switch kind {
				case "project":
					return nil, sentinel
				case "unrevisioned":
					return nativeUnrevisionedProject{}, nil
				case "record":
					return nativeRevisionedProject{failure: sentinel}, nil
				}
				return nativeRevisionedProject{}, nil
			}
			if kind == "encode" {
				sessionDetectiveEncode = func(any) ([]byte, error) { return nil, sentinel }
			}
			query, e := sessionDetectiveQuerySnapshot("project")(context.Background(), "folder/query")
			if kind == "success" {
				if e != nil || query.QueryID != "folder/query" || query.Revision != "exact-revision" {
					t.Fatal(query, e)
				}
			} else if e == nil {
				t.Fatal("non-atomic query accepted")
			}
		})
	}
}
