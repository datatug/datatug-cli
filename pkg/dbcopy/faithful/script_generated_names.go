package faithful

import "strings"

// MySQL names a CHECK that has no name <table>_chk_<n> and a foreign key that has none <table>_ibfk_<n>, in the same
// list of names as the names a user gave (check names are unique in the schema, so are the names of foreign keys): a
// name the script gives to another constraint that equals one of them makes the load fail (error 3822 for a check,
// 1826 for a foreign key), whichever of the two is created second. The names a table generates are only known when the
// load runs, so the script does not write a name of that shape for a constraint of another table (or of this one)
// while the table has a constraint without a name: the name is not carried, with a warning, and MySQL names it itself.

// generatedNameKinds are the two kinds of constraint MySQL names itself.
var generatedNameKinds = []struct {
	suffix string // after the table name
}{{"_chk_"}, {"_ibfk_"}}

// reserveGeneratedNames drops the names (with a counted warning) that equal a name MySQL generates for an unnamed
// constraint. A name that is dropped leaves its constraint unnamed, which makes MySQL generate one more name, so the
// rule runs until nothing changes.
func reserveGeneratedNames(d Dialect, p *ScriptPlan) {
	for changed := true; changed; {
		changed = false
		for k, kind := range generatedNameKinds {
			fold := foldFor // check names are compared by the fold of check names, the names of foreign keys by their own
			if k == 1 {
				fold = fkFoldFor
			}
			var generating []string // the folded prefixes of the tables that have a constraint of the kind without a name
			for _, t := range p.Tables {
				if hasUnnamed(t, k) {
					generating = append(generating, fold(d, t.Name+kind.suffix))
				}
			}
			if len(generating) == 0 {
				continue
			}
			collides := func(name string) bool {
				f := fold(d, name)
				for _, g := range generating {
					if rest, ok := strings.CutPrefix(f, g); ok && isCanonicalNumber(rest) {
						return true
					}
				}
				return false
			}
			for _, t := range p.Tables {
				if k == 0 {
					for i := range t.Checks {
						if name := t.Checks[i].Name; name != "" {
							if collides(name) {
								p.warn(WarnConstraintNameLost, t.Name+"."+name, "check constraint name is not carried (it has the form MySQL gives to the name of a check without one, <table>_chk_<n>, and a table of the script has one: the load would fail with error 3822): MySQL names the check itself")
								t.Checks[i].Name = ""
								changed = true
							}
						}
					}
					continue
				}
				for i := range t.FKs {
					if name := t.FKs[i].Name; name != "" {
						if collides(name) {
							p.warn(WarnConstraintNameLost, t.Name+"."+name, "foreign key name is not carried (it has the form MySQL gives to the name of a key without one, <table>_ibfk_<n>, and a table of the script has one: the load would fail with error 1826): MySQL names the key itself")
							t.FKs[i].Name = ""
							changed = true
						}
					}
				}
			}
		}
	}
}

// hasUnnamed reports whether the table has a check (kind 0) or a foreign key (kind 1) without a name.
func hasUnnamed(t *ScriptTable, kind int) bool {
	if kind == 0 {
		for _, c := range t.Checks {
			if c.Name == "" {
				return true
			}
		}
		return false
	}
	for _, fk := range t.FKs {
		if fk.Name == "" {
			return true
		}
	}
	return false
}

// isCanonicalNumber is true for the digits of a number as MySQL writes one: no sign, no leading zero.
func isCanonicalNumber(s string) bool {
	if s == "" || s[0] == '0' {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
