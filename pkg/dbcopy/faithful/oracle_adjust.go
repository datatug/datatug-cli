package faithful

import (
	"fmt"
	"sort"
	"strings"
)

// The rules of this file need the whole plan: the empty texts Oracle calls NULL, indexes Oracle refuses as duplicates,
// the namespaces of constraint and index names, unique keys over NULLs, the foreign keys Oracle accepts, one type for
// the columns of a foreign key, and the key size budgets.

// ObjectNames are the names (unique keys, foreign keys) that share the namespace of constraint names: Oracle keeps
// the names of the constraints of a schema in one list, apart from the names of tables and indexes.
func (d oracleDialect) ObjectNames(p *ScriptPlan) []string {
	var names []string
	for _, t := range p.Tables {
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

func (d oracleDialect) Adjust(p *ScriptPlan) error {
	if err := d.normalizeValues(p); err != nil {
		return err
	}
	d.dropRedundantIndexes(p)
	d.promoteUniqueIndexes(p)
	d.filterCompositeUniques(p)
	d.dedupeObjectNames(p)
	d.dropUnsupportedFKs(p)
	classes := d.unifyFKClasses(p)
	d.dropIdentityOfText(p)
	return d.fitKeys(p, classes)
}

// dropIdentityOfText removes the identity of a column that the foreign key class turned into text: an identity column is a number.
func (d oracleDialect) dropIdentityOfText(p *ScriptPlan) {
	for _, t := range p.Tables {
		for _, c := range t.Columns {
			if c.AutoInc && c.Choice.Family != "int" {
				c.AutoInc = false
				p.warn(WarnIdentityNotCarried, c.Object, "the column is carried as %s, which cannot be an identity column: its counter is not carried", c.Choice.Type)
			}
		}
	}
}

// ---- values Oracle does not keep as they are ----

// oracleEmpties is the number of empty values of a column that Oracle stores as NULL: an empty text, and an empty
// RAW (a BLOB keeps an empty value).
func oracleEmpties(c *ScriptColumn) int64 {
	switch {
	case c.Choice.Family == "text":
		return c.Stats.Empty
	case c.Choice.Family == "bytes" && c.Choice.Len > 0:
		return c.Stats.Empty + c.Stats.EmptyBlobs
	}
	return 0
}

// normalizeValues counts what Oracle changes in the values and says so: an empty text is NULL (a column that holds
// one is created without NOT NULL, and a key column cannot).
func (d oracleDialect) normalizeValues(p *ScriptPlan) error {
	for _, t := range p.Tables {
		for _, c := range t.Columns {
			if n := oracleEmpties(c); n > 0 {
				if c.Use.PK {
					return fmt.Errorf("primary key column %s holds %d empty value(s), which Oracle stores as NULL and a primary key refuses", c.Object, n)
				}
				if c.NotNull && !c.AutoInc {
					c.NotNull = false
					p.warn(WarnValuesNormalized, c.Object, "%d empty value(s) become NULL, because Oracle has no empty text: the column is created without NOT NULL", n)
				} else {
					p.warn(WarnValuesNormalized, c.Object, "%d empty value(s) become NULL, because Oracle has no empty text", n)
				}
			}
		}
	}
	return nil
}

// manyNulls is true when a column of the key can be NULL and holds two NULLs or more: only then can a key of SQLite hold two rows that
// Oracle's unique index calls equal.
func manyNulls(t *ScriptTable, names []string) bool {
	for _, n := range names {
		if c := t.Col(n); !c.NotNull && oracleNulls(c) >= 2 {
			return true
		}
	}
	return false
}

// oracleNulls is the number of values of a column that Oracle holds as NULL.
func oracleNulls(c *ScriptColumn) int64 { return c.Stats.nulls + oracleEmpties(c) }

// ---- indexes ----

func colsSig(names []string, desc []bool) string {
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = n
		if desc != nil && desc[i] {
			parts[i] += " DESC"
		}
	}
	return strings.Join(parts, "\x1f")
}

func indexSig(ix Index) string {
	names := make([]string, len(ix.Columns))
	desc := make([]bool, len(ix.Columns))
	for i, c := range ix.Columns {
		names[i], desc[i] = c.Name, c.Desc
	}
	return colsSig(names, desc)
}

// dropRedundantIndexes drops what Oracle refuses as a copy of another key of the table: an index with the column list
// (and the sort order) of the primary key, of a unique constraint or of another index fails with ORA-01408, and a
// second unique key over the same columns with ORA-02261. A unique index is kept before a plain one.
func (d oracleDialect) dropRedundantIndexes(p *ScriptPlan) {
	for _, t := range p.Tables {
		held := map[string]string{}
		if len(t.PK) > 0 {
			held[colsSig(t.PK, nil)] = "the primary key"
		}
		var uniques []Unique
		for _, u := range t.Uniques {
			sig := colsSig(u.Columns, nil)
			if what, ok := held[sig]; ok {
				p.warn(WarnIndexNotCarried, t.Name+"."+firstNonEmpty(u.Name, "("+strings.Join(u.Columns, ",")+")"), "the unique key (%s) repeats %s, which Oracle refuses (ORA-02261): it is not created again", strings.Join(u.Columns, ", "), what)
				continue
			}
			held[sig] = "the unique key " + firstNonEmpty(u.Name, "("+strings.Join(u.Columns, ",")+")")
			uniques = append(uniques, u)
		}
		t.Uniques = uniques
		order := make([]int, len(t.Indexes))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool { return t.Indexes[order[a]].Unique && !t.Indexes[order[b]].Unique })
		drop := map[int]bool{}
		for _, i := range order {
			ix := t.Indexes[i]
			sig := indexSig(ix)
			if what, ok := held[sig]; ok {
				p.warn(WarnIndexNotCarried, t.Name+"."+ix.Name, "the index has the columns of %s, which Oracle refuses (ORA-01408): it is not created", what)
				drop[i] = true
				continue
			}
			held[sig] = "the index " + ix.Name
		}
		if len(drop) > 0 {
			var keep []Index
			for i, ix := range t.Indexes {
				if !drop[i] {
					keep = append(keep, ix)
				}
			}
			t.Indexes = keep
		}
	}
}

// promoteUniqueIndexes makes a unique constraint of the unique index a foreign key references: Oracle takes only a
// primary key or a unique constraint as the parent of a key, never a unique index (ORA-02270). The constraint keeps
// the name of the index, and so does its index; nothing is lost, so there is no warning.
func (d oracleDialect) promoteUniqueIndexes(p *ScriptPlan) {
	referenced := func(t *ScriptTable, cols []string) bool {
		for _, o := range p.Tables {
			for _, fk := range o.FKs {
				if fk.RefTable == t.Name && sameCols(fk.RefColumns, cols) {
					return true
				}
			}
		}
		return false
	}
	for _, t := range p.Tables {
		var keep []Index
		for _, ix := range t.Indexes {
			names := make([]string, len(ix.Columns))
			plain := ix.Unique && ix.Where == ""
			for i, c := range ix.Columns {
				names[i] = c.Name
				plain = plain && !c.Desc
			}
			if plain && referenced(t, names) {
				t.Uniques = append(t.Uniques, Unique{Name: ix.Name, Columns: names})
				continue
			}
			keep = append(keep, ix)
		}
		t.Indexes = keep
	}
}

// filterCompositeUniques writes a unique key over several columns, some of which hold more than one NULL, as a unique
// function-based index: Oracle treats NULLs in a multi-column unique key as equal ((1, NULL) twice is a duplicate),
// SQLite never does. Every column of the index is CASE WHEN <no column is NULL> THEN column END, so a row with a NULL
// has an all-NULL key, which Oracle's unique index does not hold. A key over one column needs nothing: Oracle holds many
// NULLs there.
func (d oracleDialect) filterCompositeUniques(p *ScriptPlan) {
	used := map[string]bool{}
	for _, t := range p.Tables {
		for _, u := range t.Uniques {
			used[u.Name] = true
		}
		for _, ix := range t.Indexes {
			used[ix.Name] = true
		}
	}
	for _, t := range p.Tables {
		filtered := func(names []string) string {
			if len(names) < 2 {
				return ""
			}
			if !manyNulls(t, names) {
				return ""
			}
			var preds []string
			for _, n := range names {
				if !t.Col(n).NotNull {
					preds = append(preds, d.QuoteIdent(n)+" IS NOT NULL")
				}
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
			name := u.Name
			if name == "" {
				name = "UQ_" + t.Name + "_" + strings.Join(u.Columns, "_")
			}
			delete(used, u.Name)
			if d.StorableName(name) != "" || used[name] {
				name = safeName(d, name)
			}
			used[name] = true
			ix := Index{Name: name, Unique: true, Where: where}
			for _, n := range u.Columns {
				ix.Columns = append(ix.Columns, IndexColumn{Name: n})
			}
			t.Indexes = append(t.Indexes, ix)
			p.warn(WarnUniqueFiltered, t.Name+"."+ix.Name, "the unique key (%s) allows several NULLs in SQLite, and Oracle treats NULLs in a multi-column unique key as equal: it is written as a unique function-based index that holds no row with a NULL, which a foreign key cannot reference", strings.Join(u.Columns, ", "))
		}
		t.Uniques = keep
		for i := range t.Indexes {
			ix := &t.Indexes[i]
			if !ix.Unique || ix.Where != "" {
				continue
			}
			names := make([]string, len(ix.Columns))
			desc := false
			for j, c := range ix.Columns {
				names[j], desc = c.Name, desc || c.Desc
			}
			if desc && manyNulls(t, names) {
				// A descending key holds NULL as a value of its own (the descended NULL), so two NULLs collide whatever the
				// other columns hold: the key is carried ascending.
				for j := range ix.Columns {
					ix.Columns[j].Desc = false
				}
				p.warn(WarnIndexNotCarried, t.Name+"."+ix.Name, "the unique index (%s) is descending, and Oracle indexes a NULL of a descending key as a value that collides with another NULL, while SQLite allows many: it is carried ascending", strings.Join(names, ", "))
			}
			if ix.Where = filtered(names); ix.Where != "" {
				p.warn(WarnUniqueFiltered, t.Name+"."+ix.Name, "the unique index (%s) allows several NULLs in SQLite, and Oracle treats NULLs in a multi-column unique key as equal: it is written as a unique function-based index that holds no row with a NULL, which a foreign key cannot reference", strings.Join(names, ", "))
			}
		}
	}
}

// ---- names that share a namespace ----

// dedupeObjectNames drops the name of a unique constraint or foreign key that another constraint of the schema already
// has (one list for the schema), and the name of a unique constraint that an index has (the key's own index is named
// like the key).
func (d oracleDialect) dedupeObjectNames(p *ScriptPlan) {
	constraints := map[string]bool{}
	indexes := map[string]bool{}
	for _, t := range p.Tables {
		for _, ix := range t.Indexes {
			indexes[ix.Name] = true
		}
	}
	for _, t := range p.Tables {
		for i := range t.Uniques {
			u := &t.Uniques[i]
			if u.Name == "" {
				continue
			}
			if constraints[u.Name] || indexes[u.Name] {
				p.warn(WarnConstraintNameLost, t.Name+"."+u.Name, "unique constraint name is not carried (the name is taken by another constraint or index of the schema): Oracle names the key itself")
				u.Name = ""
				continue
			}
			constraints[u.Name], indexes[u.Name] = true, true
		}
	}
	for _, t := range p.Tables {
		for i := range t.FKs {
			fk := &t.FKs[i]
			if fk.Name == "" {
				continue
			}
			if constraints[fk.Name] {
				p.warn(WarnConstraintNameLost, t.Name+"."+fk.Name, "foreign key name is not carried (the name is taken by another constraint of the schema): Oracle names the key itself")
				fk.Name = ""
				continue
			}
			constraints[fk.Name] = true
		}
	}
}

// ---- foreign keys ----

// oracleParentKeys are the column lists a foreign key may reference: a primary key or a unique constraint (a unique index
// is never one, and neither is a unique function-based index).
func oracleParentKeys(t *ScriptTable) [][]string {
	keys := [][]string{}
	if len(t.PK) > 0 {
		keys = append(keys, t.PK)
	}
	for _, u := range t.Uniques {
		keys = append(keys, u.Columns)
	}
	return keys
}

// dropUnsupportedFKs removes the foreign keys Oracle cannot create and the actions it cannot carry: it has no ON UPDATE,
// no RESTRICT and no SET DEFAULT (ON DELETE CASCADE, ON DELETE SET NULL and the default, no action, are all it has).
func (d oracleDialect) dropUnsupportedFKs(p *ScriptPlan) {
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
			for _, k := range oracleParentKeys(parent) {
				if sameCols(k, fk.RefColumns) {
					ok = true
				}
			}
			if !ok {
				p.warn(WarnFKNotCarried, label, "the referenced columns (%s) of %s are no primary key or unique constraint, which Oracle requires", strings.Join(fk.RefColumns, ", "), parent.Name)
				continue
			}
			switch fk.OnUpdate {
			case "", "NO ACTION":
			default:
				p.warn(WarnFKActionNotCarried, label, "ON UPDATE %s does not exist in Oracle: the key is carried without it", fk.OnUpdate)
			}
			fk.OnUpdate = "NO ACTION"
			switch fk.OnDelete {
			case "CASCADE", "SET NULL":
			case "RESTRICT":
				p.warn(WarnFKActionNotCarried, label, "ON DELETE RESTRICT does not exist in Oracle: the key is carried with no action, which refuses the same change when the key is not deferred")
				fk.OnDelete = "NO ACTION"
			case "SET DEFAULT":
				p.warn(WarnFKActionNotCarried, label, "ON DELETE SET DEFAULT does not exist in Oracle: the key is carried with no action")
				fk.OnDelete = "NO ACTION"
			default:
				fk.OnDelete = "NO ACTION"
			}
			keep = append(keep, fk)
		}
		t.FKs = keep
	}
}

// ---- one type for the columns of a foreign key ----

// oracleClass is one equivalence class; before holds each member's type before the class was unified.
type oracleClass struct {
	members []*ScriptColumn
	before  []string
}

// unifyFKClasses gives every column of a foreign key equivalence class (the child columns and the parent columns they
// reference, transitively) one type: Oracle refuses a key between a number and a text (ORA-02267), and the script gives
// the whole class the widest of its types.
func (d oracleDialect) unifyFKClasses(p *ScriptPlan) []*oracleClass {
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
	byRoot := map[*ScriptColumn]*oracleClass{}
	var classes []*oracleClass
	for _, t := range p.Tables {
		for _, c := range t.Columns {
			if !c.Use.Keyed() {
				continue
			}
			r := find(c)
			if byRoot[r] == nil {
				byRoot[r] = &oracleClass{}
				classes = append(classes, byRoot[r])
			}
			byRoot[r].members = append(byRoot[r].members, c)
		}
	}
	for _, cl := range classes {
		if len(cl.members) > 1 {
			d.mergeClass(cl)
		}
	}
	return classes
}

func (d oracleDialect) mergeClass(cl *oracleClass) {
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
		need := oracleDefaultKeyLen
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
		oracleRender(&ch)
		m.Choice = ch
	}
}

// ---- key sizes ----

// oracleKeyBytes is the bytes a column adds to a key row: the longest value of a text or a binary column, as Oracle checks
// the key of a row (ORA-01450) and not the declared width, and the width of a number or a date. A CLOB, a BLOB and a text of
// more than 4,000 bytes cannot be indexed.
func oracleKeyBytes(c Choice, size int) int {
	const unindexable = 1 << 20
	switch c.Family {
	case "int", "bool", "decimal":
		return 22
	case "float":
		return 8
	case "date":
		return 7
	case "datetime":
		return 11
	case "text":
		if c.Len == 0 || size > oracleMaxVarchar {
			return unindexable
		}
		return max(size, 1)
	}
	if c.Len == 0 || c.Len > oracleMaxRaw {
		return unindexable
	}
	return max(size, 1)
}

func oracleKeyCost(cols []*ScriptColumn) int {
	n := 0
	for _, c := range cols {
		n += oracleKeyBytes(c.Choice, c.Choice.MaxSize) + 2
	}
	return n
}

// fitKeys makes every key fit the index limits of Oracle (about 6,398 bytes of key row, 32 columns). A text key column that
// declares no length gets VARCHAR2(255 CHAR), or its longest value when that is longer (Oracle cannot index a CLOB). Oracle
// checks the key of a row, not the declared width, so a key is measured by the longest values of its columns: one that
// does not fit is an error, and an index that does not fit is not created.
func (d oracleDialect) fitKeys(p *ScriptPlan, classes []*oracleClass) error {
	for _, cl := range classes {
		if c0 := cl.members[0].Choice; c0.Family == "text" && c0.Bounded && c0.Len < oracleDefaultKeyLen && c0.MaxSize <= oracleMaxVarchar {
			for _, m := range cl.members {
				m.Choice.Len = oracleDefaultKeyLen
				oracleRender(&m.Choice)
			}
		}
	}
	fits := func(cols []*ScriptColumn) bool {
		return oracleKeyCost(cols) <= oracleMaxKeyBytes && len(cols) <= oracleMaxKeyColumns
	}
	for _, t := range p.Tables {
		sets, kinds := t.keySets()
		for i := 0; i < len(sets); i++ {
			if kinds[i] == "foreign key" || fits(sets[i]) {
				continue // a foreign key needs no index in Oracle
			}
			if strings.HasPrefix(kinds[i], "index ") || strings.HasPrefix(kinds[i], "unique index ") {
				name := strings.TrimPrefix(strings.TrimPrefix(kinds[i], "unique "), "index ")
				p.warn(WarnIndexNotCarried, t.Name+"."+name, "the key row is longer than the %d bytes the script allows in an Oracle index (the longest values of its columns), or the key has a column that cannot be indexed", oracleMaxKeyBytes)
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
			return fmt.Errorf("%s of table %q is longer than Oracle allows (%d bytes, at most %d, at most %d columns) with the longest values of its columns, or holds a column that cannot be indexed; Oracle cannot carry it",
				kinds[i], t.Name, oracleKeyCost(sets[i]), oracleMaxKeyBytes, oracleMaxKeyColumns)
		}
	}
	// The unification warning names the final type, after the key budgets had their say.
	for _, cl := range classes {
		for i, m := range cl.members {
			if len(cl.before) > i && m.Choice.Type != cl.before[i] {
				p.warn(WarnTypeWidened, m.Object, "declared %q and carried as %s, but it joins the columns of its foreign keys, which Oracle requires to be of one type family: carried as %s", strings.TrimSpace(m.Src.SourceType), cl.before[i], m.Choice.Type)
			}
		}
	}
	for _, t := range p.Tables {
		for _, c := range t.Columns {
			if c.Use.Keyed() && c.Choice.Bounded && (c.Choice.Family == "text" || c.Choice.Family == "bytes") {
				p.warn(WarnKeyTypeBounded, c.Object, "a key column is carried as %s (declared %q): Oracle cannot index a CLOB or a BLOB; its longest value is %d", c.Choice.Type, strings.TrimSpace(c.Src.SourceType), longestValue(c))
			}
		}
	}
	return nil
}
