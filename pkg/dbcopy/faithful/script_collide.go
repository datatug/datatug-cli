package faithful

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
)

// WarnKeyValuesCollide: key values that SQLite keeps apart are equal to the target's collation, so a unique key
// refuses the second one and stops the load.
const WarnKeyValuesCollide = "key-values-collide"

// keyFolder is implemented by a dialect whose default text comparison is looser than SQLite's exact one: KeyFold
// is a text as that comparison sees it.
type keyFolder interface {
	KeyFold(text string) string
}

// keySet is one unique key (primary, unique or unique index) of a table, as positions in the table's columns.
type keySet struct {
	label string
	cols  []int
}

// warnKeyCollisions finds, for every unique key that holds a text column, the rows whose key equals another
// row's after the dialect's folding, and says how many with the first pair. The load of such a script stops at
// the second row with "Duplicate entry". It reads each table once, and keeps one 16-byte digest per row of a
// key, not the key. Only text is folded: a decimal text spelled two ways or an INTEGER and its text in one
// column are not looked for.
func warnKeyCollisions(ctx context.Context, d Dialect, db Queryer, p *ScriptPlan) error {
	kf, ok := d.(keyFolder)
	if !ok {
		return nil
	}
	for _, t := range p.Tables {
		sets := uniqueKeySets(t)
		if len(sets) == 0 || t.Rows == 0 {
			continue
		}
		if err := scanKeyCollisions(ctx, kf, db, p, t, sets); err != nil {
			return fmt.Errorf("table %q: %w", t.Name, err)
		}
	}
	return nil
}

func uniqueKeySets(t *ScriptTable) []keySet {
	pos := map[string]int{}
	for i, c := range t.Columns {
		pos[c.Name] = i
	}
	var sets []keySet
	add := func(names []string) {
		ks := keySet{label: t.Name + "(" + strings.Join(names, ",") + ")"}
		text := false
		for _, n := range names {
			ks.cols = append(ks.cols, pos[n])
			text = text || t.Columns[pos[n]].Choice.Family == "text"
		}
		if text {
			sets = append(sets, ks)
		}
	}
	if len(t.PK) > 0 {
		add(t.PK)
	}
	for _, u := range t.Uniques {
		add(u.Columns)
	}
	for _, ix := range t.Indexes {
		if ix.Unique {
			names := make([]string, len(ix.Columns))
			for i, c := range ix.Columns {
				names[i] = c.Name
			}
			add(names)
		}
	}
	return sets
}

type collision struct {
	seen        map[[16]byte]string
	rows        int64
	first, also string
}

func scanKeyCollisions(ctx context.Context, kf keyFolder, db Queryer, p *ScriptPlan, t *ScriptTable, sets []keySet) error {
	rows, err := db.QueryContext(ctx, "SELECT "+scriptSelectList(t)+" FROM "+quoteIdent(t.Src.Name))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	vals, ptrs := sqliteRowBuffer(len(t.Columns))
	states := make([]*collision, len(sets))
	for i := range states {
		states[i] = &collision{seen: map[[16]byte]string{}}
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		fixEmptyBlobs(vals)
		for i, ks := range sets {
			folded, shown, ok := foldKey(kf, t, ks, vals)
			if !ok {
				continue // a NULL in a unique key never collides
			}
			sum := sha256.Sum256([]byte(folded))
			var k [16]byte
			copy(k[:], sum[:16])
			st := states[i]
			if prev, dup := st.seen[k]; dup {
				st.rows++
				if st.first == "" {
					st.first, st.also = prev, shown
				}
				continue
			}
			st.seen[k] = shown
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i, ks := range sets {
		if st := states[i]; st.rows > 0 {
			p.warn(WarnKeyValuesCollide, ks.label, "%d row(s) have a key equal to another row's under MySQL's default text collation (case- and accent-insensitive), for example %s and %s: MySQL refuses the second with Duplicate entry and the load stops; make the keys differ or load into a column with a binary collation", st.rows, st.first, st.also)
		}
	}
	return nil
}

// foldKey is the folded form of a key, the form to show, and false when a column of the key is NULL.
func foldKey(kf keyFolder, t *ScriptTable, ks keySet, vals []any) (folded, shown string, ok bool) {
	var f, s []string
	for _, ci := range ks.cols {
		v := vals[ci]
		if v == nil {
			return "", "", false
		}
		var text string
		switch x := v.(type) {
		case string:
			text = x
		case []byte:
			text = string(x)
		default:
			text = fmt.Sprint(x)
		}
		shownText := text
		if r := []rune(shownText); len(r) > 30 {
			shownText = string(r[:30]) + "..."
		}
		s = append(s, strconv.Quote(shownText))
		if t.Columns[ci].Choice.Family == "text" {
			text = kf.KeyFold(text)
		}
		f = append(f, strconv.Itoa(len(text))+":"+text)
	}
	return strings.Join(f, "|"), strings.Join(s, ","), true
}
