package faithful

import (
	"fmt"
	"sort"
	"strings"
)

// The rules of this file need the whole plan: names that share a namespace, unique keys over NULLs, the
// foreign keys SQL Server accepts, the cascade graph, one type for the columns of a foreign key, and the key
// size budgets.

// ObjectNames are the names (tables, unique keys, foreign keys) that share the namespace of constraint names: SQL
// Server keeps the names of tables and of constraints in one list for the schema.
func (d mssqlDialect) ObjectNames(p *ScriptPlan) []string {
	names := []string{MySQLMarkerTable}
	for _, t := range p.Tables {
		names = append(names, t.Name)
		for _, u := range t.Uniques {
			if u.Name != "" {
				names = append(names, u.Name)
			}
		}
		for _, fk := range t.FKs {
			if fk.Name != "" {
				names = append(names, fk.Name)
			}
		}
	}
	return names
}

func (d mssqlDialect) Adjust(p *ScriptPlan) error {
	d.filterNullableUniques(p)
	d.dedupeObjectNames(p)
	d.dropUnsupportedFKs(p)
	d.limitCascades(p)
	classes := d.unifyFKClasses(p)
	return d.fitKeys(p, classes)
}

// ---- unique keys over NULLs ----

// filterNullableUniques writes a unique key whose nullable columns hold more than one NULL as a unique index
// filtered to the rows without NULL: SQLite never treats two NULLs as equal, SQL Server's UNIQUE does.
func (d mssqlDialect) filterNullableUniques(p *ScriptPlan) {
	for _, t := range p.Tables {
		filtered := func(names []string) string {
			var preds []string
			many := false
			for _, n := range names {
				c := t.Col(n)
				if !c.NotNull && c.Stats.nulls >= 2 {
					many = true
				}
				if !c.NotNull {
					preds = append(preds, d.QuoteIdent(n)+" IS NOT NULL")
				}
			}
			if !many {
				return ""
			}
			return strings.Join(preds, " AND ")
		}
		var keep []Unique
		for _, u := range t.Uniques {
			where := filtered(u.Columns)
			if where == "" {
				keep = append(keep, u)
				continue
			}
			ix := Index{Name: d.uniqueIndexName(t, u), Unique: true, Where: where}
			for _, n := range u.Columns {
				ix.Columns = append(ix.Columns, IndexColumn{Name: n})
			}
			t.Indexes = append(t.Indexes, ix)
			p.warn(WarnUniqueFiltered, t.Name+"."+ix.Name, "the unique key (%s) allows many NULLs in SQLite and one in SQL Server: it is written as a unique index filtered to the rows without NULL, which a foreign key cannot reference", strings.Join(u.Columns, ", "))
		}
		t.Uniques = keep
		for i := range t.Indexes {
			ix := &t.Indexes[i]
			if !ix.Unique || ix.Where != "" {
				continue
			}
			names := make([]string, len(ix.Columns))
			for j, c := range ix.Columns {
				names[j] = c.Name
			}
			if ix.Where = filtered(names); ix.Where != "" {
				p.warn(WarnUniqueFiltered, t.Name+"."+ix.Name, "the unique index (%s) allows many NULLs in SQLite and one in SQL Server: it is filtered to the rows without NULL, which a foreign key cannot reference", strings.Join(names, ", "))
			}
		}
	}
}

// uniqueIndexName is the name of the index a unique constraint becomes: its own name, or one made of the
// table and the columns, and never one another index or key of the table has.
func (d mssqlDialect) uniqueIndexName(t *ScriptTable, u Unique) string {
	used := map[string]bool{}
	for _, o := range t.Uniques {
		used[d.FoldName(o.Name)] = true
	}
	for _, ix := range t.Indexes {
		used[d.FoldName(ix.Name)] = true
	}
	name := u.Name
	if name == "" {
		name = "UQ_" + t.Name + "_" + strings.Join(u.Columns, "_")
	} else {
		delete(used, d.FoldName(u.Name)) // the constraint is replaced by the index of the same name
	}
	if d.StorableName(name) != "" || used[d.FoldName(name)] {
		name = safeName(d, name)
	}
	return name
}

// ---- names that share a namespace ----

// dedupeObjectNames drops the name of a unique constraint or foreign key that another object of the schema
// already has: SQL Server keeps one list of names for tables and constraints.
func (d mssqlDialect) dedupeObjectNames(p *ScriptPlan) {
	used := map[string]bool{d.FoldName(MySQLMarkerTable): true}
	for _, t := range p.Tables {
		used[d.FoldName(t.Name)] = true
	}
	for _, t := range p.Tables {
		for i := range t.Uniques {
			u := &t.Uniques[i]
			if u.Name == "" {
				continue
			}
			if used[d.FoldName(u.Name)] {
				p.warn(WarnConstraintNameLost, t.Name+"."+u.Name, "unique constraint name is not carried (the name is taken by another object of the schema): SQL Server names the key itself")
				u.Name = ""
				continue
			}
			used[d.FoldName(u.Name)] = true
		}
	}
	for _, t := range p.Tables {
		for i := range t.FKs {
			fk := &t.FKs[i]
			if fk.Name == "" {
				continue
			}
			if used[d.FoldName(fk.Name)] {
				p.warn(WarnConstraintNameLost, t.Name+"."+fk.Name, "foreign key name is not carried (the name is taken by a table or a unique constraint): SQL Server names the key itself")
				fk.Name = ""
				continue
			}
			used[d.FoldName(fk.Name)] = true
		}
	}
}

// ---- foreign keys ----

// dropUnsupportedFKs removes the foreign keys SQL Server cannot create and downgrades the actions it cannot
// carry (T-SQL has no RESTRICT and no deferral; SET DEFAULT needs a default; SET NULL needs nullable columns).
func (d mssqlDialect) dropUnsupportedFKs(p *ScriptPlan) {
	for _, t := range p.Tables {
		var keep []ForeignKey
		for _, fk := range t.FKs {
			label := fkLabel(t, fk)
			parent := p.Table(fk.RefTable)
			if parent == nil {
				p.warn(WarnFKNotCarried, label, "the referenced table %q is not part of the script", fk.RefTable)
				continue
			}
			ok := false
			for _, k := range parentKeys(parent) {
				if sameCols(k, fk.RefColumns) {
					ok = true
				}
			}
			if !ok {
				p.warn(WarnFKNotCarried, label, "the referenced columns (%s) of %s are no primary key, unique constraint or unfiltered unique index, which SQL Server requires", strings.Join(fk.RefColumns, ", "), parent.Name)
				continue
			}
			fix := func(action, which string) string {
				switch action {
				case "RESTRICT":
					p.warn(WarnFKActionNotCarried, label, "ON %s RESTRICT does not exist in T-SQL: the key is carried with NO ACTION, which refuses the same change when the key is not deferred", which)
					return "NO ACTION"
				case "SET DEFAULT":
					p.warn(WarnFKActionNotCarried, label, "ON %s SET DEFAULT is not carried: the key is carried with NO ACTION", which)
					return "NO ACTION"
				case "SET NULL":
					for _, c := range fk.Columns {
						if t.Col(c).NotNull {
							p.warn(WarnFKActionNotCarried, label, "ON %s SET NULL needs nullable columns and %s is NOT NULL: the key is carried with NO ACTION", which, c)
							return "NO ACTION"
						}
					}
				}
				return action
			}
			fk.OnUpdate, fk.OnDelete = fix(fk.OnUpdate, "UPDATE"), fix(fk.OnDelete, "DELETE")
			if fk.Deferrable {
				p.warn(WarnFKActionNotCarried, label, "DEFERRABLE is not supported by SQL Server: the key is checked at once")
				fk.Deferrable, fk.Deferred = false, false
			}
			keep = append(keep, fk)
		}
		t.FKs = keep
	}
}

// cascadeEdge is a foreign key as the cascade analysis sees it: the table it references (the parent) and the
// table that holds it (the child), with the two actions it carries.
type cascadeEdge struct{ parent, child, onDelete, onUpdate string }

// cascadeGraph holds the foreign keys that carry an action.
type cascadeGraph struct{ edges []cascadeEdge }

func cascades(action string) bool { return action != "" && action != "NO ACTION" }

// next lists the tables a change of a row of table reaches. A DELETE of a parent row deletes the child rows
// of a CASCADE key; a SET NULL key updates them instead, and the update goes on from there (SQL Server follows
// the update of a row whatever column was changed). An UPDATE of a parent row updates the child rows of every
// key with an ON UPDATE action, and, to be safe, of every SET NULL key too: SQL Server counts a SET NULL key
// among the keys that update, in a way this analysis was measured against (SQL Server 2022: the journey and a
// sweep of 400 random graphs), not derived from. The rule can refuse a graph SQL Server would take (2% of the
// swept downgrades, all in graphs with a SET NULL key); the sweep never found a graph it keeps that SQL Server refuses.
func (g *cascadeGraph) next(table string, deleting bool) []cascadeStep {
	var out []cascadeStep
	for _, e := range g.edges {
		if e.parent != table {
			continue
		}
		switch {
		case deleting && e.onDelete == "CASCADE":
			out = append(out, cascadeStep{e.child, true})
		case deleting && cascades(e.onDelete):
			out = append(out, cascadeStep{e.child, false})
		case !deleting && (cascades(e.onUpdate) || e.onDelete == "SET NULL"):
			out = append(out, cascadeStep{e.child, false})
		}
	}
	return out
}

type cascadeStep struct {
	table    string
	deleting bool
}

// refusal says why SQL Server refuses the graph, "" when it accepts it: SQL Server refuses a graph in which a
// DELETE or an UPDATE of a row of one table reaches a table twice, by two paths or by a cycle (error 1785).
func (g *cascadeGraph) refusal() string {
	roots := map[string]bool{}
	for _, e := range g.edges {
		roots[e.parent] = true
	}
	names := make([]string, 0, len(roots))
	for r := range roots {
		names = append(names, r)
	}
	sort.Strings(names)
	for _, root := range names {
		for _, deleting := range []bool{true, false} {
			seen := map[string]bool{root: true}
			var walk func(table string, deleting bool) string
			walk = func(table string, deleting bool) string {
				for _, st := range g.next(table, deleting) {
					if seen[st.table] {
						return st.table
					}
					seen[st.table] = true
					if hit := walk(st.table, st.deleting); hit != "" {
						return hit
					}
				}
				return ""
			}
			if hit := walk(root, deleting); hit != "" {
				op := "an UPDATE"
				if deleting {
					op = "a DELETE"
				}
				if hit == root {
					return fmt.Sprintf("%s of a row of %s reaches %s again, a cycle", op, root, root)
				}
				return fmt.Sprintf("%s of a row of %s reaches %s by two paths", op, root, hit)
			}
		}
	}
	return ""
}

// limitCascades downgrades the actions SQL Server refuses to create. The keys are taken in the order of the
// plan (tables as planned, keys as the table lists them); a key keeps both actions when the graph stays valid,
// otherwise its ON UPDATE action goes, then (keeping the update) its ON DELETE action, then both; the first
// that fits is kept, so the result is the same on every run.
func (d mssqlDialect) limitCascades(p *ScriptPlan) {
	g := &cascadeGraph{}
	for _, t := range p.Tables {
		for i := range t.FKs {
			fk := &t.FKs[i]
			if !cascades(fk.OnDelete) && !cascades(fk.OnUpdate) {
				continue
			}
			del, upd := fk.OnDelete, fk.OnUpdate
			if !cascades(del) {
				del = "NO ACTION"
			}
			if !cascades(upd) {
				upd = "NO ACTION"
			}
			no := "NO ACTION"
			tries := [][2]string{{del, upd}}
			if cascades(del) && cascades(upd) {
				tries = append(tries, [2]string{del, no}, [2]string{no, upd})
			}
			tries = append(tries, [2]string{no, no})
			var why string
			for k, try := range tries {
				fk.OnDelete, fk.OnUpdate = try[0], try[1]
				if k == len(tries)-1 {
					break // no action at all adds nothing to the graph
				}
				g.edges = append(g.edges, cascadeEdge{fk.RefTable, t.Name, try[0], try[1]})
				reason := g.refusal()
				if reason == "" {
					break
				}
				g.edges = g.edges[:len(g.edges)-1]
				if k == 0 {
					why = reason
				}
			}
			for _, lost := range []struct{ was, now, which string }{{del, fk.OnDelete, "DELETE"}, {upd, fk.OnUpdate, "UPDATE"}} {
				if cascades(lost.was) && !cascades(lost.now) {
					p.warn(WarnFKActionNotCarried, fkLabel(t, *fk), "ON %s %s is not carried: SQL Server refuses a foreign key that makes a cycle or a second cascade path (%s): the key is carried with NO ACTION", lost.which, lost.was, why)
				}
			}
		}
	}
}

// ---- one type for the columns of a foreign key ----

// mssqlClass is one equivalence class; before holds each member's type before the class was unified.
type mssqlClass struct {
	members []*ScriptColumn
	before  []string
}

// unifyFKClasses gives every column of a foreign key equivalence class (the child columns and the parent
// columns they reference, transitively) one type: SQL Server refuses a key between columns whose types or
// lengths differ.
func (d mssqlDialect) unifyFKClasses(p *ScriptPlan) []*mssqlClass {
	parent := map[*ScriptColumn]*ScriptColumn{}
	var find func(c *ScriptColumn) *ScriptColumn
	find = func(c *ScriptColumn) *ScriptColumn {
		if parent[c] == nil || parent[c] == c {
			parent[c] = c
			return c
		}
		parent[c] = find(parent[c])
		return parent[c]
	}
	for _, t := range p.Tables {
		for _, fk := range t.FKs {
			pt := p.Table(fk.RefTable)
			for i, cn := range fk.Columns {
				a, b := find(t.Col(cn)), find(pt.Col(fk.RefColumns[i]))
				if a != b {
					parent[a] = b
				}
			}
		}
	}
	byRoot := map[*ScriptColumn]*mssqlClass{}
	var classes []*mssqlClass
	for _, t := range p.Tables {
		for _, c := range t.Columns {
			if !c.Use.Keyed() {
				continue
			}
			r := find(c)
			if byRoot[r] == nil {
				byRoot[r] = &mssqlClass{}
				classes = append(classes, byRoot[r])
			}
			byRoot[r].members = append(byRoot[r].members, c)
		}
	}
	for _, cl := range classes {
		if len(cl.members) > 1 {
			d.mergeClass(p, cl)
		}
	}
	return classes
}

func (d mssqlDialect) mergeClass(p *ScriptPlan, cl *mssqlClass) {
	same := true
	fam := cl.members[0].Choice.Family
	for _, m := range cl.members {
		if m.Choice.Family != fam {
			same = false
		}
	}
	cl.before = make([]string, len(cl.members))
	for i, m := range cl.members {
		cl.before[i] = m.Choice.Type
	}
	merged := cl.members[0].Choice
	if same {
		for _, m := range cl.members[1:] {
			c := m.Choice
			merged.Bits = max(merged.Bits, c.Bits)
			merged.Len = max(merged.Len, c.Len)
			merged.Tight = max(merged.Tight, c.Tight)
			merged.Fsp = max(merged.Fsp, c.Fsp)
			merged.Scale = max(merged.Scale, c.Scale)
			merged.Bounded = merged.Bounded || c.Bounded
		}
		if fam == "decimal" {
			intDigits := 0
			for _, m := range cl.members {
				intDigits = max(intDigits, m.Choice.Prec-m.Choice.Scale)
			}
			merged.Prec = min(intDigits+merged.Scale, 38)
		}
	} else {
		hasBytes := false
		need := mssqlDefaultKeyLen
		for _, m := range cl.members {
			hasBytes = hasBytes || m.Choice.Family == "bytes"
			need = max(need, m.Choice.Len, 40)
		}
		merged = Choice{Kind: KindText, Family: "text", Len: need, Bounded: true, Tight: need}
		if hasBytes {
			merged = Choice{Kind: KindBytes, Family: "bytes", Len: need, Bounded: true, Tight: need}
		}
	}
	for _, m := range cl.members {
		n, ex, note, bounded := m.Choice.Normalized, m.Choice.Example, m.Choice.Note, m.Choice.Bounded
		ch := merged
		ch.Normalized, ch.Example, ch.Note = n, ex, note
		if !same || m.Choice.Kind == KindBool {
			ch.Bounded = bounded
		}
		ch.MaxSize = m.Choice.MaxSize
		mssqlRender(&ch)
		m.Choice = ch
	}
}

// ---- key sizes ----

func mssqlKeyBytes(c Choice) int {
	const unindexable = 1 << 20
	switch c.Family {
	case "int":
		return c.Bits / 8
	case "bool":
		return 1
	case "decimal":
		switch {
		case c.Prec <= 9:
			return 5
		case c.Prec <= 19:
			return 9
		case c.Prec <= 28:
			return 13
		}
		return 17
	case "date":
		return 3
	case "datetime":
		return 6 + (c.Fsp+1)/3
	case "time":
		return 3 + (c.Fsp+1)/3
	case "text":
		if c.Len == 0 || c.Len > mssqlMaxNVarchar {
			return unindexable
		}
		return 2 * c.Len
	case "bytes":
		if c.Len == 0 || c.Len > mssqlMaxVarbinary {
			return unindexable
		}
		return c.Len
	}
	return 8 // float
}

func mssqlKeyCost(cols []*ScriptColumn) int {
	n := 0
	for _, c := range cols {
		n += mssqlKeyBytes(c.Choice)
	}
	return n
}

// fitKeys makes every key fit the index limits of SQL Server (900 bytes for the clustered primary key, 1,700
// for any other key, 32 columns). A text or binary key column gets a bounded type: a generous one first
// (NVARCHAR(255) for a column that declares no length), the length of its longest value when the key is too
// long otherwise.
func (d mssqlDialect) fitKeys(p *ScriptPlan, classes []*mssqlClass) error {
	classOf := map[*ScriptColumn]*mssqlClass{}
	for _, cl := range classes {
		for _, m := range cl.members {
			classOf[m] = cl
		}
	}
	set := func(cl *mssqlClass, length int) {
		for _, m := range cl.members {
			m.Choice.Len = length
			mssqlRender(&m.Choice)
		}
	}
	for _, cl := range classes {
		if c0 := cl.members[0].Choice; c0.Family == "text" && c0.Bounded && c0.Len < mssqlDefaultKeyLen {
			set(cl, mssqlDefaultKeyLen)
		}
	}
	fits := func(kind string, cols []*ScriptColumn) bool {
		limit := mssqlMaxIndexKey
		if kind == "primary key" {
			limit = mssqlMaxClusteredKey
		}
		return mssqlKeyCost(cols) <= limit && len(cols) <= mssqlMaxKeyColumns
	}
	for _, t := range p.Tables {
		sets, kinds := t.keySets()
		for i := 0; i < len(sets); i++ {
			if kinds[i] == "foreign key" || fits(kinds[i], sets[i]) {
				continue // a foreign key needs no index in SQL Server
			}
			for _, c := range sets[i] {
				if cl := classOf[c]; cl != nil && (c.Choice.Family == "text" || c.Choice.Family == "bytes") {
					tight := 1
					for _, m := range cl.members {
						tight = max(tight, m.Choice.Tight)
					}
					set(cl, tight)
					for _, m := range cl.members {
						m.Choice.Bounded = true
					}
				}
			}
			if fits(kinds[i], sets[i]) {
				continue
			}
			if strings.HasPrefix(kinds[i], "index ") || strings.HasPrefix(kinds[i], "unique index ") {
				name := strings.TrimPrefix(strings.TrimPrefix(kinds[i], "unique "), "index ")
				p.warn(WarnIndexNotCarried, t.Name+"."+name, "the key is longer than the %d bytes SQL Server allows in an index key even with every column at the length of its longest value", mssqlMaxIndexKey)
				var keep []Index
				for _, ix := range t.Indexes {
					if ix.Name != name {
						keep = append(keep, ix)
					}
				}
				t.Indexes = keep
				sets, kinds = t.keySets()
				i = -1
				continue
			}
			return fmt.Errorf("%s of table %q is longer than SQL Server allows (%d bytes, at most %d, at most %d columns), even with every column at the length of its longest value; SQL Server cannot carry it",
				kinds[i], t.Name, mssqlKeyCost(sets[i]), map[bool]int{true: mssqlMaxClusteredKey, false: mssqlMaxIndexKey}[kinds[i] == "primary key"], mssqlMaxKeyColumns)
		}
	}
	// The unification warning names the final type, after the key budgets had their say.
	for _, cl := range classes {
		for i, m := range cl.members {
			if len(cl.before) > i && m.Choice.Type != cl.before[i] {
				p.warn(WarnTypeWidened, m.Object, "declared %q and carried as %s, but it joins the columns of its foreign keys, which SQL Server requires to have one type: carried as %s", strings.TrimSpace(m.Src.SourceType), cl.before[i], m.Choice.Type)
			}
		}
	}
	for _, t := range p.Tables {
		for _, c := range t.Columns {
			if c.Use.Keyed() && c.Choice.Bounded && (c.Choice.Family == "text" || c.Choice.Family == "bytes") {
				p.warn(WarnKeyTypeBounded, c.Object, "a key column is carried as %s (declared %q): SQL Server cannot index an unbounded type; its longest value is %d", c.Choice.Type, strings.TrimSpace(c.Src.SourceType), longestValue(c))
			}
		}
	}
	return nil
}
