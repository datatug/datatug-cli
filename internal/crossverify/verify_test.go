package crossverify

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy/faithful"
)

func verifyOK(t *testing.T) (*Result, error) {
	t.Helper()
	src := openSource(t, sourceStatements...)
	p := plan(t, src)
	dst := emulate(t, src, p)
	return Verify(context.Background(), Input{Plan: p, Source: src, Target: dst, Schema: "db"})
}

func TestVerifyAcceptsAFaithfulLoad(t *testing.T) {
	res, err := verifyOK(t)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range res.Diffs {
		t.Errorf("unexpected difference: %s", d)
	}
	if res.Tables != 5 || res.Rows != 12 || res.Values == 0 {
		t.Errorf("%+v", res)
	}
}

// Each tampering of the loaded database must be reported as a difference of the named aspect.
func TestVerifyReportsEveryKindOfDifference(t *testing.T) {
	cases := []struct {
		name   string
		sql    []string
		aspect string
	}{
		{"extra table", []string{"INSERT INTO information_schema.TABLES VALUES ('db', 'ghost', 'BASE TABLE')"}, "table"},
		{"missing table", []string{"DELETE FROM information_schema.TABLES WHERE TABLE_NAME = 'loose'"}, "table"},
		{"missing marker", []string{"DELETE FROM information_schema.TABLES WHERE TABLE_NAME = '_demodb_import'"}, "table"},
		{"marker without a row", []string{"DELETE FROM `_demodb_import`"}, "completion marker"},
		{"marker rows", []string{"UPDATE `_demodb_import` SET rows_count = 1"}, "marker rows"},
		{"marker tables", []string{"UPDATE `_demodb_import` SET tables_count = 1"}, "marker tables"},
		{"marker sha", []string{"UPDATE `_demodb_import` SET source_sha256 = 'x'"}, "marker source sha256"},
		{"column order", []string{"UPDATE information_schema.COLUMNS SET ORDINAL_POSITION = 99 WHERE COLUMN_NAME = 'code'"}, "columns"},
		{"column renamed", []string{"UPDATE information_schema.COLUMNS SET COLUMN_NAME = 'note2' WHERE COLUMN_NAME = 'note'"}, "columns"},
		{"column type", []string{"UPDATE information_schema.COLUMNS SET COLUMN_TYPE = 'int' WHERE COLUMN_NAME = 'code'"}, "type"},
		{"nullable", []string{"UPDATE information_schema.COLUMNS SET IS_NULLABLE = 'YES' WHERE COLUMN_NAME = 'code'"}, "nullable"},
		{"auto_increment", []string{"UPDATE information_schema.COLUMNS SET EXTRA = '' WHERE EXTRA = 'auto_increment'"}, "auto_increment"},
		{"default", []string{"UPDATE information_schema.COLUMNS SET COLUMN_DEFAULT = NULL, EXTRA = '' WHERE COLUMN_NAME = 'note'"}, "has default"},
		{"primary key", []string{"DELETE FROM information_schema.STATISTICS WHERE INDEX_NAME = 'PRIMARY' AND TABLE_NAME = 'child' AND COLUMN_NAME = 'b'"}, "primary key"},
		{"missing index", []string{"DELETE FROM information_schema.STATISTICS WHERE INDEX_NAME = 'parent_born'"}, "index"},
		{"extra index", []string{"INSERT INTO information_schema.STATISTICS VALUES ('db', 'loose', 'zz', 1, 1, 'x', 'A', NULL)"}, "index"},
		{"index name", []string{"UPDATE information_schema.STATISTICS SET INDEX_NAME = 'other' WHERE INDEX_NAME = 'parent_born'"}, "index"},
		{"index desc", []string{"UPDATE information_schema.STATISTICS SET COLLATION = 'A' WHERE INDEX_NAME = 'parent_born'"}, "index"},
		{"prefix index", []string{"UPDATE information_schema.STATISTICS SET SUB_PART = 4 WHERE INDEX_NAME = 'parent_born' AND COLUMN_NAME = 'code'"}, "index"},
		{"index not unique", []string{"UPDATE information_schema.STATISTICS SET NON_UNIQUE = 1 WHERE INDEX_NAME = 'code'"}, "index"},
		{"foreign key action", []string{"UPDATE information_schema.REFERENTIAL_CONSTRAINTS SET DELETE_RULE = 'RESTRICT'"}, "foreign keys"},
		{"foreign key missing", []string{"DELETE FROM information_schema.KEY_COLUMN_USAGE"}, "foreign keys"},
		{"foreign key name", []string{"UPDATE information_schema.KEY_COLUMN_USAGE SET CONSTRAINT_NAME = 'k'", "UPDATE information_schema.REFERENTIAL_CONSTRAINTS SET CONSTRAINT_NAME = 'k'"}, "foreign key name"},
		{"check constraints", []string{"DELETE FROM information_schema.TABLE_CONSTRAINTS"}, "check constraints"},
		{"missing row", []string{"DELETE FROM `child` WHERE a = 2"}, "row count"},
		{"changed value", []string{"UPDATE `parent` SET note = x'6e6f' WHERE note = x'68656c6c6f'"}, "values"},
		{"changed decimal", []string{"UPDATE `parent` SET price = x'332e3530' WHERE price = x'312e3530'"}, "values"},
		{"changed float", []string{"UPDATE `parent` SET ratio = 0.2 WHERE ratio = 0.1"}, "values"},
		{"changed null", []string{"UPDATE `parent` SET pic = NULL WHERE code = x'4232'"}, "values"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := openSource(t, sourceStatements...)
			p := plan(t, src)
			dst := emulate(t, src, p)
			for _, s := range c.sql {
				if _, err := dst.Exec(s); err != nil {
					t.Fatalf("%v\n%s", err, s)
				}
			}
			res, err := Verify(context.Background(), Input{Plan: p, Source: src, Target: dst, Schema: "db", SourceSHA256: strings.Repeat("ab", 32)})
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range res.Diffs {
				if d.Aspect == c.aspect {
					return
				}
			}
			t.Errorf("no %q difference in %v", c.aspect, res.Diffs)
		})
	}
}

func TestVerifyNamesAreNeverRenamedSilently(t *testing.T) {
	src := openSource(t, sourceStatements...)
	p := plan(t, src)
	dst := emulate(t, src, p)
	good, err := Verify(context.Background(), Input{Plan: p, Source: src, Target: dst, Schema: "db"})
	if err != nil || len(good.Diffs) != 0 {
		t.Fatalf("%v %v", err, good.Diffs)
	}
	renamed := 0
	for _, tb := range p.Tables {
		if tb.Name != tb.Src.Name {
			renamed++
		}
		for _, c := range tb.Columns {
			if c.Name != c.Src.Name {
				renamed++
			}
		}
	}
	if renamed != 2 {
		t.Fatalf("the source holds 2 names MySQL cannot store, the plan renamed %d", renamed)
	}
	// Without the warnings the renames are silent: reported.
	var kept []faithful.Warning
	for _, w := range p.Warnings {
		if w.Kind != faithful.WarnNameRenamed {
			kept = append(kept, w)
		}
	}
	p.Warnings = kept
	res, err := Verify(context.Background(), Input{Plan: p, Source: src, Target: dst, Schema: "db"})
	if err != nil {
		t.Fatal(err)
	}
	names := 0
	for _, d := range res.Diffs {
		if d.Aspect == "name" || d.Aspect == "count" {
			names++
		}
	}
	if names != 3 {
		t.Errorf("want the 2 renames and the count, got %v", res.Diffs)
	}
	// A warning for a name that was not changed is idle.
	p.Warnings = append(p.Warnings, faithful.Warning{Kind: faithful.WarnNameRenamed, Object: "parent", Message: "idle"})
	res, _ = Verify(context.Background(), Input{Plan: p, Source: src, Target: dst, Schema: "db"})
	found := false
	for _, d := range res.Diffs {
		found = found || (d.Aspect == "count" && d.Left == "2" && d.Right == "1")
	}
	if !found {
		t.Errorf("%v", res.Diffs)
	}
}

func TestVerifyImplicitForeignKeyIndexesAreModelled(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		diff bool
	}{
		{"the index of a key", "INSERT INTO information_schema.STATISTICS VALUES ('db', 'child', 'parent_id', 1, 1, 'parent_id', 'A', NULL)", false},
		{"the index of a key under another name", "INSERT INTO information_schema.STATISTICS VALUES ('db', 'child', 'other_name', 1, 1, 'parent_id', 'A', NULL)", true},
		{"a longer index", "INSERT INTO information_schema.STATISTICS VALUES ('db', 'child', 'longer', 1, 1, 'parent_id', 'A', NULL), ('db', 'child', 'longer', 1, 2, 'a', 'A', NULL)", true},
		{"another column", "INSERT INTO information_schema.STATISTICS VALUES ('db', 'child', 'parent_id', 1, 1, 'a', 'A', NULL)", true},
		{"a unique index", "INSERT INTO information_schema.STATISTICS VALUES ('db', 'child', 'u', 0, 1, 'parent_id', 'A', NULL)", true},
		{"a descending index", "INSERT INTO information_schema.STATISTICS VALUES ('db', 'child', 'parent_id', 1, 1, 'parent_id', 'D', NULL)", true},
		{"a prefix index", "INSERT INTO information_schema.STATISTICS VALUES ('db', 'child', 'parent_id', 1, 1, 'parent_id', 'A', 3)", true},
	}
	for _, c := range cases {
		src := openSource(t, sourceStatements...)
		p := plan(t, src)
		dst := emulate(t, src, p)
		// the emulation already adds the implicit index of each key; drop it so the case's own index is the only one
		if _, err := dst.Exec("DELETE FROM information_schema.STATISTICS WHERE TABLE_NAME = 'child' AND INDEX_NAME = 'parent_id'"); err != nil {
			t.Fatal(err)
		}
		if _, err := dst.Exec(c.sql); err != nil {
			t.Fatal(err)
		}
		res, err := Verify(context.Background(), Input{Plan: p, Source: src, Target: dst, Schema: "db"})
		if err != nil {
			t.Fatal(err)
		}
		if got := len(res.Diffs) > 0; got != c.diff {
			t.Errorf("%s: %v", c.name, res.Diffs)
		}
	}
}

func TestVerifyFailures(t *testing.T) {
	type fix func(t *testing.T, src, dst *sql.DB, p *faithful.ScriptPlan)
	exec := func(q string) fix {
		return func(t *testing.T, _, dst *sql.DB, _ *faithful.ScriptPlan) {
			if _, err := dst.Exec(q); err != nil {
				t.Fatalf("%v\n%s", err, q)
			}
		}
	}
	cases := map[string]struct {
		fix  fix
		want string
	}{
		"catalog unreadable":    {exec("DROP TABLE information_schema.TABLES"), "read tables"},
		"marker unreadable":     {exec("DROP TABLE `_demodb_import`"), "completion marker"},
		"target table missing":  {exec("DROP TABLE `parent`"), `table "parent"`},
		"source order is wrong": {func(t *testing.T, _, _ *sql.DB, p *faithful.ScriptPlan) { p.Table("parent").OrderBy = "nope" }, "read the source"},
		"source value is not of its column's family": {func(t *testing.T, _, _ *sql.DB, p *faithful.ScriptPlan) {
			p.Table("parent").Col("code").Choice.Family = "int"
		}, "does not fit"},
		"target value is not of its column's family": {exec("UPDATE `parent` SET ratio = 'text'"), "does not fit"},
		"target column missing":                      {exec("ALTER TABLE `parent` DROP COLUMN note"), "read the target"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			src := openSource(t, sourceStatements...)
			p := plan(t, src)
			dst := emulate(t, src, p)
			c.fix(t, src, dst, p)
			_, err := Verify(context.Background(), Input{Plan: p, Source: src, Target: dst, Schema: "db"})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("%v, want it to mention %q", err, c.want)
			}
		})
	}
}

// A name is renamed only when MySQL cannot store it: a column that differs from another by accents alone is no
// reason, and a rename without such a reason is a difference even when the plan and the load agree on it.
func TestVerifyRenamesNeedAReason(t *testing.T) {
	src := openSource(t, `CREATE TABLE acc (id INTEGER PRIMARY KEY, e, "é", "Ä", "ä", "x y ")`, `CREATE TABLE "plain" (id INTEGER PRIMARY KEY)`)
	p := plan(t, src)
	dst := emulate(t, src, p)
	good, err := Verify(context.Background(), Input{Plan: p, Source: src, Target: dst, Schema: "db"})
	if err != nil || len(good.Diffs) != 0 {
		t.Fatalf("%v %v", err, good.Diffs)
	}
	renamed := 0
	for _, c := range p.Table("acc").Columns {
		if c.Name != c.Src.Name {
			renamed++
		}
	}
	if renamed != 2 { // "ä" (case twin of "Ä") and "x y " (a trailing space)
		t.Fatalf("renamed %d columns", renamed)
	}
	// The mutation: the plan also renames "é", with its warning, as a fold of accents would.
	var e *faithful.ScriptColumn
	for _, c := range p.Table("acc").Columns {
		if c.Src.Name == "é" {
			e = c
		}
	}
	e.Name = "é_12345678"
	p.Warnings = append(p.Warnings, faithful.Warning{Kind: faithful.WarnNameRenamed, Object: "acc.é", Message: "mutation"})
	p.Table("plain").Name = "plain_x"
	p.Warnings = append(p.Warnings, faithful.Warning{Kind: faithful.WarnNameRenamed, Object: "plain", Message: "mutation"})
	dst = emulate(t, src, p) // the load agrees with the plan: only the reason for the renames is missing
	res, err := Verify(context.Background(), Input{Plan: p, Source: src, Target: dst, Schema: "db"})
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, d := range res.Diffs {
		if d.Aspect == "rename justified" {
			found[d.Object] = true
		}
	}
	if !found["acc.é"] || !found["plain"] || len(found) != 2 {
		t.Errorf("%v", res.Diffs)
	}
}

func TestUnstorableName(t *testing.T) {
	for name, bad := range map[string]bool{"": true, "a\xffb": true, "a ": true, strings.Repeat("é", 65): true, "😀": true, "a\nb": true, "a\x7fb": true, "a\u0085b": true, "a\u2028b": true, "é": false, "Order Details": false, strings.Repeat("é", 64): false} {
		if got := unstorableName(name) != ""; got != bad {
			t.Errorf("unstorableName(%q) = %v", name, got)
		}
	}
}
