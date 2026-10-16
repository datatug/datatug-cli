package faithful

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
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
// the second row with "Duplicate entry". It reads each table once and keeps 16 bytes per row of a key (a digest
// of the folded key, in one slice that is sorted at the end, so memory is 16 bytes a row and nothing else); only
// when a key has collisions does it read the table a second time, to show the first pair. Only text is folded: a
// decimal text spelled two ways or an INTEGER and its text in one column are not looked for.
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
		if err := checkKeyCollisions(ctx, kf, db, p, t, sets); err != nil {
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

// keyDigest is the digest of a folded key: 16 bytes.
func keyDigest(folded string) [16]byte {
	sum := sha256.Sum256([]byte(folded))
	var k [16]byte
	copy(k[:], sum[:16])
	return k
}

// eachKey reads the table and hands every row's keys to visit: for each key set the digest of the folded key and
// the key as it is shown in a warning, skipping a key with a NULL part (a NULL in a unique key never collides).
func eachKey(ctx context.Context, kf keyFolder, db Queryer, t *ScriptTable, sets []keySet, visit func(set int, digest [16]byte, shown string)) error {
	rows, err := db.QueryContext(ctx, "SELECT "+scriptSelectList(t)+" FROM "+quoteIdent(t.Src.Name))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	vals, ptrs := sqliteRowBuffer(len(t.Columns))
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		fixEmptyBlobs(vals)
		for i, ks := range sets {
			if folded, shown, ok := foldKey(kf, t, ks, vals); ok {
				visit(i, keyDigest(folded), shown)
			}
		}
	}
	return rows.Err()
}

// scanKeyDigests is the first pass: the digests of every key of every set, in row order.
func scanKeyDigests(ctx context.Context, kf keyFolder, db Queryer, t *ScriptTable, sets []keySet) ([][][16]byte, error) {
	digests := make([][][16]byte, len(sets))
	err := eachKey(ctx, kf, db, t, sets, func(i int, d [16]byte, _ string) { digests[i] = append(digests[i], d) })
	return digests, err
}

func checkKeyCollisions(ctx context.Context, kf keyFolder, db Queryer, p *ScriptPlan, t *ScriptTable, sets []keySet) error {
	digests, err := scanKeyDigests(ctx, kf, db, t, sets)
	if err != nil {
		return err
	}
	// Per set: how many rows repeat an earlier digest, and the smallest digest that is repeated.
	rows := make([]int64, len(sets))
	repeated := make([]*[16]byte, len(sets))
	for i := range digests {
		ds := digests[i]
		sort.Slice(ds, func(a, b int) bool { return bytes.Compare(ds[a][:], ds[b][:]) < 0 })
		for n := 1; n < len(ds); n++ {
			if ds[n] == ds[n-1] {
				rows[i]++
				if repeated[i] == nil {
					repeated[i] = &ds[n]
				}
			}
		}
	}
	// The second pass is for the sets that collide, and keeps the first two rows of the one digest.
	var want [][16]byte
	for i := range sets {
		if repeated[i] != nil {
			want = append(want, *repeated[i])
		}
	}
	if len(want) == 0 {
		return nil
	}
	first := make([][]string, len(sets))
	err = eachKey(ctx, kf, db, t, sets, func(i int, d [16]byte, shown string) {
		if repeated[i] != nil && d == *repeated[i] && len(first[i]) < 2 {
			first[i] = append(first[i], shown)
		}
	})
	if err != nil {
		return err
	}
	for i, ks := range sets {
		if rows[i] > 0 {
			if len(first[i]) < 2 {
				return fmt.Errorf("key %s changed while it was read", ks.label)
			}
			p.warn(WarnKeyValuesCollide, ks.label, "%d row(s) have a key equal to another row's under MySQL's default text collation (case- and accent-insensitive), for example %s and %s: MySQL refuses the second with Duplicate entry and the load stops; make the keys differ or load into a column with a binary collation", rows[i], first[i][0], first[i][1])
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
