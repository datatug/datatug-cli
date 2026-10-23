package secureread

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

// runServerNativeSQL is RunNativeSQL for a PostgreSQL, MySQL, Oracle or SQL Server source. The source is opened
// as every read of one is (OpenProtected): a PostgreSQL session has default_transaction_read_only on, a MySQL
// session transaction_read_only, an Oracle read runs in a READ ONLY transaction, and a SQL Server read runs in a
// transaction that is always rolled back. None of these stops everything (a COMMIT inside a SQL Server batch
// ends the transaction, an Oracle function with an autonomous transaction commits its own writes), so before
// the text reaches the server it must be one query that writes nothing DataTug can see (see serverQueryText).
func (e *Executor) runServerNativeSQL(ctx context.Context, sourceURL string, ref dbcopy.BackendRef, sqlText string, args []dal.QueryArg) (Result, error) {
	text, err := serverQueryText(ref.Scheme, sqlText)
	if err != nil {
		return Result{}, err
	}
	db, closeSource, err := openSource(ctx, sourceURL, false, len(e.session.Policies))
	if err != nil {
		return Result{}, err
	}
	defer closeSource()
	if ref.Scheme == "postgres" {
		db = rolledBackReads{DB: db}
	}
	// A read through policies is refused before the source opens (dbcopy.CheckServerRead, in openSource), so a
	// session that gets here has none to label the result for: when that refusal goes, the result must carry
	// nativeSQLLimitation as RunNativeSQL's SQLite one does.
	return e.runThroughPolicies(ctx, db, dal.NewTextQuery(text, nil, args...), nil)
}

// rolledBackReads runs each read of PostgreSQL SQL text in a read-only transaction that is always rolled
// back. The session is read-only already (default_transaction_read_only), but a function a query calls can
// change a setting of the session for the rest of its life, the read-only one among them (set_config with
// is_local false), and the connection is kept in a pool: a rollback undoes such a change with the rest of
// the transaction. The rows are read in full inside the transaction, which ends when the read returns.
type rolledBackReads struct{ dal.DB }

// errReadRolledBack is what the worker of a rolled-back read returns to end its transaction with a rollback.
var errReadRolledBack = errors.New("the read is rolled back")

// ExecuteQueryToRecordsReader runs query in a read-only transaction that is rolled back.
func (r rolledBackReads) ExecuteQueryToRecordsReader(ctx context.Context, query dal.Query) (dal.RecordsReader, error) {
	var records []record.Record
	err := r.RunReadonlyTransaction(ctx, func(ctx context.Context, tx dal.ReadTransaction) error {
		reader, err := tx.ExecuteQueryToRecordsReader(ctx, query)
		if err != nil {
			return err
		}
		if records, err = dal.ReadAllToRecords(ctx, reader); err != nil {
			return err
		}
		return errReadRolledBack
	})
	if !errors.Is(err, errReadRolledBack) {
		return nil, err
	}
	return dal.NewRecordsReader(records), nil
}

// serverQueryText is sqlText without its trailing semicolons (the Oracle driver refuses one, and a
// MySQL connection runs one statement), when it is one query: led, past blank space, comments and opening
// parentheses, by SELECT or WITH, with no second statement and no word that writes, runs code or ends a
// transaction (writeWords) outside its string literals, quoted names and comments. A server runs what it is
// given as one batch: SQL Server runs "SELECT 1 COMMIT DELETE FROM t" as three statements, the COMMIT ending
// the transaction DataTug rolls back. Any other text is refused with ErrNativeSQLUnsupported. The check is
// lexical and errs on the side of refusing: a query that names a column UPDATE without quoting it is refused.
func serverQueryText(scheme, sqlText string) (string, error) {
	text := strings.TrimRight(strings.TrimSpace(sqlText), "; \t\r\n")
	words, refusal := sqlWords(scheme, text)
	if refusal == "" && (len(words) == 0 || words[0] != "SELECT" && words[0] != "WITH") {
		return "", fmt.Errorf("%w: on %s, DataTug runs a query (SELECT or WITH) and nothing else", ErrNativeSQLUnsupported, dbcopy.ServerTitle(scheme))
	}
	for _, word := range words {
		if refusal != "" {
			break
		}
		if writeWords[word] || scheme == "sqlserver" && (strings.HasPrefix(word, "XP_") || strings.HasPrefix(word, "SP_")) {
			refusal = word
		}
	}
	if refusal != "" {
		return "", fmt.Errorf("%w: on %s, DataTug runs one query (SELECT or WITH) and nothing else, and the text holds %s", ErrNativeSQLUnsupported, dbcopy.ServerTitle(scheme), refusal)
	}
	return text, nil
}

// writeWords are the words, upper case, that write, run code, lock or end a transaction in PostgreSQL, MySQL,
// Oracle or SQL Server (SET_CONFIG is PostgreSQL's function that sets a session's settings, the read-only one
// among them). A query has no use for them outside a string literal or a quoted name.
var writeWords = map[string]bool{
	"INSERT": true, "UPDATE": true, "DELETE": true, "MERGE": true, "UPSERT": true, "INTO": true, "OUTFILE": true, "DUMPFILE": true,
	"CREATE": true, "ALTER": true, "DROP": true, "TRUNCATE": true, "RENAME": true, "GRANT": true, "REVOKE": true, "DENY": true,
	"COMMIT": true, "ROLLBACK": true, "SAVEPOINT": true, "SAVE": true, "BEGIN": true, "TRANSACTION": true, "TRAN": true,
	"SET": true, "DECLARE": true, "EXEC": true, "EXECUTE": true, "CALL": true, "FUNCTION": true, "PROCEDURE": true, "PRAGMA": true,
	"TRIGGER": true, "LOCK": true, "UNLOCK": true, "HANDLER": true, "LOAD": true, "USE": true, "KILL": true, "SHUTDOWN": true,
	"RECONFIGURE": true, "DBCC": true, "BACKUP": true, "RESTORE": true, "BULK": true, "OPENROWSET": true, "OPENQUERY": true,
	"OPENDATASOURCE": true, "SET_CONFIG": true,
}

// functionWords are the writeWords that name a function, which a query calls by its name quoted too.
var functionWords = map[string]bool{"SET_CONFIG": true, "OPENROWSET": true, "OPENQUERY": true, "OPENDATASOURCE": true}

// sqlWords returns the words of text, upper case, outside its string literals, quoted names and comments, or the
// reason it refuses the text: a statement separator (";"), a MySQL or MariaDB comment the server runs ("/*!", "/*M!"), a comment
// inside a comment (SQL Server nests them, so where one ends depends on the engine), an Oracle quoted literal
// (q'...') or a backslash in a MySQL literal (whose ends depend on the server's settings), or a literal, a name
// or a comment left open. Where the engines differ it follows the one it reads for: MySQL takes "--" for a
// comment only before blank space, and "#" for one.
func sqlWords(scheme, text string) (words []string, refusal string) {
	for i := 0; i < len(text); {
		c := text[i]
		switch {
		case c == ';':
			return nil, "a second statement"
		case strings.HasPrefix(text[i:], "/*!") || len(text)-i >= 4 && strings.EqualFold(text[i:i+4], "/*M!"):
			return nil, "a MySQL comment that the server runs"
		case strings.HasPrefix(text[i:], "/*"):
			end := strings.Index(text[i+2:], "*/")
			if end < 0 {
				return nil, "a comment that is not closed"
			}
			if strings.Contains(text[i+2:i+2+end], "/*") {
				return nil, "a comment inside a comment"
			}
			i += 2 + end + 2
		case strings.HasPrefix(text[i:], "--") && (scheme != "mysql" || i+2 == len(text) || text[i+2] <= ' ') || c == '#' && scheme == "mysql":
			end := strings.IndexAny(text[i:], "\n\r")
			if end < 0 {
				return words, ""
			}
			i += end + 1
		case c == '\'' || c == '"' || c == '`' || c == '[' && scheme == "sqlserver":
			closing := c
			if c == '[' {
				closing = ']'
			}
			end, ok := closingQuote(text, i+1, closing)
			if !ok {
				return nil, "a string or a name that is not closed"
			}
			if (scheme == "mysql" || scheme == "postgres") && c != '`' && strings.IndexByte(text[i+1:end], '\\') >= 0 {
				return nil, "a backslash in a " + dbcopy.ServerTitle(scheme) + " literal"
			}
			if c != '\'' {
				// A quoted name is a name, and a name can call a function: one that writes is refused quoted too.
				if name := strings.ToUpper(text[i+1 : end]); functionWords[name] || scheme == "sqlserver" && (strings.HasPrefix(name, "XP_") || strings.HasPrefix(name, "SP_")) {
					return nil, name
				}
			}
			i = end + 1
		case scheme == "postgres" && c == '$' && (i+1 == len(text) || text[i+1] < '0' || text[i+1] > '9'):
			// A "$" is a parameter ($1) or the start of a dollar-quoted string, whose tag can be any name.
			return nil, "a PostgreSQL dollar-quoted string"
		case scheme == "postgres" && c >= 0x80:
			// PostgreSQL takes a letter that is not ASCII as a letter of a name: it is refused outside a
			// literal, so a name the lexer cannot read never starts or ends where the server's does.
			return nil, "a character that is not ASCII outside a string"
		case c >= '0' && c <= '9':
			// A number ends where a letter starts, as the servers read it: SQL Server runs "SELECT 1COMMIT" as
			// SELECT 1 and COMMIT, so the letters after the digits are a word of their own.
			for i < len(text) && text[i] >= '0' && text[i] <= '9' {
				i++
			}
		case isWordRune(rune(c)):
			start := i
			for i < len(text) && isWordRune(rune(text[i])) {
				i++
			}
			word := strings.ToUpper(text[start:i])
			if scheme == "oracle" && (word == "Q" || word == "NQ") && i < len(text) && text[i] == '\'' {
				return nil, "an Oracle quoted literal (q'...')"
			}
			if scheme == "postgres" && word == "U" && i < len(text) && text[i] == '&' {
				return nil, "a PostgreSQL Unicode-escaped name or string (U&)"
			}
			if word == "SET" && scheme == "mysql" && len(words) > 0 && words[len(words)-1] == "CHARACTER" {
				continue // how MySQL names a character set: CAST(x AS CHAR CHARACTER SET utf8mb4)
			}
			words = append(words, word)
		default:
			i++
		}
	}
	return words, ""
}

// closingQuote is the index of the quote that closes a literal or a name whose text starts at from: a doubled
// quote is one character of it.
func closingQuote(text string, from int, quote byte) (int, bool) {
	for i := from; i < len(text); i++ {
		switch {
		case text[i] == quote && i+1 < len(text) && text[i+1] == quote:
			i++
		case text[i] == quote:
			return i, true
		}
	}
	return 0, false
}

func isWordRune(r rune) bool {
	return r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}
