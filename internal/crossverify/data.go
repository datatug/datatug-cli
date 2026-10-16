package crossverify

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/datatug/datatug-cli/pkg/dbcopy/faithful"
)

const maxExamples = 5

func quoteSQLite(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func quoteMySQL(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

// rowSource yields the canonical forms of the rows of one side.
type rowSource func(ctx context.Context, visit func(forms []string) error) error

// eachRow scans the rows into width values and hands each row to visit.
func eachRow(rows *sql.Rows, width int, visit func(vals []any) error) error {
	vals := make([]any, width)
	ptrs := make([]any, width)
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		if err := visit(vals); err != nil {
			return err
		}
	}
	return rows.Err()
}

// sourceRows reads the SQLite side. A column is read as +"c" so the driver gives the value with its storage
// class and no declared type, and an empty blob, which the driver reads as NULL, is told apart by a flag.
func sourceRows(in Input, t *faithful.ScriptTable) rowSource {
	return func(ctx context.Context, visit func([]string) error) error {
		n := len(t.Columns)
		exprs := make([]string, 0, 2*n)
		for _, c := range t.Columns {
			exprs = append(exprs, "+"+quoteSQLite(c.Src.Name))
		}
		for _, c := range t.Columns {
			q := quoteSQLite(c.Src.Name)
			exprs = append(exprs, "(typeof("+q+") = 'blob' AND length("+q+") = 0)")
		}
		rows, err := in.Source.QueryContext(ctx, "SELECT "+strings.Join(exprs, ", ")+" FROM "+quoteSQLite(t.Src.Name)+" ORDER BY "+t.OrderBy)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		return eachRow(rows, 2*n, func(vals []any) error {
			forms := make([]string, n)
			for i, c := range t.Columns {
				v := vals[i]
				if flag, _ := vals[n+i].(int64); flag != 0 {
					v = []byte{}
				}
				var err error
				if forms[i], err = canonSource(c.Choice, v); err != nil {
					return fmt.Errorf("source column %q: %w", c.Src.Name, err)
				}
			}
			return visit(forms)
		})
	}
}

// targetRows reads the MySQL side through a prepared statement, which makes the driver use the binary
// protocol: integers come back as int64 and doubles as float64, exactly.
func targetRows(in Input, t *faithful.ScriptTable) rowSource {
	return func(ctx context.Context, visit func([]string) error) error {
		names := make([]string, len(t.Columns))
		for i, c := range t.Columns {
			names[i] = quoteMySQL(c.Name)
		}
		stmt, err := in.Target.PrepareContext(ctx, "SELECT "+strings.Join(names, ", ")+" FROM "+quoteMySQL(t.Name))
		if err != nil {
			return err
		}
		defer func() { _ = stmt.Close() }()
		rows, err := stmt.QueryContext(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		return eachRow(rows, len(names), func(vals []any) error {
			forms := make([]string, len(names))
			for i, c := range t.Columns {
				var err error
				if forms[i], err = canonTarget(c.Choice, vals[i]); err != nil {
					return fmt.Errorf("target column %q: %w", c.Name, err)
				}
			}
			return visit(forms)
		})
	}
}

func digests(ctx context.Context, src rowSource) (map[[32]byte]int, int64, error) {
	m := map[[32]byte]int{}
	var n int64
	err := src(ctx, func(forms []string) error {
		m[rowDigest(forms)]++
		n++
		return nil
	})
	return m, n, err
}

func shown(t *faithful.ScriptTable, forms []string) string {
	parts := make([]string, len(forms))
	for i, f := range forms {
		s := f
		if len(s) > 60 {
			s = s[:60] + "..."
		}
		parts[i] = fmt.Sprintf("%s=%q", t.Columns[i].Name, s)
	}
	return strings.Join(parts, " ")
}

// examples lists up to maxExamples rows of src whose digest other does not hold as often.
func examples(ctx context.Context, t *faithful.ScriptTable, src rowSource, other map[[32]byte]int) ([]string, error) {
	left := map[[32]byte]int{}
	for k, v := range other {
		left[k] = v
	}
	var out []string
	err := src(ctx, func(forms []string) error {
		d := rowDigest(forms)
		if left[d] > 0 {
			left[d]--
			return nil
		}
		if len(out) < maxExamples {
			out = append(out, shown(t, forms))
		}
		return nil
	})
	return out, err
}

// compareData compares the row counts and every value of a table as multisets of rows.
func compareData(ctx context.Context, in Input, t *faithful.ScriptTable, add func(obj, aspect, want, got string)) (rows, values int64, err error) {
	srcMap, srcN, err := digests(ctx, sourceRows(in, t))
	if err != nil {
		return 0, 0, fmt.Errorf("read the source: %w", err)
	}
	var got int64
	if err := in.Target.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteMySQL(t.Name)).Scan(&got); err != nil {
		return 0, 0, fmt.Errorf("count the target rows: %w", err)
	}
	add(t.Name, "row count", fmt.Sprint(srcN), fmt.Sprint(got))
	add(t.Name, "planned row count", fmt.Sprint(t.Rows), fmt.Sprint(srcN))
	dstMap, dstN, err := digests(ctx, targetRows(in, t))
	if err != nil {
		return 0, 0, fmt.Errorf("read the target: %w", err)
	}
	equal := len(srcMap) == len(dstMap)
	for k, v := range srcMap {
		if dstMap[k] != v {
			equal = false
			break
		}
	}
	if !equal {
		want, got := describeMismatch(ctx, t, sourceRows(in, t), targetRows(in, t), srcMap, dstMap, srcN, dstN)
		add(t.Name, "values", want, got)
	}
	return srcN, srcN * int64(len(t.Columns)), nil
}

// describeMismatch says what each side holds that the other does not: the first rows of each. A side that
// cannot be read again says why instead.
func describeMismatch(ctx context.Context, t *faithful.ScriptTable, src, dst rowSource, srcMap, dstMap map[[32]byte]int, srcN, dstN int64) (want, got string) {
	describe := func(side string, rows rowSource, other map[[32]byte]int, n int64, lacks string) string {
		ex, err := examples(ctx, t, rows, other)
		if err != nil {
			return fmt.Sprintf("%d rows, the %s could not be read again: %v", n, side, err)
		}
		return fmt.Sprintf("%d rows, first rows the %s lacks: %s", n, lacks, strings.Join(ex, " | "))
	}
	return describe("source", src, dstMap, srcN, "target"), describe("target", dst, srcMap, dstN, "source")
}
