package faithful

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var reWithoutRowid = regexp.MustCompile(`(?i)\bWITHOUT\s+ROWID\b`)

// rowidName is the name that reads the rowid of a table whose columns may hide the usual names.
func rowidName(t *Table) string {
	for _, n := range []string{"rowid", "_rowid_", "oid"} {
		if t.Column(n) == nil {
			// a column of another case would shadow it too
			shadow := false
			for _, c := range t.Columns {
				if strings.EqualFold(c.Name, n) {
					shadow = true
				}
			}
			if !shadow {
				return n
			}
		}
	}
	return ""
}

// sourceOrder is the ORDER BY of the SQLite query of a table: the primary key and then the rowid, so the order
// is total (a primary key of SQLite may hold several NULLs, and a table may have none).
func sourceOrder(t *Table) (string, error) {
	rid := ""
	if !reWithoutRowid.MatchString(t.CreateSQL) {
		if rid = rowidName(t); rid == "" {
			return "", fmt.Errorf("table %q hides its rowid behind columns; its rows have no order the script can fix", t.Name)
		}
	}
	var parts []string
	for _, c := range t.PK {
		parts = append(parts, quoteIdent(c))
	}
	if rid != "" {
		parts = append(parts, rid)
	}
	return strings.Join(parts, ", "), nil
}

type namePlan struct {
	tables map[string]string
	cols   map[string]map[string]string
}

// PlanSQLiteTo decides how every table of a SQLite catalog is created by a script of the dialect. stats is the
// pre-scan of every column, by table. The plan is deterministic.
func PlanSQLiteTo(ctx context.Context, d Dialect, db Queryer, cat *Catalog, stats map[string][]*ScriptStats) (*ScriptPlan, error) {
	if len(cat.Tables) == 0 {
		return nil, ErrNoTables
	}
	p := &ScriptPlan{Dialect: d}
	p.Warnings = append(p.Warnings, cat.Notes...)
	names, err := planNames(d, p, cat)
	if err != nil {
		return nil, err
	}
	// Foreign keys whose rows the source itself violates cannot be created after the load.
	skipFK := map[string]map[int]bool{}
	for _, t := range cat.Tables {
		for i, fk := range t.FKs {
			n, err := orphans(ctx, db, cat, t, fk)
			if err != nil {
				p.warn(WarnFKNotCarried, t.Name+"("+strings.Join(fk.Columns, ",")+")", "the key could not be checked against the data (%v)", err)
			} else if n == 0 {
				continue
			} else {
				p.warn(WarnForeignKeyViolations, t.Name+"("+strings.Join(fk.Columns, ",")+")", "%d row(s) have no parent in %s: the key is not created, because MySQL would refuse it", n, fk.RefTable)
			}
			if skipFK[t.Name] == nil {
				skipFK[t.Name] = map[int]bool{}
			}
			skipFK[t.Name][i] = true
		}
	}
	for _, t := range cat.Tables {
		st, err := buildScriptTable(d, p, t, names, stats[t.Name], skipFK[t.Name])
		if err != nil {
			return nil, err
		}
		p.Tables = append(p.Tables, st)
	}
	dedupeFKNames(d, p)
	// Use: where a column is indexed.
	for _, t := range p.Tables {
		for _, n := range t.PK {
			t.Col(n).Use.PK = true
		}
		for _, u := range t.Uniques {
			for _, n := range u.Columns {
				t.Col(n).Use.Unique = true
			}
		}
		for _, ix := range t.Indexes {
			for _, c := range ix.Columns {
				t.Col(c.Name).Use.Index = true
			}
		}
		for _, fk := range t.FKs {
			for _, n := range fk.Columns {
				t.Col(n).Use.FK = true
			}
			if pt := p.Table(fk.RefTable); pt != nil {
				for _, n := range fk.RefColumns {
					if c := pt.Col(n); c != nil {
						c.Use.FK = true
					}
				}
			}
		}
	}
	for _, t := range p.Tables {
		for _, c := range t.Columns {
			c.Choice = d.Choose(c.Src, c.Stats, c.Use)
			if c.Choice.Note != "" {
				p.warn(WarnTypeWidened, t.Name+"."+c.Name, "%s", c.Choice.Note)
			}
		}
		finishColumns(t)
	}
	if err := d.Adjust(p); err != nil {
		return nil, err
	}
	for _, t := range p.Tables {
		for _, c := range t.Columns {
			if c.Choice.Normalized > 0 {
				p.warn(WarnValuesNormalized, t.Name+"."+c.Name, "%d text value(s) are written in the spelling of %s (%s)", c.Choice.Normalized, c.Choice.Type, c.Choice.Example)
			}
		}
		translateDefaults(d, p, t)
		translateChecks(d, p, t)
	}
	warnRealsNormalized(p)
	if err := warnKeyCollisions(ctx, d, db, p); err != nil {
		return nil, err
	}
	sortWarnings(p.Warnings)
	return p, nil
}

// warnRealsNormalized says what a DECIMAL column does to the REAL values it takes. SQLite keeps a REAL as a binary
// float (and an INTEGER and a REAL of one number apart, 5 and 5.0); a DECIMAL holds the exact decimal of its
// shortest spelling, and both spellings of one number become one value.
func warnRealsNormalized(p *ScriptPlan) {
	for _, t := range p.Tables {
		for _, c := range t.Columns {
			st := c.Stats
			if c.Choice.Family != "decimal" || st.reals == 0 {
				continue
			}
			if st.ints > 0 {
				p.warn(WarnValuesNormalized, t.Name+"."+c.Name, "%d REAL and %d INTEGER value(s) share one %s column: SQLite keeps them apart (5 and 5.0), %s does not; each REAL is written as the exact decimal of its shortest spelling, for example %s", st.reals, st.ints, c.Choice.Type, c.Choice.Type, st.RealExample)
			} else {
				p.warn(WarnValuesNormalized, t.Name+"."+c.Name, "%d REAL value(s) are written as the exact decimal of their shortest spelling, for example %s: SQLite stores a binary float, %s an exact decimal", st.reals, st.RealExample, c.Choice.Type)
			}
		}
	}
}

// orphans counts the rows of t whose foreign key has no parent row, by SQLite's own comparison.
func orphans(ctx context.Context, db Queryer, cat *Catalog, t *Table, fk ForeignKey) (int64, error) {
	parent := cat.Table(fk.RefTable)
	if parent == nil {
		return 0, nil
	}
	var notNull, match []string
	for i, c := range fk.Columns {
		notNull = append(notNull, "c."+quoteIdent(c)+" IS NOT NULL")
		if i >= len(fk.RefColumns) {
			return 0, fmt.Errorf("the key names fewer referenced columns than columns")
		}
		match = append(match, "p."+quoteIdent(fk.RefColumns[i])+" = c."+quoteIdent(c))
	}
	q := "SELECT count(*) FROM " + quoteIdent(t.Name) + " c WHERE " + strings.Join(notNull, " AND ") +
		" AND NOT EXISTS (SELECT 1 FROM " + quoteIdent(parent.Name) + " p WHERE " + strings.Join(match, " AND ") + ")"
	var n int64
	err := db.QueryRowContext(ctx, q).Scan(&n)
	return n, err
}

// planNames decides the target name of every table and column. A name that MySQL cannot store is replaced
// (a counted warning); names that differ only by case are reported.
func planNames(d Dialect, p *ScriptPlan, cat *Catalog) (*namePlan, error) {
	np := &namePlan{tables: map[string]string{}, cols: map[string]map[string]string{}}
	byFold := map[string][]string{}
	usedTables := map[string]bool{}
	for _, t := range cat.Tables {
		name := t.Name
		if why := d.StorableName(name); why != "" {
			name = safeName(d, name)
			p.warn(WarnNameRenamed, t.Name, "table name cannot be stored (%s): created as %q", why, name)
		}
		if usedTables[name] {
			return nil, fmt.Errorf("table name %q is taken twice after renaming", name)
		}
		usedTables[name] = true
		if name == MySQLMarkerTable {
			return nil, fmt.Errorf("table %q is the name the script uses for its completion marker; rename the table first", name)
		}
		np.tables[t.Name] = name
		low := strings.ToLower(name)
		byFold[low] = append(byFold[low], name)
		cols := map[string]string{}
		used := map[string]bool{}
		for _, c := range t.Columns {
			cn := c.Name
			why := d.StorableName(cn)
			if why == "" && used[foldName(cn)] {
				why = "it differs only by case from another column of the table, and MySQL compares column names without case"
			}
			if why != "" {
				cn = safeName(d, cn)
				p.warn(WarnNameRenamed, t.Name+"."+c.Name, "column name cannot be stored (%s): created as %q", why, cn)
			}
			if used[foldName(cn)] {
				return nil, fmt.Errorf("column name %q of table %q is taken twice after renaming", cn, t.Name)
			}
			used[foldName(cn)] = true
			cols[c.Name] = cn
		}
		np.cols[t.Name] = cols
	}
	var folds []string
	for k, v := range byFold {
		if len(v) > 1 {
			folds = append(folds, k)
		}
	}
	sort.Strings(folds)
	for _, k := range folds {
		v := byFold[k]
		sort.Strings(v)
		p.warn(WarnNameCaseCollision, strings.Join(v, " / "), "these table names differ only by case: the script loads on a server with case-sensitive table names (lower_case_table_names=0, the Linux default) and fails on one with case-insensitive names (macOS and Windows)")
	}
	return np, nil
}

func buildScriptTable(d Dialect, p *ScriptPlan, t *Table, np *namePlan, stats []*ScriptStats, skip map[int]bool) (*ScriptTable, error) {
	cmap := np.cols[t.Name]
	st := &ScriptTable{Src: t, Name: np.tables[t.Name], Rows: t.Rows}
	for i, c := range t.Columns {
		st.Columns = append(st.Columns, &ScriptColumn{Object: st.Name + "." + cmap[c.Name], Src: c, Name: cmap[c.Name], Stats: stats[i]})
	}
	mapCols := func(names []string) []string {
		out := make([]string, len(names))
		for i, n := range names {
			out[i] = cmap[n]
		}
		return out
	}
	order, err := sourceOrder(t)
	if err != nil {
		return nil, err
	}
	st.OrderBy = order
	st.PK = mapCols(t.PK)
	for _, n := range st.PK {
		if s := st.Col(n).Stats; s.nulls > 0 {
			return nil, fmt.Errorf("primary key column %s.%s holds %d NULL value(s) (SQLite allows it); MySQL cannot carry it", t.Name, n, s.nulls)
		}
	}
	used := map[string]bool{"primary": true}
	for _, u := range t.Uniques {
		nu := Unique{Columns: mapCols(u.Columns)}
		if u.Name != "" {
			if why := d.StorableName(u.Name); why != "" || used[foldName(u.Name)] {
				p.warn(WarnConstraintNameLost, t.Name+"."+u.Name, "unique constraint name is not carried (%s)", firstNonEmpty(why, "the name is taken by another key of the table"))
			} else {
				nu.Name = u.Name
				used[foldName(u.Name)] = true
			}
		}
		st.Uniques = append(st.Uniques, nu)
	}
	for _, ix := range t.Indexes {
		if ix.Where != "" {
			p.warn(WarnIndexNotCarried, t.Name+"."+ix.Name, "partial index (WHERE %s) is not carried: MySQL has no partial indexes", ix.Where)
			continue
		}
		ni := Index{Name: ix.Name, Unique: ix.Unique}
		for _, c := range ix.Columns {
			ni.Columns = append(ni.Columns, IndexColumn{Name: cmap[c.Name], Desc: c.Desc})
		}
		if why := d.StorableName(ix.Name); why != "" || used[foldName(ix.Name)] {
			ni.Name = safeName(d, ix.Name)
			p.warn(WarnIndexRenamed, t.Name+"."+ix.Name, "index name cannot be used (%s): created as %q", firstNonEmpty(why, "the name is taken by another key of the table"), ni.Name)
		}
		used[foldName(ni.Name)] = true
		st.Indexes = append(st.Indexes, ni)
	}
	for i, fk := range t.FKs {
		if skip[i] {
			continue
		}
		pcols := np.cols[fk.RefTable]
		nf := fk
		nf.Columns = mapCols(fk.Columns)
		if pcols != nil {
			nf.RefTable = np.tables[fk.RefTable]
			nf.RefColumns = make([]string, len(fk.RefColumns))
			for j, c := range fk.RefColumns {
				nf.RefColumns[j] = pcols[c]
			}
		}
		st.FKs = append(st.FKs, nf)
	}
	return st, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// finishColumns sets nullability and the auto-increment column of a table after the types are chosen.
func finishColumns(t *ScriptTable) {
	inPK := map[string]bool{}
	for _, n := range t.PK {
		inPK[n] = true
	}
	for _, c := range t.Columns {
		c.NotNull = c.Src.NotNull || inPK[c.Name]
		if c.Src.Identity != IdentityNone { // an INTEGER PRIMARY KEY: its values are integers
			c.AutoInc, c.NotNull = true, true
		}
	}
}

func translateDefaults(d Dialect, p *ScriptPlan, t *ScriptTable) {
	for _, c := range t.Columns {
		if c.Src.Default == "" || c.AutoInc {
			continue
		}
		def, note, ok := d.Default(c.Src.Default, c.Choice)
		if !ok {
			p.warn(WarnDefaultNotCarried, t.Name+"."+c.Name, "default expression %s cannot be translated to MySQL and is not carried", c.Src.Default)
			continue
		}
		c.Default = def
		if note != "" {
			p.warn(WarnDefaultTranslated, t.Name+"."+c.Name, "%s", note)
		}
		if def != "" && c.Choice.Family == "decimal" && parseDecl(c.Src.SourceType).text {
			p.warn(WarnDefaultTranslated, t.Name+"."+c.Name, "text default %s of a decimal text column became the numeric default %s", c.Src.Default, def)
		}
	}
}

func translateChecks(d Dialect, p *ScriptPlan, t *ScriptTable) {
	usedNames := map[string]bool{}
	for _, o := range p.Tables {
		for _, fk := range o.FKs {
			if fk.Name != "" {
				usedNames[foldName(fk.Name)] = true
			}
		}
	}
	for i, ck := range t.Src.Checks {
		obj := fmt.Sprintf("%s.check #%d", t.Name, i+1)
		if ck.Name != "" {
			obj = t.Name + "." + ck.Name
		}
		expr, note, ok := d.Check(ck.Expr, t)
		if !ok {
			p.warn(WarnCheckNotCarried, obj, "check constraint %q is not carried: %s", ck.Expr, note)
			continue
		}
		if note != "" {
			p.warn(WarnCollationNotCarried, obj, "%s", note)
		}
		nc := Check{Expr: expr}
		if ck.Name != "" {
			if why := d.StorableName(ck.Name); why != "" || usedNames[foldName(ck.Name)] {
				p.warn(WarnConstraintNameLost, obj, "check constraint name is not carried (%s)", firstNonEmpty(why, "the name is taken by another constraint of the schema"))
			} else {
				nc.Name = ck.Name
				usedNames[foldName(ck.Name)] = true
			}
		}
		t.Checks = append(t.Checks, nc)
	}
}

// dedupeFKNames keeps the name of a foreign key when MySQL can use it: a constraint name is unique in the
// whole schema, at most 64 characters and storable; otherwise MySQL names the key itself.
func dedupeFKNames(d Dialect, p *ScriptPlan) {
	used := map[string]bool{}
	for _, t := range p.Tables {
		for i := range t.FKs {
			fk := &t.FKs[i]
			if fk.Name == "" {
				continue
			}
			if why := d.StorableName(fk.Name); why != "" || used[foldName(fk.Name)] {
				p.warn(WarnConstraintNameLost, t.Name+"."+fk.Name, "foreign key name is not carried (%s): MySQL names the key itself", firstNonEmpty(why, "the name is taken by another constraint of the schema"))
				fk.Name = ""
				continue
			}
			used[foldName(fk.Name)] = true
		}
	}
}
