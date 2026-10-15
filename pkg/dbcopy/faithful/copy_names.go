package faithful

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"
)

// checkCompiles tells whether SQLite accepts a CHECK / WHERE expression over the columns.
func checkCompiles(cols []string, expr string) error {
	db, _ := sql.Open("sqlite", ":memory:") // the driver is registered: Open cannot fail
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	_, err := db.Exec("CREATE TABLE t (" + quoteList(cols) + ", CHECK (" + expr + "))")
	return err
}

func validatePGNames(p *Plan) error {
	chk := func(object, name string) error {
		if len(name) > 63 {
			return &NameError{Object: object, Reason: fmt.Sprintf("its name is %d bytes and PostgreSQL takes at most 63", len(name))}
		}
		if strings.ContainsRune(name, 0) {
			return &NameError{Object: object, Reason: "its name holds a NUL byte"}
		}
		return nil
	}
	for _, t := range p.Tables {
		if err := chk(fmt.Sprintf("table %q", t.Name), t.Name); err != nil {
			return err
		}
		for _, c := range t.Columns {
			if err := chk(fmt.Sprintf("column %q of table %q", c.Name, t.Name), c.Name); err != nil {
				return err
			}
		}
	}
	return nil
}

// shortenPGNames fits the names that PostgreSQL does not need to be exact into 63 bytes: an index
// gets its name cut and ends in a hash of the whole name (index-renamed), and a constraint that
// has a longer name is created with the name PostgreSQL gives it (constraint-name-not-carried).
// A table or column name that is too long cannot be changed and is refused by validatePGNames.
func shortenPGNames(p *Plan) {
	for _, t := range p.Tables {
		for i := range t.Indexes {
			if long := t.Indexes[i].Name; len(long) > 63 {
				sum := sha256.Sum256([]byte(long))
				short := truncateUTF8(long, 54) + "_" + hex.EncodeToString(sum[:4])
				p.warn(WarnIndexRenamed, t.Name+"."+long, "index name is %d bytes and PostgreSQL takes at most 63: created as %q", len(long), short)
				t.Indexes[i].Name = short
			}
		}
		drop := func(kind, name string) string {
			if len(name) > 63 {
				p.warn(WarnConstraintNameLost, t.Name+"."+name, "%s name is %d bytes and PostgreSQL takes at most 63: created with PostgreSQL's own name", kind, len(name))
				return ""
			}
			return name
		}
		t.PKName = drop("primary key", t.PKName)
		for i := range t.Uniques {
			t.Uniques[i].Name = drop("unique constraint", t.Uniques[i].Name)
		}
		for i := range t.FKs {
			t.FKs[i].Name = drop("foreign key", t.FKs[i].Name)
		}
		for i := range t.Checks {
			t.Checks[i].Name = drop("check constraint", t.Checks[i].Name)
		}
	}
}

func truncateUTF8(s string, n int) string {
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
