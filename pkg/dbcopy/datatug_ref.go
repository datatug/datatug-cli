package dbcopy

import (
	"errors"
	"strings"
)

// DatatugScheme is the scheme of a reference to a database that is a source of a DataTug project:
// datatug://<project>/<db>[?env=<ENV>]. It is resolved by the commands from the project's own
// catalog (pkg/api), never by Parse: the reference holds no path, URL or credential, and it is
// what a message shows for the source.
const DatatugScheme = "datatug://"

// DatatugRef is a parsed reference to a source of a project. Project is empty for the current or the
// only registered project (datatug:///<db>); Env is empty when the database id names one environment.
type DatatugRef struct {
	Project string
	DB      string
	Env     string
}

// IsDatatugRef is whether raw is written as a datatug:// reference (it may still be a malformed one).
func IsDatatugRef(raw string) bool {
	return len(raw) >= len(DatatugScheme) && strings.EqualFold(raw[:len(DatatugScheme)], DatatugScheme)
}

// errDatatugRef is the one answer for a reference that does not follow the grammar. It quotes none of
// the reference.
var errDatatugRef = errors.New("a datatug:// reference is datatug://<project>/<db> or datatug:///<db> for the current project, with an optional ?env=<ENV>; each name is a plain name (letters, digits, '.', '_' and '-')")

// ParseDatatugRef reads datatug://<project>/<db>[?env=<ENV>]. The project may be empty; the database id
// is required; the only query parameter is env; every name is a plain name (IsPlainSourceID). No
// error quotes the reference.
func ParseDatatugRef(raw string) (DatatugRef, error) {
	if !IsDatatugRef(raw) {
		return DatatugRef{}, errDatatugRef
	}
	rest := raw[len(DatatugScheme):]
	var ref DatatugRef
	if before, query, hasQuery := strings.Cut(rest, "?"); hasQuery {
		env, ok := strings.CutPrefix(query, "env=")
		if !ok || !IsPlainSourceID(env) {
			return DatatugRef{}, errDatatugRef
		}
		ref.Env, rest = env, before
	}
	project, db, hasDB := strings.Cut(rest, "/")
	if !hasDB || !IsPlainSourceID(db) || (project != "" && !IsPlainSourceID(project)) {
		return DatatugRef{}, errDatatugRef
	}
	ref.Project, ref.DB = project, db
	return ref, nil
}

// String is the reference as it is written.
func (r DatatugRef) String() string {
	text := DatatugScheme + r.Project + "/" + r.DB
	if r.Env != "" {
		text += "?env=" + r.Env
	}
	return text
}
