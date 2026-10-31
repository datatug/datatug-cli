package dbcopy

import (
	"context"
	"errors"

	"github.com/dal-go/dalgo/dal"
)

// ErrPostgresReadOnlyUnverified refuses native work when the actual transaction
// cannot prove its mode. It contains no source URL, credential or SQL values.
var ErrPostgresReadOnlyUnverified = errors.New("PostgreSQL read-only transaction could not be verified")

// RunVerifiedPostgresReadonly verifies the actual PostgreSQL transaction before
// delegating work. In particular, a coordinator that silently falls back to a
// read-write transaction never gets to execute worker. This is a native mode
// assertion, not a permission decision or a credential-privilege attestation.
// The coordinator still owns commit/rollback; worker errors are preserved.
func RunVerifiedPostgresReadonly(ctx context.Context, coordinator dal.ReadTransactionCoordinator, worker dal.ROTxWorker) error {
	if ctx == nil || coordinator == nil || worker == nil {
		return ErrPostgresReadOnlyUnverified
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return coordinator.RunReadonlyTransaction(ctx, func(ctx context.Context, tx dal.ReadTransaction) error {
		if tx == nil {
			return ErrPostgresReadOnlyUnverified
		}
		reader, err := tx.ExecuteQueryToRecordsReader(ctx, dal.NewTextQuery("SHOW transaction_read_only", nil))
		if err != nil {
			if reader != nil {
				_ = reader.Close()
			}
			return ErrPostgresReadOnlyUnverified
		}
		if reader == nil {
			return ErrPostgresReadOnlyUnverified
		}
		// Close exactly once, including every malformed/failed verification path.
		verified := func() bool {
			row, err := reader.Next()
			if err != nil || row == nil || row.Error() != nil {
				return false
			}
			values, ok := row.Data().(map[string]any)
			if !ok || len(values) != 1 || values["transaction_read_only"] != "on" {
				return false
			}
			_, err = reader.Next()
			return errors.Is(err, dal.ErrNoMoreRecords)
		}()
		closeErr := reader.Close()
		if !verified || closeErr != nil {
			return ErrPostgresReadOnlyUnverified
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return worker(ctx, tx)
	})
}
