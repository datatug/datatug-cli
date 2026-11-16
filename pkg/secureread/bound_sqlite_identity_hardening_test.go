package secureread

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/record"
)

type nativeHardeningDriverFault struct{}

func (*nativeHardeningDriverFault) Error() string { return "SECRET-DB-CREDENTIAL=owned-test-secret" }

type nativeHardeningListingFailure struct {
	dbschema.SchemaReader
	fault error
}

func (r nativeHardeningListingFailure) ListCollections(context.Context, *record.Key) ([]dal.CollectionRef, error) {
	return nil, r.fault
}

func TestBoundNativeMetadataErrorsRedactedWithCause(t *testing.T) {
	for _, stage := range []string{"listing", "query", "scan", "iteration"} {
		t.Run(stage, func(t *testing.T) {
			leaf, calls := identityTestLeaf(t)
			if leaf.ref.Scheme != "sqlite" || leaf.ref.Path == "" {
				t.Fatal("validated source reference not retained")
			}
			original := queryBoundIdentityRows
			t.Cleanup(func() { queryBoundIdentityRows = original })
			fault := &nativeHardeningDriverFault{}
			switch stage {
			case "listing":
				leaf.listing = nativeHardeningListingFailure{SchemaReader: leaf.listing, fault: fault}
			case "query":
				queryBoundIdentityRows = func(context.Context, *sql.DB) (boundIdentityRows, error) { return nil, fault }
			case "scan":
				queryBoundIdentityRows = func(context.Context, *sql.DB) (boundIdentityRows, error) { return &identityFaultRows{scan: fault}, nil }
			case "iteration":
				queryBoundIdentityRows = func(context.Context, *sql.DB) (boundIdentityRows, error) {
					return &identityFaultRows{iteration: fault}, nil
				}
			}
			check := func(err error) {
				t.Helper()
				if err == nil || !errors.Is(err, fault) {
					t.Fatal("metadata failure lost original cause", err)
				}
				var typed *nativeHardeningDriverFault
				if !errors.As(err, &typed) || typed != fault {
					t.Fatal("metadata failure lost errors.As cause", err)
				}
				if strings.Contains(err.Error(), "SECRET-DB-CREDENTIAL") || strings.Contains(err.Error(), "owned-test-secret") {
					t.Fatal("driver text escaped", err)
				}
				if calls.records != 0 || calls.recordsets != 0 {
					t.Fatal("metadata failure dispatched data", calls.records, calls.recordsets)
				}
			}
			reader, err := leaf.ExecuteQueryToRecordsReader(context.Background(), identityQuery("A"))
			if reader != nil {
				t.Fatal("reader returned on failed admission")
			}
			check(err)
			recordsetReader, err := leaf.ExecuteQueryToRecordsetReader(context.Background(), identityQuery("A"))
			if recordsetReader != nil {
				t.Fatal("recordset returned on failed admission")
			}
			check(err)
		})
	}
}
func TestBoundNativeTransactionsRefusedWithoutWorker(t *testing.T) {
	leaf, calls := identityTestLeaf(t)
	invoked := false
	if err := leaf.RunReadonlyTransaction(context.Background(), func(context.Context, dal.ReadTransaction) error { invoked = true; return nil }); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatal("readonly transaction accepted", err)
	}
	if err := leaf.RunReadwriteTransaction(context.Background(), func(context.Context, dal.ReadwriteTransaction) error { invoked = true; return nil }); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatal("readwrite transaction accepted", err)
	}
	if invoked || calls.records != 0 || calls.recordsets != 0 {
		t.Fatal("transaction reached worker or data", invoked, calls.records, calls.recordsets)
	}
	// The wrapper still owns and closes the native handle, rather than exposing
	// its embedded adapter as a bypass after transaction refusal.
	if err := leaf.Close(); err != nil {
		t.Fatal(err)
	}
	if reader, err := leaf.ExecuteQueryToRecordsReader(context.Background(), identityQuery("A")); err == nil || reader != nil {
		t.Fatal("closed wrapper admitted read", err)
	}
}
