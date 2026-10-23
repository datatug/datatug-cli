package dbcopy

import "context"

// ServerOpeners are the constructors a MySQL and an Oracle source are opened through: the adapter's own
// in production, a fake or one that stops the run in a test.
type ServerOpeners struct {
	MySQL  func(dsn string) (SchemaScanDB, error)
	Oracle func(ctx context.Context, dsn string) (SchemaScanDB, error)
}

// realServerOpeners are the openers of production, kept so a journey test can put them back.
var realServerOpeners = ServerOpeners{MySQL: newMySQLDatabase, Oracle: newOracleDatabase}

// RealServerOpeners are the openers of production: a journey test of CI puts them back for the length of
// one test, in place of the ones a test binary stops the run with.
func RealServerOpeners() ServerOpeners { return realServerOpeners }

// SetServerOpenersForTest replaces the constructors of a MySQL and an Oracle database that BackendRef
// opens a source through, for the tests of the packages that reach one: a test stands in with a fake, or
// a whole test binary installs openers that stop the run, and no test dials a server. A nil opener keeps
// the current one. The function it returns puts both back.
//
// NEVER call this from production code.
func SetServerOpenersForTest(openers ServerOpeners) (restore func()) {
	previous := ServerOpeners{MySQL: newMySQLDatabase, Oracle: newOracleDatabase}
	if openers.MySQL != nil {
		newMySQLDatabase = openers.MySQL
	}
	if openers.Oracle != nil {
		newOracleDatabase = openers.Oracle
	}
	return func() { newMySQLDatabase, newOracleDatabase = previous.MySQL, previous.Oracle }
}
