// reload.go — schema-match validation and truncate path for
// `--overwrite=reload`.
//
// Implements REQ:reload-schema-match: before any TRUNCATE or INSERT, the
// target's schema for each source-named table must be a superset of the
// source's (every source column present, type-compatible per the MVP
// type-map; primary-key column SETS equal). Extra columns on the target
// are fine (REQ:reload-accepts-superset-target).
//
// Truncate is implemented as DELETE-via-Writer (per Task 5.5 of the
// db-copy MVP plan): DALgo lacks a portable Truncate op; we enumerate
// every record in the target collection and call tx.Delete on each
// within a single ReadwriteTransaction. Correctness over throughput
// for the MVP; per-engine optimizations are out of scope.

package dbcopy

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
)

// ReloadSchemaMismatchError signals that the target's schema for a
// given table is not a superset of the source's, per REQ:reload-schema-match.
//
// Column is either a column name (when a source column is missing or
// has an incompatible target type) or the literal "<primary key>" when
// the primary-key column sets differ.
//
// SourceValue and TargetValue describe what differed (e.g. type names
// or PK column lists) in human-readable form, for the stderr diff.
type ReloadSchemaMismatchError struct {
	Table        string
	Column       string
	SourceValue  string
	TargetValue  string
	Reason       string // short reason: "missing column", "type mismatch", "primary key mismatch"
}

func (e *ReloadSchemaMismatchError) Error() string {
	if e.SourceValue == "" && e.TargetValue == "" {
		return fmt.Sprintf("reload: schema mismatch on table %q column %q: %s",
			e.Table, e.Column, e.Reason)
	}
	return fmt.Sprintf(
		"reload: schema mismatch on table %q column %q: %s (source=%s, target=%s)",
		e.Table, e.Column, e.Reason, e.SourceValue, e.TargetValue)
}

// validateReloadSchema verifies that the target's schema for the
// source-named collection identified by ref is a superset of the
// source's, per REQ:reload-schema-match. On the FIRST mismatch it
// returns a *ReloadSchemaMismatchError.
//
// If the target collection does not exist at all, that too is a
// mismatch — reported as a "missing column" on the first source column
// with Column="<table>". (The Feature spec says the user resolves with
// --overwrite=recreate or by manually fixing the target schema; either
// way we must NOT auto-create on reload.)
func validateReloadSchema(
	ctx context.Context,
	source, target dal.DB,
	ref *dal.CollectionRef,
	sourceBackend, targetBackend string,
) error {
	srcDef, err := dbschema.DescribeCollection(ctx, source, ref)
	if err != nil {
		// Same policy as the rest of the engine: a source-side describe
		// failure is reported up; the worker pool decides whether to
		// skip or abort. For reload validation we abort — we can't
		// verify what we can't describe.
		return fmt.Errorf("reload: describe source %q: %w", ref.Name(), err)
	}

	tgtRef := dal.NewCollectionRef(ref.Name(), "", nil)
	tgtDef, err := dbschema.DescribeCollection(ctx, target, &tgtRef)
	if err != nil {
		// Treat any failure to describe the target as a mismatch — the
		// table either doesn't exist or isn't introspectable; either
		// way reload can't proceed safely. Report with the first
		// source column so the user has a starting point.
		firstCol := "<table>"
		if len(srcDef.Fields) > 0 {
			firstCol = string(srcDef.Fields[0].Name)
		}
		return &ReloadSchemaMismatchError{
			Table:  srcDef.Name,
			Column: firstCol,
			Reason: fmt.Sprintf("target table not describable: %v", err),
		}
	}

	// Build a lookup of target fields by name.
	tgtByName := make(map[dal.FieldName]dbschema.FieldDef, len(tgtDef.Fields))
	for _, f := range tgtDef.Fields {
		tgtByName[f.Name] = f
	}

	// 1. Every source column must exist on target with a compatible type.
	for _, sf := range srcDef.Fields {
		tf, ok := tgtByName[sf.Name]
		if !ok {
			return &ReloadSchemaMismatchError{
				Table:       srcDef.Name,
				Column:      string(sf.Name),
				SourceValue: sf.Type.String(),
				TargetValue: "(missing)",
				Reason:      "missing column on target",
			}
		}
		mappedSrc, err := MapType(sf.Type, sourceBackend, targetBackend)
		if err != nil {
			return fmt.Errorf("reload: cannot map source type for %q.%q: %w",
				srcDef.Name, sf.Name, err)
		}
		if !typesCompatibleForReload(mappedSrc, tf.Type, targetBackend) {
			return &ReloadSchemaMismatchError{
				Table:       srcDef.Name,
				Column:      string(sf.Name),
				SourceValue: mappedSrc.String(),
				TargetValue: tf.Type.String(),
				Reason:      "type mismatch",
			}
		}
	}

	// 2. PK column SETS must be equal — with one tolerated asymmetry:
	//    dalgo2ingitdb synthesizes PrimaryKey as the single name "$key"
	//    regardless of the underlying natural PK columns (see
	//    dalgo2ingitdb/schema_reader.go pkFieldName). When the target
	//    reports that synthesized key, we treat it as "matches whatever
	//    PK the source declares" — the asymmetry is in the introspection
	//    abstraction, not in the row data, and the row-copy path uses
	//    the source PK for key encoding either way.
	if !isInGitDBSyntheticPK(tgtDef.PrimaryKey) &&
		!sameFieldNameSet(srcDef.PrimaryKey, tgtDef.PrimaryKey) {
		return &ReloadSchemaMismatchError{
			Table:       srcDef.Name,
			Column:      "<primary key>",
			SourceValue: fieldNameList(srcDef.PrimaryKey),
			TargetValue: fieldNameList(tgtDef.PrimaryKey),
			Reason:      "primary key column set mismatch",
		}
	}

	return nil
}

// isInGitDBSyntheticPK reports whether pk is the dalgo2ingitdb-synthesized
// `[$key]` value used in CollectionDef.PrimaryKey regardless of the
// underlying natural PK.
func isInGitDBSyntheticPK(pk []dal.FieldName) bool {
	return len(pk) == 1 && pk[0] == "$key"
}

// typesCompatibleForReload reports whether a column of the
// already-MapType-translated source type is reload-compatible with the
// target's reported type, accounting for known target-side storage
// widenings:
//
//   - inGitDB stores Decimal as Float (lossy by design; see
//     dalgo2ingitdb/type_mapping.go), so a source-mapped Decimal that
//     describes back as Float on the target IS compatible.
//
// All other types must match exactly.
func typesCompatibleForReload(mappedSrc, target dbschema.Type, targetBackend string) bool {
	if mappedSrc == target {
		return true
	}
	if targetBackend == BackendInGitDB &&
		mappedSrc == dbschema.Decimal && target == dbschema.Float {
		return true
	}
	return false
}

// readAllRecordsForTruncate returns a RecordsReader for every record in
// the named collection on `db`. It first tries a StructuredQuery (the
// shape inGitDB requires) and falls back to a text `SELECT *` query for
// backends like dalgo2sql that only speak SQL text.
func readAllRecordsForTruncate(ctx context.Context, db dal.DB, name string) (dal.RecordsReader, error) {
	ref := dal.NewCollectionRef(name, "", nil)
	sq := dal.From(ref).NewQuery().SelectIntoRecordset()
	reader, err := db.ExecuteQueryToRecordsReader(ctx, sq)
	if err == nil {
		return reader, nil
	}
	// Fall back to text query for backends that don't accept
	// StructuredQuery (e.g. dalgo2sql translates text-SQL directly).
	textQ := dal.NewTextQuery(`SELECT * FROM "`+name+`"`, nil)
	return db.ExecuteQueryToRecordsReader(ctx, textQ)
}

// sameFieldNameSet reports whether a and b contain the same field names
// (as a set — order doesn't matter, duplicates collapse).
func sameFieldNameSet(a, b []dal.FieldName) bool {
	if len(a) != len(b) {
		return false
	}
	in := make(map[dal.FieldName]struct{}, len(a))
	for _, n := range a {
		in[n] = struct{}{}
	}
	for _, n := range b {
		if _, ok := in[n]; !ok {
			return false
		}
	}
	return true
}

// fieldNameList renders a slice of FieldName as a comma-separated list
// for error messages.
func fieldNameList(names []dal.FieldName) string {
	switch len(names) {
	case 0:
		return "()"
	case 1:
		return "(" + string(names[0]) + ")"
	}
	out := "("
	for i, n := range names {
		if i > 0 {
			out += ","
		}
		out += string(n)
	}
	return out + ")"
}

// truncateTargetCollection deletes every record from the target
// collection named `name`, within a single ReadwriteTransaction.
//
// Per Task 5.5 of the db-copy MVP plan: DALgo lacks a portable Truncate
// op, so we enumerate-and-delete. Correctness over throughput for the
// MVP. The cursor is fully drained BEFORE the transaction starts so we
// don't hold a reader and a writer on the same DB simultaneously (the
// SQLite driver is single-writer, single-reader-per-tx).
//
// Key construction mirrors copyRows: SQLite's ExecuteQueryToRecordsReader
// returns Records whose .Key() is a placeholder, so we rebuild it from
// the row's data using the source-side PK encoding (single-column raw,
// composite joined by `__`). For inGitDB the reader produces records
// with a usable .Key() — we accept either path by preferring a
// reconstructable key from row data and falling back to rec.Key().
func truncateTargetCollection(ctx context.Context, target dal.DB, name string) error {
	keys, err := collectKeysToTruncate(ctx, target, name)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}
	return target.RunReadwriteTransaction(ctx,
		func(ctx context.Context, tx dal.ReadwriteTransaction) error {
			for _, k := range keys {
				if err := tx.Delete(ctx, k); err != nil {
					return fmt.Errorf("delete record %s: %w", k, err)
				}
			}
			return nil
		})
}

// collectKeysToTruncate reads every record of `name` from target and
// returns its Key, rebuilt from the row data using the target's own
// PK declaration. The cursor is closed before returning.
func collectKeysToTruncate(ctx context.Context, target dal.DB, name string) ([]*dal.Key, error) {
	// Describe the target collection so we know which row columns form
	// its PK. inGitDB synthesizes "$key"; SQLite returns the natural PK.
	ref := dal.NewCollectionRef(name, "", nil)
	def, err := dbschema.DescribeCollection(ctx, target, &ref)
	if err != nil {
		return nil, fmt.Errorf("describe target %q for truncate: %w", name, err)
	}
	pkFields := make([]string, 0, len(def.PrimaryKey))
	for _, p := range def.PrimaryKey {
		pkFields = append(pkFields, string(p))
	}

	// Build a query that works against both MVP backends:
	//   - SQLite (dalgo2sql) accepts text queries — use SELECT *.
	//   - inGitDB (dalgo2ingitdb) requires a StructuredQuery — build one
	//     via dal.From(<collectionRef>).SelectIntoRecordset().
	// Try the structured form first; on backends that don't support it
	// (older dalgo2sql versions) fall back to the text form.
	reader, err := readAllRecordsForTruncate(ctx, target, name)
	if err != nil {
		return nil, fmt.Errorf("read target %q for truncate: %w", name, err)
	}
	defer func() { _ = reader.Close() }()

	var keys []*dal.Key
	for {
		rec, err := reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, dal.ErrNoMoreRecords) {
				return keys, nil
			}
			return keys, fmt.Errorf("read record from target %q: %w", name, err)
		}
		key, err := targetRecordKey(name, pkFields, rec)
		if err != nil {
			return keys, err
		}
		keys = append(keys, key)
	}
}

// targetRecordKey reconstructs a dal.Key for a target row. inGitDB's
// reader produces records with usable .Key() values (its PK is the
// synthesized "$key", not a row column), so we use rec.Key() in that
// case. SQLite's reader yields placeholder keys, so we rebuild from
// the row data using the target's declared PK fields — matching
// copyRows' encoding so the deleted keys have the same shape as the
// keys subsequently inserted.
func targetRecordKey(collection string, pkFields []string, rec dal.Record) (*dal.Key, error) {
	// Synthetic "$key" PK (inGitDB) — the reader already produced the
	// correct key on the record.
	if len(pkFields) == 1 && pkFields[0] == string("$key") {
		k := rec.Key()
		if k == nil {
			return nil, fmt.Errorf("truncate %q: inGitDB record has no key", collection)
		}
		return k, nil
	}
	data, ok := rec.Data().(map[string]any)
	if !ok || len(pkFields) == 0 {
		k := rec.Key()
		if k == nil {
			return nil, fmt.Errorf("truncate %q: record has no key and no map data", collection)
		}
		return k, nil
	}
	id, err := encodeRecordID(collection, pkFields, data)
	if err != nil {
		return nil, err
	}
	return dal.NewKeyWithID(collection, id), nil
}
