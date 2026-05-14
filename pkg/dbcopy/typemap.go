// Package dbcopy implements the cross-engine database-copy primitive
// for `datatug db copy`. This file holds the type-mapping table between
// the MVP backends (SQLite via dalgo2sqlite and inGitDB via
// dalgo2ingitdb).
//
// Coverage bar: every column type appearing in the canonical Chinook
// fixture MUST map cleanly in both directions. Types outside that
// closed set return a *UnsupportedTypeError naming the source type and
// target backend.
//
// Mapping policy for the MVP:
//   - Both backends speak the engine-neutral dbschema.Type vocabulary,
//     so the translation is identity for every type currently produced
//     by either driver's SchemaReader.
//   - Chinook's *.db SQLite fixture contains columns of SQLite affinity
//     INTEGER, NVARCHAR/TEXT, NUMERIC(10,2), and DATETIME. As of
//     dalgo2sqlite v0.0.0-20260513182736-6886f34af097 the driver's
//     DescribeCollection rejects NUMERIC(p,s) and DATETIME columns
//     (driver gap upstream of dbcopy). For the columns that DO
//     describe successfully (Album, Artist, Customer, Genre, MediaType,
//     Playlist, PlaylistTrack), only dbschema.Int and dbschema.String
//     are observed. The MVP type-map declares the full dbschema
//     vocabulary supported (Bool, Int, Float, String, Bytes, Time,
//     Decimal) so the coverage holds the day dalgo2sqlite ships
//     DATETIME / NUMERIC support and Track/Invoice/InvoiceLine/Employee
//     describe successfully.
//   - dbschema.Null is intentionally rejected: it is the zero value
//     used for "unset" FieldDef.Type and never a meaningful column
//     type for cross-engine copy.
package dbcopy

import (
	"fmt"

	"github.com/dal-go/dalgo/dbschema"
)

// Supported backend identifiers accepted by MapType.
const (
	BackendSQLite  = "sqlite"
	BackendInGitDB = "ingitdb"
)

// MapType translates a column type from sourceBackend to targetBackend.
// The returned Type is ready for use in a target ddl.CreateCollection
// call. For the MVP, sourceBackend and targetBackend MUST each be one
// of "sqlite" or "ingitdb". Types outside the Chinook coverage set
// (dbschema.Null and any unrecognized Type value) return a
// *UnsupportedTypeError naming the source type and target backend.
//
// Both backends accept the same engine-neutral dbschema.Type
// vocabulary, so this function is the identity for every supported
// type today. The function exists as the seam where engine-specific
// widening / narrowing rules will land if and when the type-mapping
// matrix needs them.
func MapType(t dbschema.Type, sourceBackend, targetBackend string) (dbschema.Type, error) {
	if !isSupportedBackend(sourceBackend) {
		return dbschema.Null, fmt.Errorf("dbcopy: unsupported source backend %q (want one of: %s, %s)", sourceBackend, BackendSQLite, BackendInGitDB)
	}
	if !isSupportedBackend(targetBackend) {
		return dbschema.Null, fmt.Errorf("dbcopy: unsupported target backend %q (want one of: %s, %s)", targetBackend, BackendSQLite, BackendInGitDB)
	}
	switch t {
	case dbschema.Bool,
		dbschema.Int,
		dbschema.Float,
		dbschema.String,
		dbschema.Bytes,
		dbschema.Time,
		dbschema.Decimal:
		return t, nil
	default:
		return dbschema.Null, &UnsupportedTypeError{
			SourceType:    t,
			TargetBackend: targetBackend,
		}
	}
}

// isSupportedBackend reports whether name is one of the MVP backends.
func isSupportedBackend(name string) bool {
	return name == BackendSQLite || name == BackendInGitDB
}

// UnsupportedTypeError is returned by MapType when the source column
// type is outside the MVP coverage set.
type UnsupportedTypeError struct {
	// SourceType is the dbschema.Type that could not be mapped.
	SourceType dbschema.Type
	// TargetBackend is the backend name supplied to MapType.
	TargetBackend string
}

// Error implements the error interface. The message names both the
// source type and the target backend so the operator can act.
func (e *UnsupportedTypeError) Error() string {
	return fmt.Sprintf("dbcopy: unsupported column type %q for target backend %q", e.SourceType.String(), e.TargetBackend)
}
