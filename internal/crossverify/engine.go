package crossverify

import (
	"context"
	"fmt"

	"github.com/datatug/datatug-cli/pkg/dbcopy/faithful"
)

// targetEngine is what the verifier needs to know about the engine that loaded the script: how it quotes a
// name, how it reports its catalog, how the values it returns are made canonical, which indexes it creates
// itself, how it counts rows and reads the completion marker, and what else it can be asked about a table.
type targetEngine struct {
	name  string
	quote func(string) string
	// read reads the catalog of a schema (the connection's current one when empty).
	read func(ctx context.Context, db Queryer, schema string) (*MySQLCatalog, error)
	// canon makes a value read through the engine's Go driver canonical.
	canon func(ch faithful.Choice, v any) (string, error)
	// implicitIndex is true for an index the engine creates for a constraint, so it is left out of the comparison.
	implicitIndex func(t *faithful.ScriptTable, ix MyIndex) bool
	count         func(table string) string
	markerSQL     string
	// extra checks what only this engine can be asked about a table (nil for none).
	extra func(ctx context.Context, in Input, t *faithful.ScriptTable, add func(obj, aspect, want, got string)) error
	// compareText is true when the verifier compares the text of defaults and CHECK constraints (MySQL's information
	// schema is read for it); false for an engine whose catalog it only counts them in.
	compareText bool
	// label is the engine's name in a diff; unstorable says why it cannot store a name ("" when it can), and the
	// twin functions say that a rename is justified by another table or column the engine cannot tell apart.
	label      string
	unstorable func(string) string
	tableTwin  func(p *faithful.ScriptPlan, t *faithful.ScriptTable) bool
	columnTwin func(t *faithful.ScriptTable, c *faithful.ScriptColumn) bool
	// normalizeSource is what the engine makes of the canonical form of a SQLite value (nil: nothing); the plan says the
	// same in its warnings.
	normalizeSource func(c *faithful.ScriptColumn, form string) string
	// lobLength, when set, adds the length of every BLOB column to the target query: a driver that reads an empty BLOB as
	// NULL is told otherwise by it.
	lobLength func(quoted string) string
}

var mysqlEngine = targetEngine{
	name:          "mysql",
	quote:         quoteMySQL,
	read:          ReadMySQL,
	canon:         canonTarget,
	implicitIndex: implicitFKIndex,
	count:         func(table string) string { return "SELECT COUNT(*) FROM " + quoteMySQL(table) },
	markerSQL:     "SELECT `source_sha256`, `tables_count`, `rows_count` FROM `" + faithful.MySQLMarkerTable + "`",
	compareText:   true,
	label:         "MySQL",
	unstorable:    unstorableName,
	tableTwin:     noTableTwin,
	columnTwin:    hasCaseTwin,
}

var mssqlEngine = targetEngine{
	name:          "mssql",
	quote:         quoteMSSQL,
	read:          ReadMSSQL,
	canon:         canonMSSQL,
	implicitIndex: func(*faithful.ScriptTable, MyIndex) bool { return false }, // the backing indexes of PK and UNIQUE are not indexes of the table in the reader
	count:         func(table string) string { return "SELECT COUNT(*) FROM " + quoteMSSQL(table) },
	markerSQL:     "SELECT [source_sha256], [tables_count], [rows_count] FROM [" + faithful.MySQLMarkerTable + "]",
	extra:         checkIdentity,
	label:         "SQL Server",
	unstorable:    unstorableMSSQL,
	tableTwin:     mssqlTableTwin,
	columnTwin:    mssqlColumnTwin,
}

var oracleEngine = targetEngine{
	name:            "oracle",
	quote:           quoteOracle,
	read:            ReadOracle,
	canon:           canonOracle,
	implicitIndex:   func(*faithful.ScriptTable, MyIndex) bool { return false }, // the backing indexes of PK and UNIQUE are read as the keys
	count:           func(table string) string { return "SELECT COUNT(*) FROM " + quoteOracle(table) },
	markerSQL:       `SELECT "source_sha256", "tables_count", "rows_count" FROM "` + faithful.MySQLMarkerTable + `"`,
	extra:           checkIdentityOracle,
	label:           "Oracle",
	unstorable:      unstorableOracle,
	tableTwin:       noTableTwin,
	columnTwin:      noColumnTwin,
	normalizeSource: normalizeSourceOracle,
	lobLength:       lobLengthOracle,
}

// Engine names the engines the verifier knows.
const (
	EngineMySQL  = "mysql"
	EngineMSSQL  = "mssql"
	EngineOracle = "oracle"
)

func engineByName(name string) (targetEngine, error) {
	switch name {
	case "", EngineMySQL:
		return mysqlEngine, nil
	case EngineMSSQL:
		return mssqlEngine, nil
	case EngineOracle:
		return oracleEngine, nil
	}
	return targetEngine{}, fmt.Errorf("unknown engine %q (known: %s, %s, %s)", name, EngineMySQL, EngineMSSQL, EngineOracle)
}
