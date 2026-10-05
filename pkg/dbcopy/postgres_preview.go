package dbcopy

import "os"

// PostgresPreviewEnv is the environment variable that turns PostgreSQL sources on.
// A postgres:// source is opened only while it is "1"; any other value, and a
// variable that is not set, leaves them off. The scan (`datatug scan -D postgres`)
// is not behind it: it reads the catalog only.
//
// The switch is a preview: it stays until the whole path of a PostgreSQL source has
// had its security review, and a later change removes it. Removing it is deleting
// CheckPostgresPreview and its calls (BackendRef.openPostgres, and CheckPostgresRead
// while that exists).
const PostgresPreviewEnv = "DATATUG_PREVIEW_POSTGRES"

// refusedError is an error this package built from a fixed sentence that holds
// nothing of any source: BackendRef.OpenFailure recognises it by its type and passes
// it through as it is, where any other error is a driver's and is replaced by a
// sentence of OpenFailure's own.
type refusedError struct{ message string }

func (e *refusedError) Error() string { return e.message }

var (
	// ErrPostgresPreview is what every path that would open a PostgreSQL source
	// answers while the preview switch is off: one fixed sentence that says the sources
	// are a preview and names the variable. Nothing is parsed further and nothing is
	// dialled.
	ErrPostgresPreview error = &refusedError{"PostgreSQL sources are a preview and are switched off: set " + PostgresPreviewEnv + "=1 to use them"}

	// ErrPostgresPolicyReads is what a read of a PostgreSQL source that goes through
	// one or more access policies answers, before the source is opened (see
	// CheckPostgresRead).
	ErrPostgresPolicyReads error = &refusedError{"policy-enforced reads on PostgreSQL sources are not available in this preview"}

	// errPostgresReadOnlyOff is what a read of a PostgreSQL source whose URL turns
	// the read-only session off is refused with. It names the parameter, not the URL.
	errPostgresReadOnlyOff error = &refusedError{"the PostgreSQL URL turns the read-only session off (default_transaction_read_only): a read of a PostgreSQL source keeps it on, and only `datatug db copy --to` opens a PostgreSQL target for writing"}
)

// CheckPostgresPreview is the one function that decides whether a PostgreSQL
// source may be opened: nil while PostgresPreviewEnv is "1", ErrPostgresPreview
// otherwise.
func CheckPostgresPreview() error {
	if os.Getenv(PostgresPreviewEnv) == "1" {
		return nil
	}
	return ErrPostgresPreview
}

// CheckPostgresRead says why a read of ref, through policies access policies, cannot
// run in this preview, or nil when it can. A source that is not a PostgreSQL one is
// not its business. The preview switch is asked first, so that a person whose
// switch is off is told about the switch and not about a refusal that turning it on
// would not lift; then a read through one or more policies is refused. It is called
// before the source is opened, by every path that reads through policies.
func CheckPostgresRead(ref BackendRef, policies int) error {
	if ref.Scheme != "postgres" {
		return nil
	}
	if err := CheckPostgresPreview(); err != nil {
		return err
	}
	if policies > 0 {
		return ErrPostgresPolicyReads
	}
	return nil
}
