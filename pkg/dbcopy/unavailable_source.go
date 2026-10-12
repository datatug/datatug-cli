package dbcopy

import "errors"

// UnavailableSource returns the error of this package, found in err or in what err wraps, that says why a source
// cannot be opened or read: a fixed refusal (the preview is off, a read through policies, a URL that turns the
// read-only session off or names a service file), a classified failure of an open (BackendRef.OpenFailure), the
// missing file of a file source, or a connection of a PostgreSQL source that was lost and could not be made again. Each
// says it in a sentence built from nothing the person typed but the display form of the source (a PostgreSQL source
// has none in its text: the adapter's sentence and where its connection string is read from), so a route can show its
// text to a client and answer "source unavailable". It is nil when err holds none of them.
//
// A caller shows the text of the error returned and not the text of err: err can be a wrapper that adds text of its
// own, and a driver can wrap one of these errors around its own text.
func UnavailableSource(err error) error {
	var (
		refused *refusedError
		failed  *openError
		missing *missingSourceFileError
		lost    *postgresConnectionError
	)
	switch {
	case errors.As(err, &refused):
		return refused
	case errors.As(err, &failed):
		return failed
	case errors.As(err, &missing):
		return missing
	case errors.As(err, &lost):
		return lost
	}
	return nil
}
