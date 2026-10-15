package endpoints

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	_ "modernc.org/sqlite"
)

// lookupFixture is the semantic fixture project plus tables made for the lookup: Big (integer
// primary key with the neighbours of 2^53+1), Tag (string primary key) and Loose (no primary key).
func lookupFixture(t *testing.T, as string, roles []string) (projectID string) {
	t.Helper()
	dir, projectID := writeSemanticTestProject(t)
	db, err := sql.Open("sqlite", filepath.Join(dir, "chinook.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE Big (Id INTEGER NOT NULL PRIMARY KEY, Label TEXT)`,
		`INSERT INTO Big VALUES (9007199254740992,'below'),(9007199254740993,'exact'),(9007199254740994,'above'),(1,'one'),(2,'two')`,
		`CREATE TABLE Tag (Name TEXT NOT NULL PRIMARY KEY, Note TEXT)`,
		`INSERT INTO Tag VALUES ('1 OR 1=1','sneaky'),('x','plain')`,
		`CREATE TABLE Loose (A INTEGER, B TEXT)`,
		`INSERT INTO Loose VALUES (1,'a')`,
		`CREATE TABLE Flt (F REAL NOT NULL PRIMARY KEY, Note TEXT)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	configureSemanticSession(t, dir, projectID, as, roles)
	return projectID
}

type lookupQuerySpy struct{ calls int }

func spyQueries(t *testing.T) *lookupQuerySpy {
	t.Helper()
	spy := &lookupQuerySpy{}
	previous := runStructuredRelatedHook
	runStructuredRelatedHook = func(e *secureread.Executor, ctx context.Context, source string, q dal.StructuredQuery, v map[string]any) (secureread.Result, error) {
		spy.calls++
		return previous(e, ctx, source, q, v)
	}
	t.Cleanup(func() { runStructuredRelatedHook = previous })
	return spy
}

type lookupAnswer struct {
	status  int
	raw     string
	result  apicontract.Result
	errBody struct {
		Error struct {
			Code    string `json:"code"`
			Field   string `json:"field"`
			Message string `json:"message"`
		} `json:"error"`
	}
}

func doLookup(t *testing.T, body string) lookupAnswer {
	t.Helper()
	w := httptest.NewRecorder()
	recordsLookupHandler(w, httptest.NewRequest(http.MethodPost, "/datatug/records/lookup", strings.NewReader(body)))
	a := lookupAnswer{status: w.Code, raw: w.Body.String()}
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &a.result); err != nil {
			t.Fatalf("decode result: %v", err)
		}
	} else if err := json.Unmarshal(w.Body.Bytes(), &a.errBody); err != nil {
		t.Fatalf("decode error: %v: %s", err, w.Body.String())
	}
	return a
}

func lookupBody(project, source, schema, table, key string) string {
	return `{"project":"` + project + `","env":"local","source":"` + source + `","schema":"` + schema + `","table":"` + table + `","key":` + key + `}`
}

// rowText is the recordset of an answer as JSON text, for substring checks of a value.
func rowText(a lookupAnswer) string {
	raw, _ := json.Marshal(a.result.Recordset.Rows)
	return string(raw)
}

func TestRecordsLookup_Table(t *testing.T) {
	project := lookupFixture(t, "alice", []string{"admin"})
	spy := spyQueries(t)

	type want struct {
		status  int
		code    string
		field   string
		queries int
		row     string // substring of the row, for a 200
	}
	cases := []struct {
		name string
		body string
		want want
	}{
		{"single key", lookupBody(project, "chinook", "", "Genre", `{"GenreId":1}`), want{200, "", "", 1, "Rock"}},
		{"default schema qualifier", lookupBody(project, "chinook", "main", "Genre", `{"GenreId":1}`), want{200, "", "", 1, "Rock"}},
		{"composite key", lookupBody(project, "chinook", "", "PlaylistTrack", `{"PlaylistId":1,"TrackId":3402}`), want{200, "", "", 1, "3402"}},
		{"composite key order irrelevant", lookupBody(project, "chinook", "", "PlaylistTrack", `{"TrackId":3402,"PlaylistId":1}`), want{200, "", "", 1, "3402"}},
		{"2^53+1 as json number", lookupBody(project, "chinook", "", "Big", `{"Id":9007199254740993}`), want{200, "", "", 1, "exact"}},
		{"2^53+1 as string", lookupBody(project, "chinook", "", "Big", `{"Id":"9007199254740993"}`), want{200, "", "", 1, "exact"}},
		{"neighbour is a different record", lookupBody(project, "chinook", "", "Big", `{"Id":"9007199254740994"}`), want{200, "", "", 1, "above"}},
		{"negative integer not found", lookupBody(project, "chinook", "", "Big", `{"Id":-5}`), want{404, "NOT_FOUND", "key", 1, ""}},
		{"string pk with injection text is only a value", lookupBody(project, "chinook", "", "Tag", `{"Name":"1 OR 1=1"}`), want{200, "", "", 1, "sneaky"}},
		{"string pk quote is only a value", lookupBody(project, "chinook", "", "Tag", `{"Name":"x' OR '1'='1"}`), want{404, "NOT_FOUND", "key", 1, ""}},
		{"record not found", lookupBody(project, "chinook", "", "Genre", `{"GenreId":99999}`), want{404, "NOT_FOUND", "key", 1, ""}},

		{"injection in integer key", lookupBody(project, "chinook", "", "Genre", `{"GenreId":"1 OR 1=1"}`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"trailing text integer key", lookupBody(project, "chinook", "", "Genre", `{"GenreId":"1; DROP TABLE Genre"}`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"fraction for integer", lookupBody(project, "chinook", "", "Genre", `{"GenreId":"1.0"}`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"fraction number for integer", lookupBody(project, "chinook", "", "Genre", `{"GenreId":1.5}`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"exponent for integer", lookupBody(project, "chinook", "", "Genre", `{"GenreId":1e3}`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"leading plus", lookupBody(project, "chinook", "", "Genre", `{"GenreId":"+1"}`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"leading space", lookupBody(project, "chinook", "", "Genre", `{"GenreId":" 1"}`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"leading zero", lookupBody(project, "chinook", "", "Genre", `{"GenreId":"01"}`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"minus zero", lookupBody(project, "chinook", "", "Genre", `{"GenreId":"-0"}`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"empty string integer", lookupBody(project, "chinook", "", "Genre", `{"GenreId":""}`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"bare minus", lookupBody(project, "chinook", "", "Genre", `{"GenreId":"-"}`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"integer beyond int64", lookupBody(project, "chinook", "", "Genre", `{"GenreId":"9223372036854775808"}`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"number for string column", lookupBody(project, "chinook", "", "Tag", `{"Name":1}`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"too long string", lookupBody(project, "chinook", "", "Tag", `{"Name":"`+strings.Repeat("a", 257)+`"}`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"null value", lookupBody(project, "chinook", "", "Genre", `{"GenreId":null}`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"object value", lookupBody(project, "chinook", "", "Genre", `{"GenreId":{"$gt":0}}`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"array value", lookupBody(project, "chinook", "", "Genre", `{"GenreId":[1]}`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"bool value", lookupBody(project, "chinook", "", "Genre", `{"GenreId":true}`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"non-key column", lookupBody(project, "chinook", "", "Genre", `{"Name":"Rock"}`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"non-key column added to key", lookupBody(project, "chinook", "", "Genre", `{"GenreId":1,"Name":"Rock"}`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"unknown column", lookupBody(project, "chinook", "", "Genre", `{"Nope":1}`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"unknown column replacing a key column", lookupBody(project, "chinook", "", "PlaylistTrack", `{"PlaylistId":1,"Nope":1}`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"missing key column", lookupBody(project, "chinook", "", "PlaylistTrack", `{"PlaylistId":1}`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"extra key column", lookupBody(project, "chinook", "", "PlaylistTrack", `{"PlaylistId":1,"TrackId":3402,"X":1}`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"empty key", lookupBody(project, "chinook", "", "Genre", `{}`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"key is null", lookupBody(project, "chinook", "", "Genre", `null`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"key is an array", lookupBody(project, "chinook", "", "Genre", `[1]`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"key is a scalar", lookupBody(project, "chinook", "", "Genre", `1`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"table without a primary key", lookupBody(project, "chinook", "", "Loose", `{"A":1}`), want{400, "INVALID_REQUEST", "key", 0, ""}},
		{"unsupported key type", lookupBody(project, "chinook", "", "Flt", `{"F":1}`), want{400, "INVALID_REQUEST", "key", 0, ""}},

		{"unknown table", lookupBody(project, "chinook", "", "NoSuchTable", `{"A":1}`), want{404, "NOT_FOUND", "table", 0, ""}},
		{"unknown project", lookupBody("no-such-project", "chinook", "", "Genre", `{"GenreId":1}`), want{404, "NOT_FOUND", "project", 0, ""}},
		{"unknown source", lookupBody(project, "no-such-db", "", "Genre", `{"GenreId":1}`), want{404, "NOT_FOUND", "source", 0, ""}},
		{"unknown environment", `{"project":"` + project + `","env":"nope","source":"chinook","table":"Genre","key":{"GenreId":1}}`, want{404, "NOT_FOUND", "source", 0, ""}},

		{"connection string as source", lookupBody(project, "sqlite:///etc/passwd", "", "Genre", `{"GenreId":1}`), want{400, "INVALID_REQUEST", "source", 0, ""}},
		{"postgres dsn as source", lookupBody(project, "postgres://u:p@h/db", "", "Genre", `{"GenreId":1}`), want{400, "INVALID_REQUEST", "source", 0, ""}},
		{"path as source", lookupBody(project, "../chinook.sqlite", "", "Genre", `{"GenreId":1}`), want{400, "INVALID_REQUEST", "source", 0, ""}},
		{"connection string as project", lookupBody("postgres://u:p@h/db", "chinook", "", "Genre", `{"GenreId":1}`), want{400, "INVALID_REQUEST", "project", 0, ""}},
		{"connection string as env", `{"project":"` + project + `","env":"mysql://u:p@h/db","source":"chinook","table":"Genre","key":{"GenreId":1}}`, want{400, "INVALID_REQUEST", "env", 0, ""}},
		{"connection string as table", lookupBody(project, "chinook", "", "sqlite://x.db", `{"GenreId":1}`), want{400, "INVALID_REQUEST", "table", 0, ""}},
		{"path as table", lookupBody(project, "chinook", "", "/etc/passwd", `{"GenreId":1}`), want{400, "INVALID_REQUEST", "table", 0, ""}},
		{"backslash path as table", lookupBody(project, "chinook", "", `..\\x`, `{"GenreId":1}`), want{400, "INVALID_REQUEST", "table", 0, ""}},
		{"connection string as schema", lookupBody(project, "chinook", "postgres://u:p@h/db", "Genre", `{"GenreId":1}`), want{400, "INVALID_REQUEST", "schema", 0, ""}},
		{"control character in table", lookupBody(project, "chinook", "", `Genre\u0000`, `{"GenreId":1}`), want{400, "INVALID_REQUEST", "table", 0, ""}},
		{"too long table", lookupBody(project, "chinook", "", strings.Repeat("a", 257), `{"GenreId":1}`), want{400, "INVALID_REQUEST", "table", 0, ""}},

		{"missing project", `{"env":"local","source":"chinook","table":"Genre","key":{}}`, want{400, "MISSING_PARAMETER", "", 0, ""}},
		{"missing source (no default source)", `{"project":"` + project + `","env":"local","table":"Genre","key":{"GenreId":1}}`, want{400, "MISSING_PARAMETER", "", 0, ""}},
		{"missing table", `{"project":"` + project + `","env":"local","source":"chinook","key":{"GenreId":1}}`, want{400, "MISSING_PARAMETER", "", 0, ""}},
		{"unknown request field", `{"project":"` + project + `","env":"local","source":"chinook","table":"Genre","key":{"GenreId":1},"where":"1=1"}`, want{400, "INVALID_REQUEST", "body", 0, ""}},
		{"not json", `not json`, want{400, "INVALID_REQUEST", "body", 0, ""}},
		{"trailing json", `{} {}`, want{400, "INVALID_REQUEST", "body", 0, ""}},
		{"no body", ``, want{400, "INVALID_REQUEST", "body", 0, ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := spy.calls
			a := doLookup(t, tc.body)
			if a.status != tc.want.status {
				t.Fatalf("status = %d, want %d; body = %s", a.status, tc.want.status, a.raw)
			}
			if got := spy.calls - before; got != tc.want.queries {
				t.Fatalf("queries issued = %d, want %d", got, tc.want.queries)
			}
			if tc.want.status == 200 {
				if len(a.result.Recordset.Rows) != 1 {
					t.Fatalf("rows = %d, want exactly 1", len(a.result.Recordset.Rows))
				}
				if !strings.Contains(rowText(a), tc.want.row) {
					t.Fatalf("row %q lacks %q", rowText(a), tc.want.row)
				}
				return
			}
			if a.errBody.Error.Code != tc.want.code {
				t.Fatalf("code = %q, want %q; body = %s", a.errBody.Error.Code, tc.want.code, a.raw)
			}
			if tc.want.field != "" && a.errBody.Error.Field != tc.want.field {
				t.Fatalf("field = %q, want %q", a.errBody.Error.Field, tc.want.field)
			}
			// No answer echoes a value of the request.
			for _, secret := range []string{"postgres://", "mysql://", "sqlite://", "/etc/passwd", "u:p@h", "DROP TABLE", "1 OR 1=1", "x' OR", "Nope", "NoSuchTable", "no-such"} {
				if strings.Contains(a.raw, secret) {
					t.Fatalf("answer echoes %q: %s", secret, a.raw)
				}
			}
		})
	}
}

// The key 9007199254740993 is bound as that exact int64, never as a float or a neighbour.
func TestRecordsLookup_BindsTheKeyAsATypedParameter(t *testing.T) {
	project := lookupFixture(t, "alice", []string{"admin"})
	var captured []dal.StructuredQuery
	previous := runStructuredRelatedHook
	runStructuredRelatedHook = func(e *secureread.Executor, ctx context.Context, source string, q dal.StructuredQuery, v map[string]any) (secureread.Result, error) {
		captured = append(captured, q)
		return previous(e, ctx, source, q, v)
	}
	t.Cleanup(func() { runStructuredRelatedHook = previous })

	for _, key := range []string{`{"Id":9007199254740993}`, `{"Id":"9007199254740993"}`} {
		a := doLookup(t, lookupBody(project, "chinook", "", "Big", key))
		if a.status != 200 || !strings.Contains(rowText(a), "exact") {
			t.Fatalf("%s: status %d body %s", key, a.status, a.raw)
		}
	}
	if len(captured) != 2 {
		t.Fatalf("queries = %d, want 2", len(captured))
	}
	for _, q := range captured {
		cmp, ok := q.Where().(dal.Comparison)
		if !ok {
			t.Fatalf("where = %T, want one comparison", q.Where())
		}
		constant, ok := cmp.Right.(dal.Constant)
		if !ok || constant.Value != int64(9007199254740993) {
			t.Fatalf("key operand = %#v, want the int64 constant 9007199254740993 bound as a parameter", cmp.Right)
		}
	}
	if v, err := bindKeyValue(json.Number("9007199254740993"), "int"); err != nil || v != int64(9007199254740993) {
		t.Fatalf("bound = %v (%T), %v", v, v, err)
	}
}

// A principal the policy refuses sees nothing of the record, and no row is returned.
func TestRecordsLookup_AccessDeniedPrincipalSeesNothing(t *testing.T) {
	project := lookupFixture(t, "bob", []string{"support"})
	// support may read Customer only.
	allowed := doLookup(t, lookupBody(project, "chinook", "", "Customer", `{"CustomerId":5}`))
	if allowed.status != 200 || len(allowed.result.Recordset.Rows) != 1 {
		t.Fatalf("support reading Customer: status %d body %s", allowed.status, allowed.raw)
	}
	denied := doLookup(t, lookupBody(project, "chinook", "", "Invoice", `{"InvoiceId":1}`))
	if denied.status != http.StatusForbidden || denied.errBody.Error.Code != "ACCESS_DENIED" {
		t.Fatalf("status %d body %s, want 403 ACCESS_DENIED", denied.status, denied.raw)
	}
	if len(denied.result.Recordset.Rows) != 0 || strings.Contains(denied.raw, "recordset") {
		t.Fatalf("a denied lookup returned data: %s", denied.raw)
	}
}

// The agent log never holds the body, a key or a row, on success or on failure.
func TestRecordsLookup_LogsNoKeyOrRowValue(t *testing.T) {
	project := lookupFixture(t, "alice", []string{"admin"})
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(testLogOutput) })

	previous := runStructuredRelatedHook
	t.Cleanup(func() { runStructuredRelatedHook = previous })
	doLookup(t, lookupBody(project, "chinook", "", "Big", `{"Id":9007199254740993}`))
	doLookup(t, lookupBody(project, "chinook", "", "Big", `{"Id":"9007199254740993 OR 1=1"}`))
	doLookup(t, lookupBody(project, "chinook", "", "Tag", `{"Name":"1 OR 1=1"}`))
	runStructuredRelatedHook = func(*secureread.Executor, context.Context, string, dal.StructuredQuery, map[string]any) (secureread.Result, error) {
		return secureread.Result{}, errors.New("driver failure near 9007199254740993 exact sneaky")
	}
	a := doLookup(t, lookupBody(project, "chinook", "", "Big", `{"Id":9007199254740993}`))
	if a.status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", a.status)
	}
	for _, secret := range []string{"9007199254740993", "exact", "sneaky", "1 OR 1=1", "below"} {
		if strings.Contains(buf.String(), secret) || strings.Contains(a.raw, secret) {
			t.Fatalf("log or answer holds %q: log=%s answer=%s", secret, buf.String(), a.raw)
		}
	}
}

var testLogOutput = log.Writer()

func TestRecordsLookup_RouteIsRegisteredBehindTheGuardAndReadOnly(t *testing.T) {
	for _, writeOnly := range []bool{false, true} {
		r := &recordingRouter{handlers: map[string]http.HandlerFunc{}}
		registerRoutes("", r, func(f http.HandlerFunc) http.HandlerFunc { return f }, writeOnly, Capabilities{})
		for _, route := range []string{"lookup", "schema"} {
			_, found := r.handlers[http.MethodPost+" /datatug/records/"+route]
			if found == writeOnly {
				t.Fatalf("writeOnly=%v: %s registered=%v", writeOnly, route, found)
			}
		}
	}
}

func TestComputeRecordsLookup_Seams(t *testing.T) {
	project := lookupFixture(t, "alice", []string{"admin"})
	req := recordsLookupRequest{recordsTableRequest: recordsTableRequest{Project: project, Env: "local", Source: "chinook", Table: "Genre"}, Key: json.RawMessage(`{"GenreId":1}`)}
	ctx := context.Background()
	assertCode := func(t *testing.T, err error, code apicontract.ErrorCode) {
		t.Helper()
		assertContractError(t, err, code)
	}

	t.Run("project store failure", func(t *testing.T) {
		prev := relatedProjectStoreFor
		relatedProjectStoreFor = func(string) (datatug.ProjectStore, error) { return nil, errors.New("secret path /x") }
		defer func() { relatedProjectStoreFor = prev }()
		_, err := computeRecordsLookup(ctx, req)
		assertCode(t, err, apicontract.ErrCodeInvalidRequest)
		if strings.Contains(err.Error(), "/x") {
			t.Fatalf("echoes the cause: %v", err)
		}
	})
	t.Run("registry failure that is not an unknown source", func(t *testing.T) {
		prev := apiResolveSource
		apiResolveSource = func(context.Context, datatug.ProjectStore, string, string, string) (api.ResolvedSource, error) {
			return api.ResolvedSource{}, errors.New("registry broke")
		}
		defer func() { apiResolveSource = prev }()
		_, err := computeRecordsLookup(ctx, req)
		assertCode(t, err, apicontract.ErrCodeSourceUnavailable)
	})
	t.Run("schema read failure classified by the resolver", func(t *testing.T) {
		err := recordsLookupSchemaError(newSourceUnavailable("x"))
		assertCode(t, err, apicontract.ErrCodeSourceUnavailable)
		assertCode(t, recordsLookupSchemaError(errors.New("plain")), apicontract.ErrCodeNotFound)
	})
	t.Run("no policy session", func(t *testing.T) {
		prev := secureExecutorHook
		secureExecutorHook = func() (*secureread.Executor, bool) { return nil, false }
		defer func() { secureExecutorHook = prev }()
		_, err := computeRecordsLookup(ctx, req)
		assertCode(t, err, codeInternal)
	})
	run := func(err error) error {
		prev := runStructuredRelatedHook
		runStructuredRelatedHook = func(*secureread.Executor, context.Context, string, dal.StructuredQuery, map[string]any) (secureread.Result, error) {
			return secureread.Result{}, err
		}
		defer func() { runStructuredRelatedHook = prev }()
		_, got := computeRecordsLookup(ctx, req)
		return got
	}
	t.Run("timeout", func(t *testing.T) { assertCode(t, run(context.DeadlineExceeded), apicontract.ErrCodeTimeout) })
	t.Run("denied", func(t *testing.T) { assertCode(t, run(secureread.ErrAccessDenied), apicontract.ErrCodeAccessDenied) })
	t.Run("source unavailable", func(t *testing.T) {
		assertCode(t, run(dbcopy.ErrSourceFileMissing), apicontract.ErrCodeSourceUnavailable)
	})
	t.Run("other failure", func(t *testing.T) { assertCode(t, run(errors.New("boom")), codeInternal) })
	t.Run("record encoding failure", func(t *testing.T) {
		prev := toContractRecordsetHook
		toContractRecordsetHook = func(secureread.Result) (apicontract.Recordset, error) {
			return apicontract.Recordset{}, errors.New("x")
		}
		defer func() { toContractRecordsetHook = prev }()
		_, err := computeRecordsLookup(ctx, req)
		assertCode(t, err, codeInternal)
	})
	t.Run("response validation failure", func(t *testing.T) {
		prev := relatedResultValidateHook
		relatedResultValidateHook = func(*apicontract.Result) error { return errors.New("x") }
		defer func() { relatedResultValidateHook = prev }()
		_, err := computeRecordsLookup(ctx, req)
		assertCode(t, err, codeInternal)
	})
}

// An integer key above 2^53 reads a bucket of candidates and keeps only the exact one: a bucket
// with no exact row is not found, a bucket as wide as the read is an error, never a neighbour.
func TestRecordsLookup_ExactBucket(t *testing.T) {
	project := lookupFixture(t, "alice", []string{"admin"})
	req := recordsLookupRequest{recordsTableRequest: recordsTableRequest{Project: project, Env: "local", Source: "chinook", Table: "Big"}, Key: json.RawMessage(`{"Id":9007199254740993}`)}
	rows := func(n int, id int64) []secureread.Row {
		out := make([]secureread.Row, n)
		for i := range out {
			out[i] = secureread.Row{Data: map[string]any{"Id": id, "Label": "neighbour"}}
		}
		return out
	}
	prev := runStructuredRelatedHook
	defer func() { runStructuredRelatedHook = prev }()
	for _, tc := range []struct {
		name string
		rows []secureread.Row
		code apicontract.ErrorCode
	}{
		{"no exact row, bucket not full", rows(2, 9007199254740992), apicontract.ErrCodeNotFound},
		{"no exact row, bucket full", rows(recordsLookupExactCandidates, 9007199254740992), codeInternal},
		{"key column hidden from the row", []secureread.Row{{Data: map[string]any{"Label": "x"}}}, apicontract.ErrCodeNotFound},
		{"key column as a float", []secureread.Row{{Data: map[string]any{"Id": float64(9007199254740992)}}}, apicontract.ErrCodeNotFound},
	} {
		runStructuredRelatedHook = func(*secureread.Executor, context.Context, string, dal.StructuredQuery, map[string]any) (secureread.Result, error) {
			return secureread.Result{Columns: []string{"Id", "Label"}, Rows: tc.rows}, nil
		}
		_, err := computeRecordsLookup(context.Background(), req)
		if err == nil {
			t.Fatalf("%s: no error", tc.name)
		}
		assertContractError(t, err, tc.code)
	}
	// The exact row among its neighbours is the one answered.
	runStructuredRelatedHook = func(*secureread.Executor, context.Context, string, dal.StructuredQuery, map[string]any) (secureread.Result, error) {
		mixed := append(rows(1, 9007199254740992), secureread.Row{Data: map[string]any{"Id": int64(9007199254740993), "Label": "exact"}})
		mixed[0].Data["Id"] = int64(9007199254740992)
		return secureread.Result{Columns: []string{"Id", "Label"}, Rows: mixed}, nil
	}
	got, err := computeRecordsLookup(context.Background(), req)
	if err != nil || len(got.Recordset.Rows) != 1 {
		t.Fatalf("got %v, %v", got, err)
	}
	if raw, _ := json.Marshal(got.Recordset.Rows); !strings.Contains(string(raw), "9007199254740993") {
		t.Fatalf("row = %s", raw)
	}
}

func TestRecordsLookup_BodyTooLarge(t *testing.T) {
	big := `{"project":"` + strings.Repeat("a", recordsLookupMaxBody) + `"}`
	a := doLookup(t, big)
	if a.status != http.StatusBadRequest || a.errBody.Error.Field != "body" {
		t.Fatalf("status %d body %s", a.status, a.raw)
	}
}

func TestDriverAndCollectionNames(t *testing.T) {
	for url, want := range map[string]string{
		"sqlite://x": "sqlite3", "sqlite3://x": "sqlite3", "postgres://x": "postgres", "postgresql://x": "postgres", "ingitdb://x": "", "": "",
	} {
		if got := driverOfURL(url); got != want {
			t.Errorf("driverOfURL(%q) = %q, want %q", url, got, want)
		}
	}
	if got := lookupCollectionName("main", "T", "sqlite3"); got != "T" {
		t.Errorf("default schema: %q", got)
	}
	if got := lookupCollectionName("archive", "T", "sqlite3"); got != "archive.T" {
		t.Errorf("other schema: %q", got)
	}
	if got := lookupCollectionName("", "T", ""); got != "T" {
		t.Errorf("no schema: %q", got)
	}
}

func doSchema(t *testing.T, body string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	recordsSchemaHandler(w, httptest.NewRequest(http.MethodPost, "/datatug/records/schema", strings.NewReader(body)))
	return w.Code, w.Body.String()
}

// The live schema of a registered source that was never scanned (the fixture has no scan).
func TestRecordsSchema(t *testing.T) {
	project := lookupFixture(t, "alice", []string{"admin"})
	code, raw := doSchema(t, `{"project":"`+project+`","env":"local","source":"chinook","schema":"main","table":"PlaylistTrack"}`)
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, raw)
	}
	var got recordsSchemaResponse
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.PrimaryKey, ",") != "PlaylistId,TrackId" || len(got.Columns) != 2 || got.Columns[0].Type != "int" {
		t.Fatalf("schema = %+v", got)
	}
	code, raw = doSchema(t, `{"project":"`+project+`","env":"local","source":"chinook","table":"Genre"}`)
	var genre recordsSchemaResponse
	if err := json.Unmarshal([]byte(raw), &genre); err != nil || code != 200 || len(genre.ReferencedBy) == 0 {
		t.Fatalf("Genre: %d %s %v", code, raw, err)
	}
	code, raw = doSchema(t, `{"project":"`+project+`","env":"local","source":"chinook","table":"Loose"}`)
	var loose recordsSchemaResponse
	if err := json.Unmarshal([]byte(raw), &loose); err != nil || code != 200 || len(loose.PrimaryKey) != 0 || loose.PrimaryKey == nil {
		t.Fatalf("Loose: %d %s %v", code, raw, err)
	}
	for body, want := range map[string]int{
		`{"project":"` + project + `","env":"local","source":"chinook","table":"NoSuchTable"}`:      404,
		`{"project":"` + project + `","env":"local","source":"postgres://u:p@h/d","table":"Genre"}`: 400,
		`{"project":"` + project + `","env":"local","source":"nodb","table":"Genre"}`:               404,
		`{"project":"nope","env":"local","source":"chinook","table":"Genre"}`:                       404,
		`{"project":"` + project + `","env":"local","source":"chinook","table":"Genre","key":{}}`:   400,
	} {
		if code, raw := doSchema(t, body); code != want || strings.Contains(raw, "postgres://") {
			t.Fatalf("%s: status %d: %s", body, code, raw)
		}
	}
	w := httptest.NewRecorder()
	recordsSchemaHandler(w, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"project":"`+strings.Repeat("a", recordsLookupMaxBody)+`"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized body: %d", w.Code)
	}
}
