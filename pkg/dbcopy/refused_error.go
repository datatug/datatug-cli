package dbcopy

// refusedError is an error this package built from a fixed sentence that holds
// nothing of any source: BackendRef.OpenFailure recognises it by its type and passes
// it through as it is, where any other error is a driver's and is replaced by a
// sentence of OpenFailure's own.
type refusedError struct{ message string }

func (e *refusedError) Error() string { return e.message }

var (
	// errPostgresReadOnlyOff is what a read of a PostgreSQL source whose URL turns
	// the read-only session off is refused with. It names the parameter, not the URL.
	errPostgresReadOnlyOff error = &refusedError{"the PostgreSQL URL turns the read-only session off (default_transaction_read_only): a read of a PostgreSQL source keeps it on, and only `datatug db copy --to` opens a PostgreSQL target for writing"}
)
