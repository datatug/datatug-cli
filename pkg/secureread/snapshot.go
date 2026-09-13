package secureread

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-core/pkg/apicontract"
	_ "modernc.org/sqlite"
)

// ErrSnapshotPolicyUnexpressible means SQLite cannot represent a recorded
// value without changing policy-comparison semantics. Callers must deny the
// read without disclosing the value.
var ErrSnapshotPolicyUnexpressible = errors.New("snapshot policy evaluation is not safely expressible")

// RunSnapshot replays recorded typed rows through the session's current
// access policies. A temporary private SQLite source lets the same
// accesspolicies.Run path enforce both column projections and expressible row
// conditions; an unsupported condition fails closed in RunStructured.
func (e *Executor) RunSnapshot(ctx context.Context, collection string, recordset apicontract.Recordset) (Result, error) {
	if err := recordset.Validate(); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(collection) == "" {
		return Result{}, fmt.Errorf("snapshot collection is required")
	}
	dir, err := os.MkdirTemp("", "datatug-snapshot-policy-")
	if err != nil {
		return Result{}, fmt.Errorf("create snapshot policy workspace: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.Chmod(dir, 0o700); err != nil {
		return Result{}, fmt.Errorf("secure snapshot policy workspace: %w", err)
	}
	dbPath := filepath.Join(dir, "snapshot.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return Result{}, err
	}
	if err := materializeSnapshot(ctx, db, collection, recordset); err != nil {
		_ = db.Close()
		return Result{}, err
	}
	if err := db.Close(); err != nil {
		return Result{}, err
	}
	if err := os.Chmod(dbPath, 0o600); err != nil {
		return Result{}, err
	}
	query := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef(collection, ""))).SelectColumns()
	result, err := e.RunStructured(ctx, "sqlite://"+dbPath, query, nil)
	if err != nil {
		return Result{}, err
	}
	typed, err := restoreSnapshotRecordset(recordset, result)
	if err != nil {
		return Result{}, err
	}
	result.SnapshotRecordset = &typed
	return result, nil
}

func restoreSnapshotRecordset(original apicontract.Recordset, filtered Result) (apicontract.Recordset, error) {
	allowed := make(map[string]struct{}, len(filtered.Columns))
	for _, name := range filtered.Columns {
		allowed[name] = struct{}{}
	}
	indices := make([]int, 0, len(filtered.Columns))
	columnNames := make([]string, 0, len(filtered.Columns))
	for index, column := range original.Columns {
		if _, ok := allowed[column.Name]; ok {
			indices = append(indices, index)
			columnNames = append(columnNames, column.Name)
			delete(allowed, column.Name)
		}
	}
	if len(allowed) != 0 {
		return apicontract.Recordset{}, ErrSnapshotPolicyUnexpressible
	}
	columns := make([]apicontract.Column, len(indices))
	for i, index := range indices {
		columns[i] = original.Columns[index]
	}
	rows := make([][]apicontract.TypedValue, 0, len(filtered.Rows))
	used := make([]bool, len(original.Rows))
	for _, filteredRow := range filtered.Rows {
		match := -1
		for originalIndex, originalRow := range original.Rows {
			if used[originalIndex] || !snapshotRowMatches(originalRow, indices, columnNames, filteredRow.Data) {
				continue
			}
			match = originalIndex
			break
		}
		if match < 0 {
			for filteredIndex, index := range indices {
				actual := filteredRow.Data[columnNames[filteredIndex]]
				if !snapshotValueMatches(original.Rows[0][index], actual) {
					return apicontract.Recordset{}, fmt.Errorf("%w: cannot restore %s from %T", ErrSnapshotPolicyUnexpressible, original.Rows[0][index].Type, actual)
				}
			}
			return apicontract.Recordset{}, fmt.Errorf("%w: filtered row cannot be matched to original typed evidence", ErrSnapshotPolicyUnexpressible)
		}
		used[match] = true
		row := make([]apicontract.TypedValue, len(indices))
		for i, index := range indices {
			row[i] = original.Rows[match][index]
		}
		rows = append(rows, row)
	}
	restored := apicontract.Recordset{Columns: columns, Rows: rows}
	if err := restored.Validate(); err != nil {
		return apicontract.Recordset{}, ErrSnapshotPolicyUnexpressible
	}
	return restored, nil
}

func snapshotRowMatches(original []apicontract.TypedValue, indices []int, columns []string, filtered map[string]any) bool {
	for filteredIndex, index := range indices {
		value := original[index]
		actual, found := filtered[columns[filteredIndex]]
		if !found || !snapshotValueMatches(value, actual) {
			return false
		}
	}
	return true
}

func snapshotValueMatches(expected apicontract.TypedValue, actual any) bool {
	switch expected.Type {
	case apicontract.ValueTypeNull:
		return actual == nil
	case apicontract.ValueTypeString, apicontract.ValueTypeDate, apicontract.ValueTypeDatetime:
		value, ok := actual.(string)
		return ok && value == expected.Str
	case apicontract.ValueTypeDecimal:
		value, err := snapshotDecimalFloat(expected.Str)
		if err != nil {
			return false
		}
		actualValue, ok := actual.(float64)
		return ok && actualValue == value
	case apicontract.ValueTypeInteger:
		value, err := strconv.ParseInt(expected.Str, 10, 64)
		if err != nil {
			return false
		}
		switch actual := actual.(type) {
		case int64:
			return actual == value
		case int:
			return int64(actual) == value
		case float64:
			return actual == float64(value)
		default:
			return false
		}
	case apicontract.ValueTypeNumber:
		value, ok := actual.(float64)
		return ok && value == expected.Num
	case apicontract.ValueTypeBoolean:
		switch actual := actual.(type) {
		case bool:
			return actual == expected.Bool
		case int64:
			return actual == 1 && expected.Bool || actual == 0 && !expected.Bool
		case float64:
			return actual == 1 && expected.Bool || actual == 0 && !expected.Bool
		default:
			return false
		}
	default:
		return false
	}
}

func materializeSnapshot(ctx context.Context, db *sql.DB, collection string, recordset apicontract.Recordset) error {
	quotedCollection := quoteSQLiteIdentifier(collection)
	definitions := make([]string, len(recordset.Columns))
	for i, column := range recordset.Columns {
		definitions[i] = quoteSQLiteIdentifier(column.Name) + " " + snapshotSQLiteType(column.Type)
	}
	if len(definitions) == 0 {
		definitions = []string{`"_datatug_probe" INTEGER`}
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+quotedCollection+" ("+strings.Join(definitions, ",")+")"); err != nil {
		return fmt.Errorf("create snapshot policy table: %w", err)
	}
	if len(recordset.Rows) == 0 {
		return nil
	}
	placeholders := make([]string, len(recordset.Columns))
	for i := range placeholders {
		placeholders[i] = "?"
	}
	insert := "INSERT INTO " + quotedCollection + " VALUES(" + strings.Join(placeholders, ",") + ")"
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, insert)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, row := range recordset.Rows {
		values := make([]any, len(row))
		for i, value := range row {
			converted, err := snapshotDriverValue(value)
			if err != nil {
				return err
			}
			values[i] = converted
		}
		if _, err := stmt.ExecContext(ctx, values...); err != nil {
			return fmt.Errorf("insert snapshot policy row: %w", err)
		}
	}
	return tx.Commit()
}

func quoteSQLiteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func snapshotSQLiteType(valueType string) string {
	switch apicontract.ValueType(valueType) {
	case apicontract.ValueTypeNumber:
		return "REAL"
	case apicontract.ValueTypeInteger, apicontract.ValueTypeBoolean:
		return "INTEGER"
	case apicontract.ValueTypeDecimal:
		return "REAL"
	default:
		return "TEXT"
	}
}

func snapshotDriverValue(value apicontract.TypedValue) (any, error) {
	switch value.Type {
	case apicontract.ValueTypeNull:
		return nil, nil
	case apicontract.ValueTypeString, apicontract.ValueTypeDate, apicontract.ValueTypeDatetime:
		return value.Str, nil
	case apicontract.ValueTypeDecimal:
		return snapshotDecimalFloat(value.Str)
	case apicontract.ValueTypeInteger:
		if parsed, err := strconv.ParseInt(value.Str, 10, 64); err == nil {
			return parsed, nil
		}
		return nil, ErrSnapshotPolicyUnexpressible
	case apicontract.ValueTypeNumber:
		return value.Num, nil
	case apicontract.ValueTypeBoolean:
		return value.Bool, nil
	default:
		return nil, ErrSnapshotPolicyUnexpressible
	}
}

func snapshotDecimalFloat(value string) (float64, error) {
	exact, ok := new(big.Rat).SetString(value)
	if !ok {
		return 0, ErrSnapshotPolicyUnexpressible
	}
	approx, _ := exact.Float64()
	roundTrip := new(big.Rat).SetFloat64(approx)
	if roundTrip == nil || roundTrip.Cmp(exact) != 0 {
		return 0, ErrSnapshotPolicyUnexpressible
	}
	return approx, nil
}
