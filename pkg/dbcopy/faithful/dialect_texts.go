package faithful

// dialectTexts is what a dialect may say about itself where the shared planner words a warning or compares
// names. A dialect that does not implement it is MySQL, whose wording and rules the planner started with.
type dialectTexts interface {
	// Label is the engine's name in a message ("SQL Server").
	Label() string
	// FoldName is a name as the engine compares object names.
	FoldName(name string) string
	// FoldDescription says in what two names are the same for the engine ("case").
	FoldDescription() string
	// PartialIndexNote says why an index with a WHERE clause is not carried.
	PartialIndexNote() string
	// FoldsTableNames is true when the engine cannot hold two tables whose names differ only by case.
	FoldsTableNames() bool
}

func dialectLabel(d Dialect) string {
	if t, ok := d.(dialectTexts); ok {
		return t.Label()
	}
	return "MySQL"
}

func foldFor(d Dialect, name string) string {
	if t, ok := d.(dialectTexts); ok {
		return t.FoldName(name)
	}
	return foldName(name)
}

func foldDescription(d Dialect) string {
	if t, ok := d.(dialectTexts); ok {
		return t.FoldDescription()
	}
	return "case" // MySQL compares the names of columns, indexes and constraints without case, never without accents
}

// fkFoldFor is a foreign key name as the engine compares it: MySQL compares them more loosely than other names.
func fkFoldFor(d Dialect, name string) string {
	if t, ok := d.(dialectTexts); ok {
		return t.FoldName(name)
	}
	return fkFoldName(name)
}

func partialIndexNote(d Dialect) string {
	if t, ok := d.(dialectTexts); ok {
		return t.PartialIndexNote()
	}
	return "MySQL has no partial indexes"
}

func foldsTableNames(d Dialect) bool {
	t, ok := d.(dialectTexts)
	return ok && t.FoldsTableNames()
}

// objectNamer is implemented by a dialect whose constraint names share a namespace with other objects.
type objectNamer interface {
	// ObjectNames are the names (tables, keys, foreign keys) a CHECK constraint name must not repeat.
	ObjectNames(p *ScriptPlan) []string
}

func extraObjectNames(d Dialect, p *ScriptPlan) []string {
	if o, ok := d.(objectNamer); ok {
		return o.ObjectNames(p)
	}
	return nil
}
