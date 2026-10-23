package secureread

import (
	"context"
	"fmt"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

// runServerNativeSQL is RunNativeSQL for a MySQL, Oracle or SQL Server source. The source is opened as
// every read of one is (OpenProtected, behind its preview switch): a MySQL session has
// transaction_read_only on and an Oracle read runs in a READ ONLY transaction, so the server refuses any
// write; SQL Server has no read-only session and relies on a read-only login. On top of that the text must be one
// query led by SELECT or WITH (see serverQueryText): a statement such as SET, which could turn the
// read-only session off, never reaches the server.
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
	// A read through policies is refused before the source opens (dbcopy.CheckServerRead, in openSource) while
	// these sources are a preview, so a session that gets here has none to label the result for: when that
	// refusal goes, the result must carry nativeSQLLimitation as RunNativeSQL's SQLite one does.
	return e.runThroughPolicies(ctx, db, dal.NewTextQuery(text, nil, args...), nil)
}

// serverQueryText is sqlText without its trailing semicolons (the Oracle driver refuses one, and a
// MySQL connection runs one statement), when it is a query: led, past blank space, comments and
// opening parentheses, by SELECT or WITH. Any other text is refused with ErrNativeSQLUnsupported.
func serverQueryText(scheme, sqlText string) (string, error) {
	text := strings.TrimRight(strings.TrimSpace(sqlText), "; \t\r\n")
	rest := text
	for {
		trimmed := strings.TrimLeft(rest, " \t\r\n(")
		switch {
		case strings.HasPrefix(trimmed, "--"):
			_, after, found := strings.Cut(trimmed, "\n")
			if !found {
				after = ""
			}
			rest = after
			continue
		case strings.HasPrefix(trimmed, "/*"):
			_, after, found := strings.Cut(trimmed[2:], "*/")
			if !found {
				after = ""
			}
			rest = after
			continue
		}
		rest = trimmed
		break
	}
	word := rest
	if end := strings.IndexFunc(rest, func(r rune) bool { return !isWordRune(r) }); end >= 0 {
		word = rest[:end]
	}
	switch strings.ToUpper(word) {
	case "SELECT", "WITH":
		return text, nil
	}
	return "", fmt.Errorf("%w: on %s, DataTug runs a query (SELECT or WITH) and nothing else", ErrNativeSQLUnsupported, dbcopy.ServerTitle(scheme))
}

func isWordRune(r rune) bool {
	return r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}
