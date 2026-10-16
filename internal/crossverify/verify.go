package crossverify

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"hash"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/datatug/datatug-cli/pkg/dbcopy/faithful"
)

func newHash() hash.Hash { return sha256.New() }

// engine is the engine of the input (MySQL when none was resolved).
func (in Input) engine() targetEngine {
	if in.eng.quote != nil {
		return in.eng
	}
	return mysqlEngine
}

// Input is what to compare: the plan the exporter makes for the SQLite source, the source, and the database
// the script was loaded into.
type Input struct {
	Plan   *faithful.ScriptPlan
	Source *sql.DB
	Target *sql.DB
	// Schema is the MySQL database (the SQL Server schema); the connection's current one when empty.
	Schema string
	// Engine is the engine that loaded the script: EngineMySQL (the default) or EngineMSSQL.
	Engine string
	eng    targetEngine
	// SourceSHA256 is the hash of the SQLite file, checked against the marker row; "" skips that check.
	SourceSHA256 string
}

// Result is what a verification found.
type Result struct {
	Diffs  []faithful.Diff
	Tables int
	Rows   int64
	// Values is the number of values compared one by one.
	Values int64
}

// Verify compares the plan and the SQLite source with what MySQL holds: tables, columns (type, nullability,
// auto-increment, default), primary keys, foreign keys, unique keys and indexes (the indexes MySQL creates
// itself for a foreign key excluded), check constraints, row counts, the completion marker, and every value.
func Verify(ctx context.Context, in Input) (*Result, error) {
	eng, err := engineByName(in.Engine)
	if err != nil {
		return nil, err
	}
	in.eng = eng
	cat, err := eng.read(ctx, in.Target, in.Schema)
	if err != nil {
		return nil, err
	}
	in.Schema = cat.Schema // the schema the reader resolved, for what an engine asks about a table
	res := &Result{}
	add := func(obj, aspect, want, got string) {
		if want != got {
			res.Diffs = append(res.Diffs, faithful.Diff{Object: obj, Aspect: aspect, Left: want, Right: got})
		}
	}
	checkNames(in.Plan, eng, add)
	planned := map[string]bool{}
	for _, t := range in.Plan.Tables {
		planned[t.Name] = true
	}
	var actualNames []string
	for _, t := range cat.Tables {
		if t.Name != faithful.MySQLMarkerTable && !planned[t.Name] {
			actualNames = append(actualNames, t.Name)
		}
	}
	for _, n := range actualNames {
		add(n, "table", "absent", "present")
	}
	if err := verifyMarker(ctx, in, eng, cat, add); err != nil {
		return nil, err
	}
	for _, t := range in.Plan.Tables {
		mt := cat.Table(t.Name)
		if mt == nil {
			add(t.Name, "table", "present", "missing")
			continue
		}
		res.Tables++
		compareCatalog(ctx, in, eng, t, mt, add)
		n, values, err := compareData(ctx, in, t, add)
		if err != nil {
			return nil, fmt.Errorf("table %q: %w", t.Name, err)
		}
		if eng.extra != nil {
			if err := eng.extra(ctx, in, t, add); err != nil {
				return nil, fmt.Errorf("table %q: %w", t.Name, err)
			}
		}
		res.Rows += n
		res.Values += values
	}
	return res, nil
}

func verifyMarker(ctx context.Context, in Input, eng targetEngine, cat *MySQLCatalog, add func(obj, aspect, want, got string)) error {
	if cat.Table(faithful.MySQLMarkerTable) == nil {
		add(faithful.MySQLMarkerTable, "table", "present", "missing")
		return nil
	}
	var sha string
	var tables, rows int64
	err := in.Target.QueryRowContext(ctx, eng.markerSQL).Scan(&sha, &tables, &rows)
	if err == sql.ErrNoRows {
		add(faithful.MySQLMarkerTable, "completion marker", "one row", "no row: the load did not finish")
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the completion marker: %w", err)
	}
	var want int64
	for _, t := range in.Plan.Tables {
		want += t.Rows
	}
	add(faithful.MySQLMarkerTable, "marker tables", fmt.Sprint(len(in.Plan.Tables)), fmt.Sprint(tables))
	add(faithful.MySQLMarkerTable, "marker rows", fmt.Sprint(want), fmt.Sprint(rows))
	if in.SourceSHA256 != "" {
		add(faithful.MySQLMarkerTable, "marker source sha256", in.SourceSHA256, sha)
	}
	return nil
}

func compareCatalog(ctx context.Context, in Input, eng targetEngine, t *faithful.ScriptTable, mt *MyTable, add func(obj, aspect, want, got string)) {
	want := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		want[i] = c.Name
	}
	got := make([]string, len(mt.Columns))
	for i, c := range mt.Columns {
		got[i] = c.Name
	}
	add(t.Name, "columns", strings.Join(want, "\x1f"), strings.Join(got, "\x1f"))
	byName := map[string]MyColumn{}
	for _, c := range mt.Columns {
		byName[c.Name] = c
	}
	for _, c := range t.Columns {
		mc, ok := byName[c.Name]
		if !ok {
			continue
		}
		obj := t.Name + "." + c.Name
		add(obj, "type", strings.ToLower(c.Choice.Type), mc.Type)
		add(obj, "nullable", fmt.Sprint(!c.NotNull), fmt.Sprint(mc.Nullable))
		add(obj, "auto_increment", fmt.Sprint(c.AutoInc), fmt.Sprint(mc.AutoIncrement))
		add(obj, "has default", fmt.Sprint(c.Default != ""), fmt.Sprint(mc.HasDefault))
		if eng.compareText {
			compareDefault(ctx, in, c, mc, add)
		}
	}
	add(t.Name, "primary key", strings.Join(t.PK, ","), strings.Join(mt.PK, ","))
	compareIndexes(eng, t, mt, add)
	compareFKs(t, mt, add)
	add(t.Name, "check constraints", fmt.Sprint(len(t.Checks)), fmt.Sprint(mt.Checks))
	if eng.compareText {
		compareChecks(in, t, mt, add)
	}
	add(t.Name, "untrusted constraints", "", strings.Join(mt.Untrusted, ", "))
}

func indexSig(unique bool, cols []string) string {
	u := "index"
	if unique {
		u = "unique"
	}
	return u + "(" + strings.Join(cols, ",") + ")"
}

// filteredSig marks the signature of an index with a WHERE clause.
func filteredSig(sig string, filtered bool) string {
	if filtered {
		return sig + " where"
	}
	return sig
}

// compareIndexes matches the planned unique keys and indexes with MySQL's. A key the plan names is
// matched by name and columns; an unnamed unique key by its columns. An index of MySQL that nothing
// planned and whose columns are the columns of a foreign key is the index MySQL creates for the key
// itself: it is modelled here and left out.
func compareIndexes(eng targetEngine, t *faithful.ScriptTable, mt *MyTable, add func(obj, aspect, want, got string)) {
	type exp struct {
		name, sig string
	}
	var expect []exp
	for _, u := range t.Uniques {
		expect = append(expect, exp{u.Name, indexSig(true, u.Columns)})
	}
	for _, ix := range t.Indexes {
		cols := make([]string, len(ix.Columns))
		for i, c := range ix.Columns {
			cols[i] = c.Name
			if c.Desc {
				cols[i] += " DESC"
			}
		}
		expect = append(expect, exp{ix.Name, filteredSig(indexSig(ix.Unique, cols), ix.Where != "")})
	}
	used := make([]bool, len(mt.Indexes))
	actualSig := func(ix MyIndex) string {
		cols := make([]string, len(ix.Columns))
		for i, c := range ix.Columns {
			cols[i] = c.Name
			if c.Desc {
				cols[i] += " DESC"
			}
			if c.Prefix > 0 {
				cols[i] += fmt.Sprintf("(%d)", c.Prefix)
			}
		}
		return filteredSig(indexSig(ix.Unique, cols), ix.Filtered)
	}
	for _, e := range expect {
		found := false
		for i, ix := range mt.Indexes {
			if used[i] || actualSig(ix) != e.sig || (e.name != "" && e.name != ix.Name) {
				continue
			}
			used[i], found = true, true
			break
		}
		if !found {
			add(t.Name+"."+e.name, "index", e.sig, "missing")
		}
	}
	for i, ix := range mt.Indexes {
		if used[i] {
			continue
		}
		if eng.implicitIndex(t, ix) {
			continue
		}
		add(t.Name+"."+ix.Name, "index", "absent", actualSig(ix))
	}
}

// implicitFKIndex is true for the plain index MySQL creates for a foreign key that has no index of its own: a
// non-unique index on exactly the columns of one of the table's foreign keys, named as MySQL names it (the
// constraint's name when the key has one, the name of its first column when it has none).
func implicitFKIndex(t *faithful.ScriptTable, ix MyIndex) bool {
	if ix.Unique {
		return false
	}
	for _, fk := range t.FKs {
		if len(fk.Columns) != len(ix.Columns) {
			continue
		}
		name := fk.Name
		if name == "" {
			name = fk.Columns[0]
		}
		if ix.Name != name {
			continue
		}
		same := true
		for i, c := range ix.Columns {
			if c.Name != fk.Columns[i] || c.Desc || c.Prefix > 0 {
				same = false
			}
		}
		if same {
			return true
		}
	}
	return false
}

func compareFKs(t *faithful.ScriptTable, mt *MyTable, add func(obj, aspect, want, got string)) {
	sig := func(cols []string, ref string, refCols []string, upd, del string, deferrable, deferred bool) string {
		s := fmt.Sprintf("(%s)->%s(%s) update %s delete %s", strings.Join(cols, ","), ref, strings.Join(refCols, ","), upd, del)
		if deferrable {
			s += " deferrable"
			if deferred {
				s += " initially deferred"
			}
		}
		return s
	}
	var want, got []string
	names := map[string]string{}
	for _, fk := range t.FKs {
		s := sig(fk.Columns, fk.RefTable, fk.RefColumns, fk.OnUpdate, fk.OnDelete, fk.Deferrable, fk.Deferred)
		want = append(want, s)
		if fk.Name != "" {
			names[s] = fk.Name
		}
	}
	for _, fk := range mt.FKs {
		s := sig(fk.Columns, fk.RefTable, fk.RefColumns, fk.OnUpdate, fk.OnDelete, fk.Deferrable, fk.Deferred)
		got = append(got, s)
		if n, ok := names[s]; ok && n != fk.Name {
			add(t.Name+"."+n, "foreign key name", n, fk.Name)
		}
	}
	sort.Strings(want)
	sort.Strings(got)
	add(t.Name, "foreign keys", strings.Join(want, "\n"), strings.Join(got, "\n"))
}

// checkNames checks that a table or column keeps the exact name it has in SQLite unless a name-renamed warning
// says it was renamed, and that every such warning is for a name that was in fact changed: a rename is never
// silent and a warning is never idle.
func checkNames(p *faithful.ScriptPlan, eng targetEngine, add func(obj, aspect, want, got string)) {
	warned := map[string]bool{}
	for _, w := range p.Warnings {
		if w.Kind == faithful.WarnNameRenamed {
			warned[w.Object] = true
		}
	}
	changed := 0
	for _, t := range p.Tables {
		if t.Src.Name != t.Name {
			changed++
			if !warned[t.Src.Name] {
				add(t.Src.Name, "name", "kept or a name-renamed warning", "renamed to "+t.Name+" without a warning")
			}
			if why := eng.unstorable(t.Src.Name); why == "" && !eng.tableTwin(p, t) {
				add(t.Src.Name, "rename justified", "a name "+eng.label+" cannot store", "renamed to "+t.Name+" though "+eng.label+" can store it")
			}
		}
		for _, c := range t.Columns {
			if c.Src.Name != c.Name {
				changed++
				if !warned[t.Src.Name+"."+c.Src.Name] {
					add(t.Src.Name+"."+c.Src.Name, "name", "kept or a name-renamed warning", "renamed to "+c.Name+" without a warning")
				}
				if eng.unstorable(c.Src.Name) == "" && !eng.columnTwin(t, c) {
					add(t.Src.Name+"."+c.Src.Name, "rename justified", "a name "+eng.label+" cannot store, or a twin among the columns", "renamed to "+c.Name+" though "+eng.label+" can store it")
				}
			}
		}
	}
	add("name-renamed warnings", "count", fmt.Sprint(changed), fmt.Sprint(len(warned)))
}

// unstorableName is why MySQL cannot store a name, by rules written here and not taken from the exporter: empty, ends
// in a space, longer than 64 characters, outside the Basic Multilingual Plane, a control or line-break character, not
// valid UTF-8. "" for a name it can store.
func unstorableName(name string) string {
	switch {
	case name == "":
		return "empty"
	case !utf8.ValidString(name):
		return "not valid UTF-8"
	case strings.HasSuffix(name, " "):
		return "ends in a space"
	case utf8.RuneCountInString(name) > 64:
		return "longer than 64 characters"
	}
	for _, r := range name {
		if r > 0xFFFF || r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f || r == 0x2028 || r == 0x2029 {
			return "a character it cannot hold"
		}
	}
	return ""
}

// hasCaseTwin is true when another column of the table is the same name up to the case MySQL folds (its table, not
// Unicode's: ß and ẞ, ς and σ are no twins there). Accents alone make no twin: MySQL keeps e and é apart.
func hasCaseTwin(t *faithful.ScriptTable, c *faithful.ScriptColumn) bool {
	for _, o := range t.Columns {
		if o != c && faithful.MySQLNameFold(o.Src.Name) == faithful.MySQLNameFold(c.Src.Name) {
			return true
		}
	}
	return false
}

// noTableTwin: MySQL keeps two tables whose names differ by case apart (on a case-sensitive server), so a
// rename of a table is never justified by a twin.
func noTableTwin(*faithful.ScriptPlan, *faithful.ScriptTable) bool { return false }

// unstorableMSSQL is why SQL Server cannot store a name, by rules written here and not taken from the exporter:
// empty, only spaces, not valid UTF-8, longer than 128 UTF-16 code units, holding "$(", a control or line-break
// character or a byte-order mark. "" for a name it can store.
func unstorableMSSQL(name string) string {
	switch {
	case name == "":
		return "empty"
	case !utf8.ValidString(name):
		return "not valid UTF-8"
	case strings.TrimRight(name, " ") == "":
		return "only spaces"
	case strings.Contains(name, "$("):
		return "holds $("
	}
	units := 0
	for _, r := range name {
		units++
		if r > 0xFFFF {
			units++
		}
		if r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f || r == 0x2028 || r == 0x2029 || r == 0xFEFF || r == 0xFFFE || r == 0xFFFF {
			return "a character it cannot hold"
		}
	}
	if units > 128 {
		return "longer than 128 characters"
	}
	return ""
}

func mssqlFold(name string) string { return strings.ToLower(strings.TrimRight(name, " ")) }

// mssqlTableTwin is true when another table has the name SQL Server's default collation would merge with this
// one: the table is renamed, which is justified.
func mssqlTableTwin(p *faithful.ScriptPlan, t *faithful.ScriptTable) bool {
	for _, o := range p.Tables {
		if o != t && mssqlFold(o.Src.Name) == mssqlFold(t.Src.Name) {
			return true
		}
	}
	return false
}

func mssqlColumnTwin(t *faithful.ScriptTable, c *faithful.ScriptColumn) bool {
	for _, o := range t.Columns {
		if o != c && mssqlFold(o.Src.Name) == mssqlFold(c.Src.Name) {
			return true
		}
	}
	return false
}
