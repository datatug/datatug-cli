package faithful

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// demoDatabases returns name=path pairs from DATATUG_DEMO_SQLITE (comma separated).
func demoDatabases(t *testing.T) map[string]string {
	raw := os.Getenv("DATATUG_DEMO_SQLITE")
	if raw == "" {
		t.Skip("DATATUG_DEMO_SQLITE (name=path,name=path) is not set")
	}
	out := map[string]string{}
	for _, kv := range strings.Split(raw, ",") {
		name, path, ok := strings.Cut(kv, "=")
		if ok {
			out[name] = path
		}
	}
	return out
}

func TestRoundTripSQLitePGSQLite(t *testing.T) {
	for name, path := range demoDatabases(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			src := openSQLite(t, path)
			pg := newPGDatabase(t)
			rep, err := Copy(ctx, src, pg, Options{})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("RESULT leg=sqlite>pg db=%s %s warnings=%s", name, totalsLine(rep.Totals), warningKinds(rep.Warnings))
			for _, w := range rep.Warnings {
				t.Logf("  warn %s", w)
			}
			back := newSQLite(t)
			rep2, err := Copy(ctx, pg, back, Options{})
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range rep2.Warnings {
				t.Logf("  warn(back) %s", w)
			}
			diffs, err := Verify(ctx, src, back)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("RESULT leg=sqlite>pg>sqlite db=%s %s warnings=%s diffs=%d", name, totalsLine(rep2.Totals), warningKinds(rep2.Warnings), len(diffs))
			for _, d := range diffs {
				if explainedBy(rep, d) {
					continue
				}
				t.Errorf("diff: %s", d)
			}
		})
	}
}

// explainedBy says whether the warnings of the first leg announced the difference d: a column that
// was widened or whose text was rewritten, an index whose name was shortened.
func explainedBy(rep *Report, d Diff) bool {
	for _, w := range rep.Warnings {
		switch w.Kind {
		case WarnTypeWidened, WarnValuesNormalized:
			if d.Object == w.Object {
				return true
			}
		case WarnIndexRenamed:
			if d.Aspect == "indexes" && strings.HasPrefix(w.Object, d.Object+".") {
				return true
			}
		}
	}
	return false
}

// TestRoundTripPGSQLitePG starts from PostgreSQL copies of the demo databases loaded by
// demo-db/websites/scripts/hosting-tools/postgres_samples.py (`generate` writes <name>.sql into
// DATATUG_PG_SAMPLES_DIR; each file creates a schema called <name>).
func TestRoundTripPGSQLitePG(t *testing.T) {
	dir := os.Getenv("DATATUG_PG_SAMPLES_DIR")
	if dir == "" {
		t.Skip("DATATUG_PG_SAMPLES_DIR is not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.sql"))
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".sql")
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			src := newPGDatabase(t)
			src.Schema = name
			sqlText, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			simple := simpleProtocol(t, src)
			if _, err := simple.ExecContext(ctx, string(sqlText)); err != nil {
				// The script's own views of northwind do not run on PostgreSQL (text = integer):
				// load the tables without the views.
				t.Logf("sample script failed (%v): loading it without its views", err)
				stripped := regexp.MustCompile(`(?s)CREATE VIEW .*?;\n`).ReplaceAllString(string(sqlText), "")
				fresh := newPGDatabase(t)
				fresh.Schema = name
				src = fresh
				simple = simpleProtocol(t, src)
				if _, err := simple.ExecContext(ctx, stripped); err != nil {
					t.Fatal(err)
				}
			}
			mid := newSQLite(t)
			rep, err := Copy(ctx, src, mid, Options{})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("RESULT leg=pg>sqlite db=%s %s warnings=%s", name, totalsLine(rep.Totals), warningKinds(rep.Warnings))
			for _, w := range rep.Warnings {
				t.Logf("  warn %s", w)
			}
			dst := newPGDatabase(t)
			dst.Schema = name
			rep2, err := Copy(ctx, mid, dst, Options{})
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range rep2.Warnings {
				t.Logf("  warn(back) %s", w)
			}
			diffs, err := Verify(ctx, src, dst)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("RESULT leg=pg>sqlite>pg db=%s %s warnings=%s diffs=%d", name, totalsLine(rep2.Totals), warningKinds(rep2.Warnings), len(diffs))
			for _, d := range diffs {
				t.Errorf("diff: %s", d)
			}
		})
	}
}

func totalsLine(t Totals) string {
	return fmt.Sprintf("tables=%d rows=%d pk=%d fk=%d idx=%d uq=%d ck=%d", t.Tables, t.Rows, t.PrimaryKeys, t.ForeignKeys, t.Indexes, t.UniqueConstraints, t.CheckConstraints)
}

func warningKinds(ws []Warning) string {
	counts := map[string]int{}
	for _, w := range ws {
		counts[w.Kind]++
	}
	var kinds []string
	for k, n := range counts {
		kinds = append(kinds, fmt.Sprintf("%s:%d", k, n))
	}
	sort.Strings(kinds)
	return strings.Join(kinds, ",")
}

func TestSQLiteToSQLiteIsExact(t *testing.T) {
	for name, path := range demoDatabases(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			src := openSQLite(t, path)
			dst := newSQLite(t)
			rep, err := Copy(ctx, src, dst, Options{})
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range rep.Warnings {
				t.Logf("  warn %s", w)
			}
			diffs, err := Verify(ctx, src, dst)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range diffs {
				t.Errorf("diff: %s", d)
			}
			// Every object of sqlite_master is there, too (views, triggers, indexes).
			var a, b int
			_ = src.DB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite\_%' ESCAPE '\'`).Scan(&a)
			_ = dst.DB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite\_%' ESCAPE '\'`).Scan(&b)
			if a != b {
				t.Errorf("sqlite_master objects: %d != %d", a, b)
			}
		})
	}
}
