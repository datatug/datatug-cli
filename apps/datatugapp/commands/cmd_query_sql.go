package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/spf13/cobra"
)

// querySQLFlag is `query run --sql`: SQL text to run against --db, '-' for stdin.
const querySQLFlag = "sql"

// runSQLQueryCommand runs `query run --db ... --sql TEXT`: the SQL text as it is, through
// secureread.Executor.RunNativeSQL, the path a saved SQL query of a project takes, so it is
// checked and read as one is. On a PostgreSQL, MySQL, Oracle or SQL Server source the text must
// be one query (SELECT or WITH) that writes nothing, and the session is read-only; on a SQLite
// source the file is opened read-only. SQL text cannot be read through access policies' row and
// column rules, so with policies loaded it is refused unless they name an opaque-query rule; with
// none loaded, or --no-policies, it runs unrestricted, as `--db ... -f` does.
func runSQLQueryCommand(cmd *cobra.Command, o queryOptions) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	text, err := readSQLText(o.sql, cmd.InOrStdin())
	if err != nil {
		return err
	}
	sourceURL, err := sqlSourceURL(ctx, o.db)
	if err != nil {
		return err
	}
	loaded, err := accesspolicies.Load(accesspolicies.LoadOptions{Dir: o.policiesDir, Files: o.policies, None: o.noPolicies})
	if err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}
	session := secureread.Session{Policies: loaded, Unrestricted: o.noPolicies || len(loaded) == 0, AllowOpaqueSQL: o.allowOpaqueSQL}
	if o.as != "" || len(o.roles) > 0 || len(o.groups) > 0 {
		session.Principal = &access.Principal{ID: o.as, Roles: o.roles, Groups: o.groups}
	}
	stderr := cmd.ErrOrStderr()
	if session.Unrestricted {
		_, _ = fmt.Fprintln(stderr, "access: running without access policies")
	}
	var args []dal.QueryArg
	if ref, parseErr := dbcopy.Parse(sourceURL); parseErr == nil && dbcopy.IsClickHouseScheme(ref.Scheme) {
		variables, parseErr := parseClickHouseVariables(o.vars)
		if parseErr != nil {
			return Exit(parseErr.Error(), exitCodeUsage)
		}
		names := make([]string, 0, len(variables))
		for name := range variables {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			args = append(args, dal.QueryArg{Name: name, Value: variables[name]})
		}
	}
	result, err := secureread.NewExecutor(session).RunNativeSQL(ctx, sourceURL, text, args...)
	if errors.Is(err, secureread.ErrOpaqueSQLNotGranted) {
		return Exit(err.Error()+": SQL text is not read through the row and column rules of access policies; use --allow-opaque-sql with an opaque-query policy grant, or run it with --no-policies", exitCodeUsage)
	}
	if err != nil {
		return savedQueryFailure(err)
	}
	if !o.quiet {
		writeSavedQueryLimitations(stderr, result.Limitations)
	}
	if result.AnonymousRecordset != nil {
		return writeAnonymousResult(cmd.OutOrStdout(), o.format, result)
	}
	if err := writeQueryRows(cmd.OutOrStdout(), o.format, result.Columns, secureRowsToQueryRows(result.Rows), nil); err != nil {
		return Exit(err.Error(), 1)
	}
	return nil
}

// readSQLText is the SQL text of --sql: the flag's value, or stdin for "-".
func readSQLText(value string, stdin io.Reader) (string, error) {
	text := value
	if value == "-" {
		content, err := io.ReadAll(stdin)
		if err != nil {
			return "", Exit(fmt.Sprintf("read --sql from stdin: %v", err), exitCodeUsage)
		}
		text = string(content)
	}
	if strings.TrimSpace(text) == "" {
		return "", Exit("--sql is empty", exitCodeUsage)
	}
	return text, nil
}

// sqlSourceURL is the source --db names, as RunNativeSQL takes it: the value as it is, or for a
// datatug:// reference the source of the project it names.
func sqlSourceURL(ctx context.Context, db string) (string, error) {
	if !dbcopy.IsDatatugRef(db) {
		if _, err := dbcopy.Parse(db); err != nil {
			return "", Exit(err.Error(), exitCodeUsage)
		}
		return db, nil
	}
	reference, err := dbcopy.ParseDatatugRef(db)
	if err != nil {
		return "", Exit(err.Error(), exitCodeUsage)
	}
	return datatugRefSource(ctx, reference)
}
