package dbcopy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	dalgo2libsql "github.com/dal-go/dalgo2libsql"
)

func TestParseLibSQLSource(t *testing.T) {
	t.Parallel()
	accepted := []struct {
		source   string
		scheme   string
		host     string
		path     string
		loopback bool
		endpoint string
	}{
		{"libsql://db.example.com", LibSQLScheme, "db.example.com", "", false, "https://db.example.com"},
		{"libsql://db.example.com:8080", LibSQLScheme, "db.example.com:8080", "", false, "https://db.example.com:8080"},
		{"libsql+https://db.example.com/team/db", LibSQLHTTPSScheme, "db.example.com", "/team/db", false, "https://db.example.com/team/db"},
		{"libsql://db.example.com/", LibSQLScheme, "db.example.com", "", false, "https://db.example.com"},
		{"libsql+http://127.0.0.1:8080", LibSQLHTTPScheme, "127.0.0.1:8080", "", true, "http://127.0.0.1:8080"},
		{"libsql+http://localhost:8080/", LibSQLHTTPScheme, "localhost:8080", "", true, "http://localhost:8080"},
		{"libsql+http://[::1]:8080/base", LibSQLHTTPScheme, "[::1]:8080", "/base", true, "http://[::1]:8080/base"},
		{"LIBSQL://db.example.com", LibSQLScheme, "db.example.com", "", false, "https://db.example.com"},
	}
	for _, tc := range accepted {
		t.Run(tc.source, func(t *testing.T) {
			ref, err := Parse(tc.source)
			if err != nil {
				t.Fatal(err)
			}
			if ref.Scheme != tc.scheme || ref.LibSQLHost != tc.host || ref.LibSQLBasePath != tc.path {
				t.Fatalf("ref = %#v", ref)
			}
			if ref.LibSQLAllowLoopbackHTTP != tc.loopback {
				t.Fatalf("loopback = %v", ref.LibSQLAllowLoopbackHTTP)
			}
			if ref.Path != tc.host+tc.path {
				t.Fatalf("Path = %q", ref.Path)
			}
			if got := ref.libSQLEndpoint(); got != tc.endpoint {
				t.Fatalf("endpoint = %q, want %q", got, tc.endpoint)
			}
			if got := ref.LibSQLSourceURL(); got != tc.scheme+"://"+tc.host+tc.path {
				t.Fatalf("LibSQLSourceURL = %q", got)
			}
		})
	}
}

func TestParseLibSQLSourceRefusals(t *testing.T) {
	t.Parallel()
	secret := "s3cr3t-libSQL"
	for _, tc := range []struct {
		source string
		want   string
	}{
		{"libsql://user:" + secret + "@db.example.com", "credentials are not supported"},
		{"libsql://token@db.example.com", "credentials are not supported"},
		{"libsql://db.example.com#" + secret, "takes no query or fragment"},
		{"libsql://db.example.com?authToken=" + secret, "takes no query or fragment"},
		{"libsql://db.example.com?", "takes no query or fragment"},
		{"libsql://db.example.com/base/", "takes no trailing slash"},
		{"libsql://db.example.com/a//b", "plain segments"},
		{"libsql://db.example.com/../db", "plain segments"},
		{"libsql://db.example.com/base%2Fx", "must not be percent-encoded"},
		{`libsql://db.example.com/a\b`, "takes no backslash"},
		{"libsql://db.example.com/v3/pipeline", "not an API endpoint"},
		{"libsql://db.example.com/v2", "not an API endpoint"},
		{"libsql://db.example.com/health", "not an API endpoint"},
		{"libsql+http://db.example.com", "loopback endpoint"},
		{"libsql://db.example.com:bad-port", "expected libsql://HOST"},
		{"libsql://:8080", "names no usable host"},
	} {
		_, err := Parse(tc.source)
		if err == nil {
			t.Fatalf("%s was accepted", tc.source)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: error = %q, want %q", tc.source, err, tc.want)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("%s leaked the secret: %q", tc.source, err)
		}
	}
}

// TestLibSQLDoesNotReinterpretHTTPS pins the boundary this consumer must not
// cross: an ordinary https:// source keeps its existing meaning (a local DataTug
// project directory), so a libSQL server is only reachable through a libsql://
// form even though both end up as HTTPS on the wire.
func TestLibSQLDoesNotReinterpretHTTPS(t *testing.T) {
	t.Parallel()
	ref, err := Parse("https://db.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Scheme != "https" || ref.LibSQLHost != "" {
		t.Fatalf("https:// was reinterpreted as libSQL: %#v", ref)
	}
	if ref.Path != "db.example.com" {
		t.Fatalf("https source path = %q", ref.Path)
	}
	if got := ref.LibSQLSourceURL(); got != "" {
		t.Fatalf("a non-libSQL ref has a libSQL source URL: %q", got)
	}
	if got := (BackendRef{Scheme: LibSQLScheme}).LibSQLSourceURL(); got != "" {
		t.Fatalf("a libSQL ref without a host has a source URL: %q", got)
	}
}

func TestLibSQLHeadersReadTheEnvironmentForEveryRequest(t *testing.T) {
	t.Parallel()
	// No token: an anonymous request whose policy the server decides.
	headers, err := libSQLHeaders(func(string) (string, bool) { return "", false })
	if err != nil || headers != nil {
		t.Fatalf("headers = %v, err = %v", headers, err)
	}
	// A variable set to an empty string behaves as unset.
	headers, err = libSQLHeaders(func(string) (string, bool) { return "", true })
	if err != nil || headers != nil {
		t.Fatalf("empty token: headers = %v, err = %v", headers, err)
	}

	token := "token-one"
	lookup := func(name string) (string, bool) {
		if name == LibSQLBearerEnv {
			return token, true
		}
		return "", false
	}
	first, err := libSQLHeaders(lookup)
	if err != nil || first["Authorization"] != "Bearer token-one" {
		t.Fatalf("first = %v, err = %v", first, err)
	}
	// Rotation: the next call sees the new value, so nothing is captured at open.
	token = "token-two"
	second, err := libSQLHeaders(lookup)
	if err != nil || second["Authorization"] != "Bearer token-two" {
		t.Fatalf("second = %v, err = %v", second, err)
	}
}

func TestLibSQLHeadersRefuseAnUnsafeValueWithoutEchoingIt(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"token\r\nX-Injected: 1", "token\nnext", "token\x00tail"} {
		_, err := libSQLHeaders(func(string) (string, bool) { return value, true })
		if err == nil {
			t.Fatalf("%q was accepted as a header value", value)
		}
		if !IsLibSQLUnsafeCredential(err) {
			t.Fatalf("error is not the typed refusal: %v", err)
		}
		if strings.Contains(err.Error(), value) || strings.Contains(err.Error(), "token") {
			t.Fatalf("the refusal echoed the value: %q", err)
		}
		if !strings.Contains(err.Error(), LibSQLBearerEnv) {
			t.Fatalf("the refusal does not name the variable: %q", err)
		}
	}
}

func TestOpenLibSQLRefusesAWriteBeforeAnyIO(t *testing.T) {
	t.Parallel()
	ref, err := Parse("libsql://db.example.com")
	if err != nil {
		t.Fatal(err)
	}
	called := false
	original := newLibSQLDatabase
	newLibSQLDatabase = func(context.Context, dalgo2libsql.Config) (dal.DB, error) {
		called = true
		return nil, errors.New("must not be called")
	}
	t.Cleanup(func() { newLibSQLDatabase = original })

	_, err = openLibSQL(context.Background(), ref, openMode{forWrite: true})
	if err == nil {
		t.Fatal("a write open was accepted")
	}
	if called {
		t.Fatal("a write open reached the adapter constructor")
	}
	if !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("error = %q", err)
	}
}

func TestOpenLibSQLPassesTheValidatedBaseAddress(t *testing.T) {
	// The token is read from the process environment at open, as in production.
	t.Setenv(LibSQLBearerEnv, "open-token")
	ref, err := Parse("libsql+http://127.0.0.1:8080/team/db")
	if err != nil {
		t.Fatal(err)
	}
	var captured dalgo2libsql.Config
	original := newLibSQLDatabase
	newLibSQLDatabase = func(_ context.Context, cfg dalgo2libsql.Config) (dal.DB, error) {
		captured = cfg
		return nil, errors.New("stub open")
	}
	t.Cleanup(func() { newLibSQLDatabase = original })

	if _, err = openLibSQL(context.Background(), ref, openMode{}); err == nil {
		t.Fatal("the stub constructor's error was dropped")
	}
	if captured.URL != "http://127.0.0.1:8080/team/db" {
		t.Fatalf("URL = %q", captured.URL)
	}
	if !captured.AllowLoopbackHTTP {
		t.Fatal("the loopback permission was not passed")
	}
	if captured.Recordsets != nil {
		t.Fatal("a plain query must not declare a key map")
	}
	if captured.Headers == nil {
		t.Fatal("no header provider was passed")
	}
	headers, err := captured.Headers(context.Background())
	if err != nil || headers["Authorization"] != "Bearer open-token" {
		t.Fatalf("headers = %v, err = %v", headers, err)
	}
}

func TestOpenLibSQLFailsAnUnsafeTokenBeforeTheConstructor(t *testing.T) {
	t.Setenv(LibSQLBearerEnv, "token\r\nX-Injected: 1")
	ref, err := Parse("libsql://db.example.com")
	if err != nil {
		t.Fatal(err)
	}
	called := false
	original := newLibSQLDatabase
	newLibSQLDatabase = func(context.Context, dalgo2libsql.Config) (dal.DB, error) {
		called = true
		return nil, nil
	}
	t.Cleanup(func() { newLibSQLDatabase = original })

	_, err = openLibSQL(context.Background(), ref, openMode{})
	if err == nil || !IsLibSQLUnsafeCredential(err) {
		t.Fatalf("error = %v", err)
	}
	if called {
		t.Fatal("an unsafe token reached the adapter constructor")
	}
	if strings.Contains(err.Error(), "X-Injected") {
		t.Fatalf("the refusal echoed the token: %q", err)
	}
}

func TestOpenLibSQLRejectsAnIncompleteRef(t *testing.T) {
	t.Parallel()
	for _, ref := range []BackendRef{
		{Scheme: LibSQLScheme},
		{Scheme: "postgres", LibSQLHost: "db.example.com"},
	} {
		if _, err := openLibSQL(context.Background(), ref, openMode{}); !errors.Is(err, errUnsupportedBackend) {
			t.Fatalf("%#v: error = %v", ref, err)
		}
	}
}

func TestLibSQLFailureReason(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  error
		want string
	}{
		{dalgo2libsql.ErrUnsafeTarget, "not a safe base URL"},
		{dalgo2libsql.ErrInvalidConfig, "configuration is invalid"},
		{dalgo2libsql.ErrNotSupported, "no native SQL, joins, cursors or transactions"},
		{dalgo2libsql.ErrUnsupportedResponse, "does not recognise"},
		{dalgo2libsql.ErrBackend, LibSQLBearerEnv},
	}
	for _, tc := range cases {
		reason := libSQLFailureReason(tc.err)
		if !strings.Contains(reason, tc.want) {
			t.Fatalf("%v: reason = %q, want %q", tc.err, reason, tc.want)
		}
		if strings.Contains(reason, "db.example.com") {
			t.Fatalf("%v: the reason names an endpoint: %q", tc.err, reason)
		}
	}
	reason := libSQLFailureReason(context.Canceled)
	if !strings.Contains(reason, "cancel") {
		t.Fatalf("cancel reason = %q", reason)
	}
	if got := LibSQLFailureReason(fmt.Errorf("outer: %w", context.DeadlineExceeded)); !strings.Contains(got, "deadline") {
		t.Fatalf("deadline reason = %q", got)
	}

	// An outer wrapper is not the adapter's own boundary: it can add arbitrary
	// text, including a credential or SQL. The exported reason keeps the fixed
	// classification and drops that text.
	const secret = "s3cret-bearer-value"
	wrapped := errors.Join(dalgo2libsql.ErrBackend, errors.New("Authorization: Bearer "+secret+" select * from items"))
	reason = LibSQLFailureReason(wrapped)
	if strings.Contains(reason, secret) || strings.Contains(reason, "select *") {
		t.Fatalf("a wrapped secret or SQL reached the reason: %q", reason)
	}
	if !strings.Contains(reason, LibSQLBearerEnv) {
		t.Fatalf("wrapped backend reason = %q", reason)
	}

	// A wrapped unsafe-credential refusal must show the typed error's fixed
	// sentence, not the wrapper's: only the wrapper can quote the value.
	const unsafeValue = "token\r\nX-Injected: " + secret
	unsafe := CheckLibSQLCredentials(func(string) (string, bool) { return unsafeValue, true })
	if unsafe == nil {
		t.Fatal("an unsafe value was accepted")
	}
	reason = LibSQLFailureReason(fmt.Errorf("outer carrying %s: %w", unsafeValue, unsafe))
	if strings.Contains(reason, secret) || strings.Contains(reason, unsafeValue) || strings.Contains(reason, "outer carrying") {
		t.Fatalf("a wrapped credential value or wrapper text reached the reason: %q", reason)
	}
	if !strings.Contains(reason, LibSQLBearerEnv) || !strings.Contains(reason, "cannot be sent in an HTTP header") {
		t.Fatalf("wrapped unsafe-credential reason = %q", reason)
	}

	if got := LibSQLFailureReason(fmt.Errorf("outer: %w", context.Canceled)); !strings.Contains(got, "cancelled") {
		t.Fatalf("wrapped cancel reason = %q", got)
	}
	if got := LibSQLFailureReason(nil); got != "" {
		t.Fatalf("nil reason = %q", got)
	}
}

func TestOpenFailureNamesALibSQLSourceWithoutItsToken(t *testing.T) {
	t.Parallel()
	ref, err := Parse("libsql://db.example.com")
	if err != nil {
		t.Fatal(err)
	}
	failure := ref.OpenFailure(libSQLUnsafeCredential(LibSQLBearerEnv))
	if failure == nil {
		t.Fatal("no failure")
	}
	if !strings.Contains(failure.Error(), "libsql") || !strings.Contains(failure.Error(), LibSQLBearerEnv) {
		t.Fatalf("failure = %q", failure.Error())
	}
}

func TestDecodeLibSQLDescriptor(t *testing.T) {
	t.Parallel()
	descriptor, err := DecodeLibSQLDescriptor([]byte(`{"dsnEnv":"DATATUG_LIBSQL_DB"}`))
	if err != nil || descriptor.DSNEnv != "DATATUG_LIBSQL_DB" {
		t.Fatalf("descriptor = %#v, err = %v", descriptor, err)
	}
	for _, body := range []string{
		`{"dsnEnv":"DATATUG_LIBSQL_DB","url":"libsql://db.example.com"}`,
		`{"dsnEnv":"DATATUG_LIBSQL_DB","dsnEnv":"DATATUG_LIBSQL_OTHER"}`,
		`{"dsnEnv":"libsql://db.example.com"}`,
		`{"dsnEnv":""}`,
		`{}`,
		`[]`,
	} {
		if _, err := DecodeLibSQLDescriptor([]byte(body)); err == nil {
			t.Fatalf("%s was accepted", body)
		} else if !strings.Contains(err.Error(), "libSQL") {
			t.Fatalf("%s: error does not name libSQL: %q", body, err)
		}
	}
}

// TestLibSQLAdapterRefusesUnsupportedOperations pins the adapter's own contract
// at the boundary this CLI relies on: NewDB performs no I/O, so a text query, a
// join and a write transaction are all refused before the fixture would be
// reached. There is no SELECT-prefix pseudo-parser here and no local fallback in
// this package.
// TestOpenSchemaScanLibSQLUsesTheBorrowedSchemaReader covers the scan route's
// libSQL branch without a server: the constructor seam returns a schema reader,
// which OpenSchemaScan must expose and close through SourceCloser ownership.
func TestOpenSchemaScanLibSQLUsesTheBorrowedSchemaReader(t *testing.T) {
	t.Setenv(LibSQLBearerEnv, "")
	ref, err := Parse("libsql://db.example.com")
	if err != nil {
		t.Fatal(err)
	}
	fake := &postgRESTScanFakeDB{}
	original := newLibSQLDatabase
	newLibSQLDatabase = func(context.Context, dalgo2libsql.Config) (dal.DB, error) {
		return fake, nil
	}
	t.Cleanup(func() { newLibSQLDatabase = original })

	scanDB, err := ref.OpenSchemaScan(context.Background())
	if err != nil {
		t.Fatalf("OpenSchemaScan: %v", err)
	}
	if err = scanDB.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if fake.closed != 1 {
		t.Fatalf("the schema reader was closed %d times, want 1", fake.closed)
	}
}

// TestOpenSchemaScanLibSQLReturnsTheOpenFailure keeps a refused libSQL scan an
// error rather than a half-open schema reader, and it names no credential.
func TestOpenSchemaScanLibSQLReturnsTheOpenFailure(t *testing.T) {
	const secret = "unsafe-token-value"
	t.Setenv(LibSQLBearerEnv, "token\r\nX-Injected: "+secret)
	ref, err := Parse("libsql://db.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ref.OpenSchemaScan(context.Background()); !IsLibSQLUnsafeCredential(err) {
		t.Fatalf("OpenSchemaScan error = %v, want the unsafe-credential refusal", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the refusal echoed the credential: %v", err)
	}
}

func TestLibSQLAdapterRefusesUnsupportedOperations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := dalgo2libsql.NewDB(ctx, dalgo2libsql.Config{
		URL:               "http://127.0.0.1:1",
		AllowLoopbackHTTP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closer, ok := dal.As[io.Closer](db); ok {
			_ = closer.Close()
		}
	})

	if _, err = db.ExecuteQueryToRecordsReader(ctx, dal.NewTextQuery("select 1", nil)); !errors.Is(err, dalgo2libsql.ErrNotSupported) {
		t.Fatalf("a text query was not refused as unsupported: %v", err)
	}
	if err = db.RunReadwriteTransaction(ctx, func(context.Context, dal.ReadwriteTransaction) error { return nil }); !errors.Is(err, dalgo2libsql.ErrNotSupported) {
		t.Fatalf("a write transaction was not refused as unsupported: %v", err)
	}
	if err = db.RunReadonlyTransaction(ctx, func(context.Context, dal.ReadTransaction) error { return nil }); !errors.Is(err, dalgo2libsql.ErrNotSupported) {
		t.Fatalf("a read-only transaction was not refused as unsupported: %v", err)
	}
}
