package secureread

import (
	"errors"

	"github.com/dal-go/dalgo/access"
)

// ErrAccessDenied is access.ErrAccessDenied, re-exported so callers can
// check a secureread error with errors.Is(err, secureread.ErrAccessDenied)
// without importing the access package themselves. It is the exact same
// sentinel value dal-go/dalgo/access uses, not a wrapped copy.
var ErrAccessDenied = access.ErrAccessDenied

// ErrNoPrincipal is returned by NewSession when the session is secured
// (NoPolicies is unset) but no principal was named via --as/--role/--group.
// REQ:principal-selection requires `datatug serve` to fix a principal for
// the whole session; constructing a secured Session with nobody named would
// let every request quietly fall through to each policy's own default
// (usually deny) with no caller-visible identity to blame or audit against.
// An Unrestricted session (--no-policies) may omit a principal since
// nothing is enforced.
var ErrNoPrincipal = errors.New("secureread: no principal named; pass --as, --role or --group, or run Unrestricted")

// ErrNativeSQLUnsupported is returned by RunNativeSQL for a source scheme
// with no native SQL-text execution surface in this repository yet. See
// RunNativeSQL's doc comment for the exact schemes this covers today.
var ErrNativeSQLUnsupported = errors.New("secureread: native SQL is not supported for this source")
