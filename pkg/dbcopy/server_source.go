package dbcopy

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2mssql"
	"github.com/dal-go/dalgo2mysql"
	"github.com/dal-go/dalgo2oracle"
	"github.com/dal-go/dalgo2sql"
	dalrecord "github.com/dal-go/record"
	"github.com/go-sql-driver/mysql"
	goora "github.com/sijms/go-ora/v3"
)

// A MySQL, Oracle or SQL Server source is a database server reached by a connection URL, like a
// PostgreSQL one: mysql://user:password@host:port/database, oracle://user:password@host:port/service
// and sqlserver://user:password@host:port/database. The URL is read from an environment variable
// ("env:NAME"), and a project names the variable and never the URL (see PostgresDescriptor, which they
// share). DataTug only reads one: a MySQL session is opened with transaction_read_only on, a records read
// of an Oracle source runs in a READ ONLY transaction, and one of a SQL Server source, which has no
// read-only session, in a transaction that is always rolled back (see transactionReads).
// A schema scan opens one the same way.

// serverEngine is what differs between the database servers a source URL of this file names.
type serverEngine struct {
	scheme      string
	title       string
	defaultPort int
	// pathNames is what the one segment of the path of the URL names, for the sentence that
	// refuses a URL without it.
	pathNames string
	// queryKeys are the keys of the query of the URL that are passed on to the driver, in lower
	// case. Any other key is refused: a key the driver reads can turn the read-only session off, or
	// send the connection somewhere the rest of the URL does not name.
	queryKeys map[string]bool
}

var (
	mysqlEngine = serverEngine{
		scheme: "mysql", title: "MySQL", defaultPort: 3306, pathNames: "database",
		queryKeys: map[string]bool{"tls": true, "timeout": true},
	}
	oracleEngine = serverEngine{
		scheme: "oracle", title: "Oracle", defaultPort: 1521, pathNames: "service name",
		queryKeys: map[string]bool{"ssl": true, "ssl verify": true, "timeout": true, "connection timeout": true, "wallet": true},
	}
	sqlServerEngine = serverEngine{
		scheme: "sqlserver", title: "SQL Server", defaultPort: 1433, pathNames: "database",
		queryKeys: map[string]bool{"encrypt": true, "trustservercertificate": true, "connection timeout": true},
	}
)

// serverEngineOf is the engine of a source scheme, and false for a scheme that is not a MySQL or an
// Oracle one.
func serverEngineOf(scheme string) (serverEngine, bool) {
	switch scheme {
	case "mysql":
		return mysqlEngine, true
	case "oracle":
		return oracleEngine, true
	case "sqlserver":
		return sqlServerEngine, true
	}
	return serverEngine{}, false
}

// IsServerScheme reports whether scheme is that of a database server whose connection URL is kept in
// an environment variable: postgres, mysql, oracle or sqlserver.
func IsServerScheme(scheme string) bool {
	_, ok := serverEngineOf(scheme)
	return ok || scheme == "postgres"
}

// ServerTitle is how a message names the engine of a server scheme: "PostgreSQL", "MySQL" or
// "Oracle", and the scheme itself for any other.
func ServerTitle(scheme string) string {
	if scheme == "postgres" {
		return "PostgreSQL"
	}
	if engine, ok := serverEngineOf(scheme); ok {
		return engine.title
	}
	return scheme
}

// CheckServerRead says why a read of ref through policies access policies cannot run, or nil when it can.
// Reads through access policies of a PostgreSQL, MySQL, Oracle or SQL Server source are not served yet: their
// row and column rules have been reviewed for SQLite and inGitDB sources only. A read with no policy, and a
// source that is not a server's, are not its business. It is called before the source is opened, by every path
// that reads through policies.
func CheckServerRead(ref BackendRef, policies int) error {
	if policies > 0 && IsServerScheme(ref.Scheme) {
		return &refusedError{"policy-enforced reads on " + ServerTitle(ref.Scheme) + " sources are not available yet: run with --no-policies"}
	}
	return nil
}

// ServerTarget is the part of a MySQL, Oracle or SQL Server connection URL that says where it points. It never
// holds the password.
type ServerTarget struct {
	Host string
	// Port is the port of the URL, or the engine's default port when it names none.
	Port int
	User string
	// Database is the one segment of the path: the database of a MySQL URL, the service name of an
	// Oracle one.
	Database string
}

// serverURL is a parsed MySQL, Oracle or SQL Server URL: the target, the password and the query that the
// driver is given.
type serverURL struct {
	ServerTarget
	password string
	query    url.Values
}

// serverURLRefusal is the refusal of a MySQL, Oracle or SQL Server URL: a fixed sentence that says what is wrong
// and quotes none of the URL, which holds the password.
type serverURLRefusal struct{ message string }

func (e *serverURLRefusal) Error() string { return e.message }

func refuseServerURL(engine serverEngine, format string, args ...any) error {
	return &serverURLRefusal{"the " + engine.title + " URL " + fmt.Sprintf(format, args...)}
}

// serverHost is a host name or an address: what a server URL may name as its host. A host list, or
// the path of a socket, is refused.
var serverHost = regexp.MustCompile(`^[A-Za-z0-9:][A-Za-z0-9._:-]*$`)

// parseServerURL reads rawURL, a URL of engine's scheme. Every error is a serverURLRefusal.
func parseServerURL(engine serverEngine, rawURL string) (serverURL, error) {
	if atSignAfterAuthority(rawURL) {
		return serverURL{}, refuseServerURL(engine, "holds a literal '@' after its host, so it is read differently from how it was written (a password that holds '/', '?' or '#' ends the host early): percent-encode '@', '/', '?' and '#' in the user name and the password, for example '/' as %%2F")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Opaque != "" || parsed.Host == "" {
		return serverURL{}, refuseServerURL(engine, "cannot be read as user, password, host, port and %s", engine.pathNames)
	}
	if parsed.Fragment != "" {
		return serverURL{}, refuseServerURL(engine, "has a fragment ('#'): percent-encode '#' in the user name and the password")
	}
	host := parsed.Hostname()
	if !serverHost.MatchString(host) {
		return serverURL{}, refuseServerURL(engine, "names no host DataTug can connect to: use a host name or an address")
	}
	port := engine.defaultPort
	if text := parsed.Port(); text != "" {
		if port, err = strconv.Atoi(text); err != nil || port < 1 || port > 65535 {
			return serverURL{}, refuseServerURL(engine, "has a port that is not a number from 1 to 65535")
		}
	}
	user := parsed.User.Username()
	if user == "" {
		return serverURL{}, refuseServerURL(engine, "names no user: write user:password@ in front of the host")
	}
	if strings.ContainsAny(user, ":%") {
		return serverURL{}, refuseServerURL(engine, "has a user name that holds a colon or a percent sign once it is decoded (the user and the password joined by an encoded ':', or a name encoded twice): write user:password with a literal colon and percent-encode each once")
	}
	password, _ := parsed.User.Password()
	database := strings.TrimPrefix(parsed.Path, "/")
	if database == "" || strings.Contains(database, "/") {
		return serverURL{}, refuseServerURL(engine, "must name one %s as its path (/%s)", engine.pathNames, strings.ReplaceAll(engine.pathNames, " ", "_"))
	}
	query := parsed.Query()
	for key := range query {
		if !engine.queryKeys[strings.ToLower(key)] {
			return serverURL{}, refuseServerURL(engine, "has a query parameter DataTug does not pass on (it accepts only %s)", strings.Join(slices.Sorted(maps.Keys(engine.queryKeys)), ", "))
		}
	}
	return serverURL{
		ServerTarget: ServerTarget{Host: host, Port: port, User: user, Database: database},
		password:     password,
		query:        query,
	}, nil
}

// ParseServerTarget reads the destination a mysql://, oracle:// or sqlserver:// URL states. No error
// quotes the URL.
func ParseServerTarget(scheme, rawURL string) (ServerTarget, error) {
	engine, ok := serverEngineOf(scheme)
	if !ok {
		return ServerTarget{}, fmt.Errorf("%s is not a MySQL, Oracle or SQL Server scheme", scheme)
	}
	parsed, err := parseServerURL(engine, rawURL)
	return parsed.ServerTarget, err
}

// parseServerSource is parseURL's case for a mysql:// or an oracle:// source.
func parseServerSource(engine serverEngine, rest, display string) (BackendRef, error) {
	path := engine.scheme + "://" + rest
	if _, err := parseServerURL(engine, path); err != nil {
		return BackendRef{}, fmt.Errorf("invalid %s URL: %w", engine.scheme, err)
	}
	return BackendRef{Scheme: engine.scheme, Path: path, Raw: display}, nil
}

// serverConnectTimeout bounds a connection attempt whose URL sets no timeout of its own.
const serverConnectTimeout = 10 * time.Second

// mysqlDSN is the go-sql-driver/mysql data source name of a parsed MySQL URL. Every session it opens
// is read only: transaction_read_only is set on each connection, and the URL cannot turn it off (its
// query may set only tls and timeout).
func mysqlDSN(parsed serverURL) (string, error) {
	cfg := mysql.NewConfig()
	cfg.User = parsed.User
	cfg.Passwd = parsed.password
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(parsed.Host, strconv.Itoa(parsed.Port))
	cfg.DBName = parsed.Database
	cfg.ParseTime = true
	cfg.Timeout = serverConnectTimeout
	cfg.Params = map[string]string{"transaction_read_only": "1"}
	for key, values := range parsed.query {
		value := values[len(values)-1]
		switch strings.ToLower(key) {
		case "tls":
			cfg.TLSConfig = value
		case "timeout":
			timeout, err := time.ParseDuration(value)
			if err != nil || timeout <= 0 {
				return "", refuseServerURL(mysqlEngine, "has a timeout that is not a duration such as 5s")
			}
			cfg.Timeout = timeout
		}
	}
	return cfg.FormatDSN(), nil
}

// oracleURL is the go-ora connection URL of a parsed Oracle URL, built again from its parts so the
// driver reads what parseServerURL read.
func oracleURL(parsed serverURL) string {
	options := map[string]string{}
	for key, values := range parsed.query {
		options[strings.ToUpper(key)] = values[len(values)-1]
	}
	if _, set := options["TIMEOUT"]; !set {
		options["TIMEOUT"] = strconv.Itoa(int(serverConnectTimeout / time.Second))
	}
	return goora.BuildUrl(parsed.Host, parsed.Port, parsed.Database, parsed.User, parsed.password, options)
}

// openServerSchemaDB opens a MySQL or an Oracle database as its adapter does: the DALgo database, its
// schema reader and its Close. They are seams so tests reach the open without a server; the test
// binary of this package replaces them with ones that stop the run.
var (
	newMySQLDatabase = func(dsn string) (SchemaScanDB, error) {
		return dalgo2mysql.NewDatabaseWithOptions(dsn, dal.NewSchema(nil, nil), dalgo2sql.DbOptions{StructuredQueryDialect: "mysql"})
	}
	newOracleDatabase = func(ctx context.Context, dsn string) (SchemaScanDB, error) {
		return asSchemaScanDB(dalgo2oracle.NewDatabase(ctx, dsn))
	}
	newSQLServerDatabase = func(ctx context.Context, dsn string) (SchemaScanDB, error) {
		return asSchemaScanDB(dalgo2mssql.NewDatabase(ctx, dsn))
	}
)

// asSchemaScanDB is what an adapter's constructor returned, as a SchemaScanDB: an error stays an
// error with no database, so a nil pointer of the adapter's type never becomes a non-nil interface.
func asSchemaScanDB[T SchemaScanDB](db T, err error) (SchemaScanDB, error) {
	if err != nil {
		return nil, err
	}
	return db, nil
}

// transactionReads is a database whose records reads each run in a transaction that cannot keep a
// write a query's text might hold. For Oracle it is a READ ONLY transaction (dalgo2oracle turns
// RunReadonlyTransaction into SET TRANSACTION READ ONLY), which the server refuses to write in. SQL
// Server has none, so for it (rollBack) the read runs in a transaction that is always rolled back:
// a write, and DDL, which SQL Server runs inside a transaction, is undone. The rows are read in full
// inside the transaction and handed over from memory, because the transaction ends when the read returns.
type transactionReads struct {
	SchemaScanDB
	rollBack bool
}

// errRolledBack is what the worker of a rolled-back read returns to end its transaction with a rollback.
var errRolledBack = errors.New("the read is rolled back")

// ExecuteQueryToRecordsReader runs query in a transaction that keeps no write (see transactionReads).
func (r transactionReads) ExecuteQueryToRecordsReader(ctx context.Context, query dal.Query) (dal.RecordsReader, error) {
	var records []dalrecord.Record
	read := func(ctx context.Context, tx dal.QueryExecutor) error {
		reader, err := tx.ExecuteQueryToRecordsReader(ctx, query)
		if err != nil {
			return err
		}
		records, err = dal.ReadAllToRecords(ctx, reader)
		return err
	}
	var err error
	if r.rollBack {
		err = r.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
			if err := read(ctx, tx); err != nil {
				return err
			}
			return errRolledBack
		})
		if errors.Is(err, errRolledBack) {
			err = nil
		}
	} else {
		err = r.RunReadonlyTransaction(ctx, func(ctx context.Context, tx dal.ReadTransaction) error { return read(ctx, tx) })
	}
	if err != nil {
		return nil, err
	}
	return dal.NewRecordsReader(records), nil
}

// errServerReadOnly refuses to open a MySQL, Oracle or SQL Server source for writing.
func errServerReadOnly(engine serverEngine) error {
	return &refusedError{engine.title + " sources are read only in DataTug: only a read opens one"}
}

// sqlServerURL is the go-mssqldb connection URL of a parsed SQL Server URL: the database moves from
// the path, which go-mssqldb reads as an instance name, to the database parameter.
func sqlServerURL(parsed serverURL) string {
	query := url.Values{}
	for key, values := range parsed.query {
		query.Set(key, values[len(values)-1])
	}
	query.Set("database", parsed.Database)
	if !query.Has("connection timeout") {
		query.Set("connection timeout", strconv.Itoa(int(serverConnectTimeout/time.Second)))
	}
	return (&url.URL{
		Scheme:   "sqlserver",
		User:     url.UserPassword(parsed.User, parsed.password),
		Host:     net.JoinHostPort(parsed.Host, strconv.Itoa(parsed.Port)),
		RawQuery: query.Encode(),
	}).String()
}

// openServerDatabase opens this MySQL, Oracle or SQL Server source through its adapter, for reading.
func (r BackendRef) openServerDatabase(ctx context.Context, engine serverEngine) (SchemaScanDB, error) {
	parsed, err := parseServerURL(engine, r.Path)
	if err != nil {
		return nil, err
	}
	switch engine.scheme {
	case "mysql":
		dsn, err := mysqlDSN(parsed)
		if err != nil {
			return nil, err
		}
		return newMySQLDatabase(dsn)
	case "oracle":
		db, err := newOracleDatabase(ctx, oracleURL(parsed))
		if err != nil {
			return nil, err
		}
		return transactionReads{SchemaScanDB: db}, nil
	}
	db, err := newSQLServerDatabase(ctx, sqlServerURL(parsed))
	if err != nil {
		return nil, err
	}
	return transactionReads{SchemaScanDB: db, rollBack: true}, nil
}

// openServer is openSource's case for a MySQL, Oracle or SQL Server source: a refusal of a write, then the
// open.
func (r BackendRef) openServer(ctx context.Context, engine serverEngine, mode openMode) (dal.DB, error) {
	if mode.forWrite {
		return nil, errServerReadOnly(engine)
	}
	return r.openServerDatabase(ctx, engine)
}

// serverFailureText is the text of a MySQL, Oracle or SQL Server source that could not be opened: what failed,
// why when that can be told without reading the driver's message, and where the connection string is
// read from. It names no part of the URL.
func (r BackendRef) serverFailureText(engine serverEngine, err error) string {
	return fmt.Sprintf("the %s source could not be opened: %s; %s", engine.title, openFailureReason(err), r.serverConnectionHint(engine))
}

// serverConnectionHint is connectionHint for a MySQL or an Oracle source.
func (r BackendRef) serverConnectionHint(engine serverEngine) string {
	lead := "the " + engine.title + " connection string is read from "
	if name, isEnv := strings.CutPrefix(r.Raw, envPrefix); isEnv && ValidEnvName(name) {
		return lead + "the environment variable " + name
	}
	if r.flag != "" {
		return lead + "the " + r.flag + " flag"
	}
	return "the " + engine.title + " connection string is the one the source was given"
}
