package dbcopy

import "context"

// neverDialMySQL and neverDialOracle are the openers of this test binary (see TestMain): a test that reaches
// one did not stand in, and the real one would dial the host of the URL. The message names nothing of it.
func neverDialMySQL(string) (SchemaScanDB, error) {
	panic("a test of pkg/dbcopy opened a MySQL database for real: it must stand in and never dial")
}

func neverDialOracle(context.Context, string) (SchemaScanDB, error) {
	panic("a test of pkg/dbcopy opened an Oracle database for real: it must stand in and never dial")
}
