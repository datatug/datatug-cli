package api

const (
	// DriverMySQL is the driver name a MySQL server and catalog carry in a project.
	DriverMySQL = "mysql"

	// DriverOracle is the driver name an Oracle server and catalog carry in a project.
	DriverOracle = "oracle"

	// DriverSQLServer is the driver name a SQL Server server and catalog carry in a project.
	DriverSQLServer = "sqlserver"

	// DriverCouchDB is the driver name a CouchDB catalog carries, and the --driver
	// value `datatug scan` reads it with. One CouchDB database is one logical
	// collection.
	DriverCouchDB = "couchdb"

	// DriverPostgREST is the driver name a PostgREST catalog carries, and the
	// --driver value `datatug scan` reads it with. One PostgREST API root exposes
	// native PostgreSQL relations as collections.
	DriverPostgREST = "postgrest"

	// DriverLibSQL is the driver name a libSQL catalog carries, and the --driver
	// value `datatug scan` reads it with. One libSQL database exposes SQLite
	// tables and views as collections; the token comes from the environment.
	DriverLibSQL = "libsql"

	// DriverClickHouse names the read-only anonymous-recordset adapter.
	DriverClickHouse = "clickhouse"

	// DriverTypesense is the driver name a Typesense catalog carries, and the
	// --driver value `datatug scan` reads it with. One Typesense server exposes
	// one configured collection of native documents; the scoped API key comes
	// from the environment.
	DriverTypesense = "typesense"

	// ServerDefaultSchema is the schema a MySQL, Oracle or SQL Server catalog records its tables
	// under: the schema the connection reads (a MySQL connection's database, an Oracle session's
	// current schema, a SQL Server login's default schema), by a fixed name and not its own. The real name is the database of the URL or, for
	// Oracle, most often the user, both parts of the connection that stay in the environment variable
	// and out of the project, like every part of a PostgreSQL URL.
	ServerDefaultSchema = "default"
)

// IsServerDriver reports whether driver is that of a database server whose connection URL a project
// keeps in an environment variable, named by a connection descriptor: postgres, mysql, oracle or
// sqlserver.
func IsServerDriver(driver string) bool {
	return driver == DriverPostgres || driver == DriverMySQL || driver == DriverOracle || driver == DriverSQLServer
}
