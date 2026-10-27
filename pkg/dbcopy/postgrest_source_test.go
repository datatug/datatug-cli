package dbcopy

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo2postgrest"
	"github.com/dal-go/record"
)

func TestParsePostgRESTSource(t *testing.T) {
	t.Parallel()
	accepted := []struct {
		source   string
		scheme   string
		host     string
		path     string
		schema   string
		idColumn string
		loopback bool
	}{
		{"postgrest://api.example.com/rest/v1", PostgRESTScheme, "api.example.com", "/rest/v1", "", "", false},
		{"postgrest+https://api.example.com/rest/v1", PostgRESTHTTPSScheme, "api.example.com", "/rest/v1", "", "", false},
		{"postgrest+https://api.example.com", PostgRESTHTTPSScheme, "api.example.com", "", "", "", false},
		{"postgrest+http://127.0.0.1:3000", PostgRESTHTTPScheme, "127.0.0.1:3000", "", "", "", true},
		{"postgrest+http://127.0.0.1:3000/", PostgRESTHTTPScheme, "127.0.0.1:3000", "", "", "", true},
		{"postgrest+http://localhost:3000/rest/v1", PostgRESTHTTPScheme, "localhost:3000", "/rest/v1", "", "", true},
		{"postgrest+http://[::1]:3000/rest/v1", PostgRESTHTTPScheme, "[::1]:3000", "/rest/v1", "", "", true},
		{"POSTGREST://api.example.com/rest/v1", PostgRESTScheme, "api.example.com", "/rest/v1", "", "", false},
		{"postgrest://api.example.com/rest/v1?schema=app&id=note_id", PostgRESTScheme, "api.example.com", "/rest/v1", "app", "note_id", false},
	}
	for _, tc := range accepted {
		t.Run(tc.source, func(t *testing.T) {
			ref, err := Parse(tc.source)
			if err != nil {
				t.Fatal(err)
			}
			if ref.Scheme != tc.scheme || ref.PostgRESTHost != tc.host || ref.PostgRESTAPIPath != tc.path {
				t.Fatalf("ref = %#v", ref)
			}
			if ref.PostgRESTSchema != tc.schema || ref.PostgRESTIDColumn != tc.idColumn || ref.PostgRESTAllowLoopbackHTTP != tc.loopback {
				t.Fatalf("options/loopback = %#v", ref)
			}
			if ref.Path != tc.host+tc.path {
				t.Fatalf("Path = %q", ref.Path)
			}
			if got := ref.PostgRESTSourceURL(); !strings.HasPrefix(got, tc.scheme+"://"+tc.host) {
				t.Fatalf("PostgRESTSourceURL = %q", got)
			}
			if shown := ref.Display(); strings.Contains(shown, "@") || strings.Contains(shown, "schema=") || !strings.HasPrefix(shown, tc.scheme+"://") {
				t.Fatalf("display = %q", shown)
			}
		})
	}
}

func TestParsePostgRESTSourceRefusals(t *testing.T) {
	t.Parallel()
	secret := "s3cr3t-PostgREST"
	for _, tc := range []struct {
		source string
		want   string
	}{
		{"postgrest://user:" + secret + "@api.example.com/rest/v1", "credentials are not supported"},
		{"postgrest://token@api.example.com/rest/v1", "credentials are not supported"},
		{"postgrest://api.example.com/rest/v1#" + secret, "takes no fragment"},
		{"postgrest://api.example.com/rest/v1?apikey=" + secret, "unknown option"},
		{"postgrest://api.example.com/rest/v1?schema=app&schema=other", "given more than once"},
		{"postgrest://api.example.com/rest/v1?schema=", "schema must be a simple SQL identifier"},
		{"postgrest://api.example.com/rest/v1?schema=app%3Bdrop", "schema must be a simple SQL identifier"},
		{"postgrest://api.example.com/rest/v1?id=1bad", "id must be a simple SQL identifier"},
		{"postgrest://api.example.com/rest/v1?schema=" + strings.Repeat("a", 64), "schema must be a simple SQL identifier"},
		{"postgrest://api.example.com/rest/v1?", "takes no empty query"},
		{"postgrest://api.example.com/rest/v1/", "takes no trailing slash"},
		{"postgrest://api.example.com/rest/../v1", "plain segments"},
		{"postgrest://:3000/rest/v1", "names no usable host"},
		{"postgrest://api.example.com/rest\\v1", "takes no backslash"},
		{"postgrest://api.example.com/rest/v1?schema=%zz", "URL options cannot be read"},
		{"postgrest://api.example.com/rest/v1?bad%20name=x", "unknown option"},
		{"postgrest://api.example.com/rest/%76", "must not be percent-encoded"},
		{"postgrest://api.example.com/re%2Fst", "must not be percent-encoded"},
		{"postgrest+http://api.example.com/rest/v1", "allowed only for a loopback endpoint"},
		{"postgrest+http://10.0.0.5/rest/v1", "allowed only for a loopback endpoint"},
		{"postgrest:///rest/v1", "expected postgrest://HOST/API-PATH"},
		{"postgrest://api.example.com:notaport/rest/v1", "invalid PostgREST URL"},
	} {
		_, err := Parse(tc.source)
		if err == nil {
			t.Errorf("%s was accepted", tc.source)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q lacks %q", tc.source, err, tc.want)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: error leaks the secret: %q", tc.source, err)
		}
	}
}

func TestPostgRESTHeadersFromEnvironment(t *testing.T) {
	t.Parallel()
	env := func(values map[string]string) func(string) (string, bool) {
		return func(name string) (string, bool) {
			value, ok := values[name]
			return value, ok
		}
	}
	for _, tc := range []struct {
		name    string
		values  map[string]string
		want    map[string]string
		wantErr bool
	}{
		{"no credentials", nil, nil, false},
		{"bearer", map[string]string{PostgRESTBearerEnv: "tok-" + "only"}, map[string]string{"Authorization": "Bearer tok-only"}, false},
		{"api key", map[string]string{PostgRESTAPIKeyEnv: "key-" + "only"}, map[string]string{"apikey": "key-only"}, false},
		{"both", map[string]string{PostgRESTBearerEnv: "tok", PostgRESTAPIKeyEnv: "key"}, map[string]string{"Authorization": "Bearer tok", "apikey": "key"}, false},
		{"empty variables are unset", map[string]string{PostgRESTBearerEnv: "", PostgRESTAPIKeyEnv: ""}, nil, false},
		{"bearer control character", map[string]string{PostgRESTBearerEnv: "tok\r\nInjected: yes"}, nil, true},
		{"api key control character", map[string]string{PostgRESTAPIKeyEnv: "key\x00"}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := postgRESTHeaders(env(tc.values))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("headers = %#v, want a refusal", got)
				}
				if !IsPostgRESTUnsafeCredential(err) {
					t.Fatalf("error = %v, want the unsafe-credential error", err)
				}
				for _, value := range tc.values {
					if value != "" && strings.Contains(err.Error(), value) {
						t.Fatalf("the error shows the value: %q", err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("headers = %#v, want %#v", got, tc.want)
			}
			for name, want := range tc.want {
				if got.Get(name) != want {
					t.Fatalf("headers = %#v, want %#v", got, tc.want)
				}
			}
		})
	}
}

func TestPostgRESTHeadersReadTheEnvironmentPerRequest(t *testing.T) {
	// Not parallel: it reads and writes the process environment.
	unsetPostgRESTEnv(t)
	ref, err := Parse("postgrest+http://127.0.0.1:3000")
	if err != nil {
		t.Fatal(err)
	}
	original := newPostgRESTDatabase
	t.Cleanup(func() { newPostgRESTDatabase = original })
	var cfg dalgo2postgrest.Config
	newPostgRESTDatabase = func(_ context.Context, got dalgo2postgrest.Config) (dal.DB, error) {
		cfg = got
		return nil, nil
	}
	t.Setenv(PostgRESTBearerEnv, "first-token")
	if _, err = ref.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	first, err := cfg.Headers(context.Background())
	if err != nil || first.Get("Authorization") != "Bearer first-token" {
		t.Fatalf("first headers = %#v, %v", first, err)
	}
	t.Setenv(PostgRESTBearerEnv, "second-token")
	second, err := cfg.Headers(context.Background())
	if err != nil || second.Get("Authorization") == first.Get("Authorization") {
		t.Fatalf("the header did not follow the environment: %#v %#v %v", first, second, err)
	}
	t.Setenv(PostgRESTAPIKeyEnv, "project-key")
	both, err := cfg.Headers(context.Background())
	if err != nil || both.Get("Authorization") != "Bearer second-token" || both.Get("apikey") != "project-key" {
		t.Fatalf("both headers = %#v, %v", both, err)
	}
	t.Setenv(PostgRESTAPIKeyEnv, "bad\nkey")
	if _, err = cfg.Headers(context.Background()); !IsPostgRESTUnsafeCredential(err) {
		t.Fatalf("an unsafe api key was accepted: %v", err)
	}
}

func TestPostgRESTOpenConfigurationAndReadOnly(t *testing.T) {
	unsetPostgRESTEnv(t)
	ref, err := Parse("postgrest+http://127.0.0.1:3000/rest/v1?schema=app&id=note_id")
	if err != nil {
		t.Fatal(err)
	}
	original := newPostgRESTDatabase
	t.Cleanup(func() { newPostgRESTDatabase = original })
	var cfg dalgo2postgrest.Config
	opened := 0
	newPostgRESTDatabase = func(_ context.Context, got dalgo2postgrest.Config) (dal.DB, error) {
		opened++
		cfg = got
		return nil, nil
	}
	if _, err = ref.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	if opened != 1 || cfg.Endpoint != "http://127.0.0.1:3000/rest/v1" || cfg.IDColumn != "note_id" || cfg.Schema != "app" || !cfg.AllowInsecureLoopback {
		t.Fatalf("opened %d with %#v", opened, cfg)
	}
	if cfg.Headers == nil {
		t.Fatal("Headers must be set so credentials are read for every request")
	}
	if _, err = ref.OpenProtected(context.Background()); err != nil || opened != 2 {
		t.Fatalf("OpenProtected: opened %d, %v", opened, err)
	}
	if _, err = ref.OpenForWrite(context.Background()); err == nil || opened != 2 {
		t.Fatalf("OpenForWrite must be refused before any driver call: opened %d, err %v", opened, err)
	} else if !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("OpenForWrite error = %q", err)
	}
}

func TestPostgRESTOpenRefusesAnUnsafeCredential(t *testing.T) {
	unsetPostgRESTEnv(t)
	t.Setenv(PostgRESTBearerEnv, "token\r\nX-Injected: 1")
	ref, err := Parse("postgrest+http://127.0.0.1:3000/rest/v1")
	if err != nil {
		t.Fatal(err)
	}
	original := newPostgRESTDatabase
	t.Cleanup(func() { newPostgRESTDatabase = original })
	newPostgRESTDatabase = func(context.Context, dalgo2postgrest.Config) (dal.DB, error) {
		t.Fatal("the driver must not be called with an unsafe credential")
		return nil, nil
	}
	_, err = ref.Open(context.Background())
	if !IsPostgRESTUnsafeCredential(err) {
		t.Fatalf("error = %v, want the unsafe-credential error", err)
	}
	if strings.Contains(err.Error(), "token") || strings.Contains(err.Error(), "Injected") {
		t.Fatalf("the error shows a credential: %q", err)
	}
	if !strings.Contains(err.Error(), PostgRESTBearerEnv) {
		t.Fatalf("the error should name the variable: %q", err)
	}
}

func TestPostgRESTOpenDoesNotDial(t *testing.T) {
	unsetPostgRESTEnv(t)
	// The adapter validates configuration without network I/O: opening a loopback
	// address nothing listens on succeeds, and Close releases the idle transport.
	ref, err := Parse("postgrest+http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	db, err := ref.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	CloseSource(db)
}

func TestOpenSchemaScanPostgRESTUsesTheGenericReader(t *testing.T) {
	unsetPostgRESTEnv(t)
	ref, err := Parse("postgrest+http://127.0.0.1:3000/rest/v1")
	if err != nil {
		t.Fatal(err)
	}
	original := newPostgRESTDatabase
	t.Cleanup(func() { newPostgRESTDatabase = original })
	fake := &postgRESTScanFakeDB{}
	newPostgRESTDatabase = func(context.Context, dalgo2postgrest.Config) (dal.DB, error) { return fake, nil }
	scan, err := ref.OpenSchemaScan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	collections, err := scan.ListCollections(context.Background(), nil)
	if err != nil || len(collections) != 1 || collections[0].Name() != "items" {
		t.Fatalf("collections = %#v, %v", collections, err)
	}
	if err = scan.Close(); err != nil {
		t.Fatal(err)
	}
	if fake.closed != 1 {
		t.Fatalf("the scan handle was closed %d times, want 1", fake.closed)
	}
}

func TestOpenSchemaScanPostgRESTRefusesAnAdapterWithoutAReader(t *testing.T) {
	unsetPostgRESTEnv(t)
	ref, err := Parse("postgrest+http://127.0.0.1:3000/rest/v1")
	if err != nil {
		t.Fatal(err)
	}
	original := newPostgRESTDatabase
	t.Cleanup(func() { newPostgRESTDatabase = original })
	fake := &postgRESTCloseOnlyDB{}
	newPostgRESTDatabase = func(context.Context, dalgo2postgrest.Config) (dal.DB, error) { return fake, nil }
	if _, err = ref.OpenSchemaScan(context.Background()); err == nil || !strings.Contains(err.Error(), "PostgREST") {
		t.Fatalf("OpenSchemaScan = %v, want a refusal naming PostgREST", err)
	}
	if fake.closed != 1 {
		t.Fatalf("a refused scan left its handle open: closed %d times", fake.closed)
	}
}

func TestPostgRESTOpenFailureTextIsSafe(t *testing.T) {
	t.Parallel()
	token := "tok-" + "secret"
	apiKey := "key-" + "secret"
	ref := BackendRef{Scheme: PostgRESTScheme, Path: "api.example.com/rest/v1", PostgRESTHost: "api.example.com", PostgRESTAPIPath: "/rest/v1", Raw: "postgrest://api.example.com/rest/v1"}
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"unauthorized", &dalgo2postgrest.HTTPError{StatusCode: 401}, "refused the request (HTTP 401)"},
		{"forbidden", &dalgo2postgrest.HTTPError{StatusCode: 403}, "refused the request (HTTP 403)"},
		{"not found", &dalgo2postgrest.HTTPError{StatusCode: 404}, "resource was not found (HTTP 404)"},
		{"unauthorized sentinel", dalgo2postgrest.ErrUnauthorized, "refused the request"},
		{"metadata unauthorized", dalgo2postgrest.ErrMetadataUnauthorized, "OpenAPI metadata document"},
		{"metadata unavailable", dalgo2postgrest.ErrMetadataUnavailable, "usable OpenAPI document"},
		{"relation", dalgo2postgrest.ErrRelationNotFound, "relation was not found"},
		{"config", dalgo2postgrest.ErrInvalidConfig, "configuration is invalid"},
		{"header", dalgo2postgrest.ErrUnsafeHeader, "not safe to send"},
		{"redirect", dalgo2postgrest.ErrRedirectRefused, "redirect"},
		{"budget", dalgo2postgrest.ErrBudgetExceeded, "configured bound"},
		{"response", dalgo2postgrest.ErrResponseTooLarge, "configured bound"},
		{"malformed", dalgo2postgrest.ErrInvalidResponse, "does not recognise"},
		{"request", dalgo2postgrest.ErrRequestFailed, "PostgREST request failed"},
		{"server error", &dalgo2postgrest.HTTPError{StatusCode: 500}, "answered with HTTP 500"},
		{"unknown", errors.New("boom"), "driver could not open the source"},
		{"closed", dalgo2postgrest.ErrClosed, "closed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shown := ref.OpenFailure(tc.err).Error()
			if !strings.Contains(shown, tc.want) {
				t.Fatalf("OpenFailure = %q, want %q", shown, tc.want)
			}
			for _, forbidden := range []string{"Authorization", "apikey", token, apiKey, "password"} {
				if strings.Contains(shown, forbidden) {
					t.Fatalf("OpenFailure shows a credential: %q", shown)
				}
			}
		})
	}
}

// postgRESTScanFakeDB is a generic schema reader for the scan tests: it embeds a
// nil dal.DB for DALgo's sealed interface and implements dbschema.SchemaReader.
type postgRESTScanFakeDB struct {
	dal.DB
	closed int
}

func (f *postgRESTScanFakeDB) Close() error { f.closed++; return nil }

func (*postgRESTScanFakeDB) ListCollections(context.Context, *record.Key) ([]dal.CollectionRef, error) {
	return []dal.CollectionRef{dal.NewRootCollectionRef("items", "")}, nil
}

func (*postgRESTScanFakeDB) DescribeCollection(_ context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	return &dbschema.CollectionDef{Name: ref.Name(), PrimaryKey: []dal.FieldName{"id"}}, nil
}

func (*postgRESTScanFakeDB) ListIndexes(context.Context, *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListIndexes", Backend: "postgrest"}
}

func (*postgRESTScanFakeDB) ListConstraints(context.Context, *dal.CollectionRef) ([]dbschema.ConstraintDef, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListConstraints", Backend: "postgrest"}
}

func (*postgRESTScanFakeDB) ListReferrers(context.Context, *dal.CollectionRef) ([]dbschema.Referrer, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListReferrers", Backend: "postgrest"}
}

var _ SchemaScanDB = (*postgRESTScanFakeDB)(nil)

// postgRESTCloseOnlyDB is an adapter handle with a Close but no schema reader.
type postgRESTCloseOnlyDB struct {
	dal.DB
	closed int
}

func (f *postgRESTCloseOnlyDB) Close() error { f.closed++; return nil }

// unsetPostgRESTEnv clears the two credential variables for the test and puts
// back whatever was there, so a developer's own environment cannot change the
// result.
func unsetPostgRESTEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{PostgRESTBearerEnv, PostgRESTAPIKeyEnv} {
		old, ok := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
		name := name
		t.Cleanup(func() {
			if ok {
				_ = os.Setenv(name, old)
			}
		})
	}
}

func TestPostgRESTSourceURLOfAnotherRefIsEmpty(t *testing.T) {
	t.Parallel()
	for _, ref := range []BackendRef{
		{},
		{Scheme: "postgres", Path: "db.example.com/shop"},
		{Scheme: PostgRESTScheme},
	} {
		if got := ref.PostgRESTSourceURL(); got != "" {
			t.Fatalf("PostgRESTSourceURL(%#v) = %q, want empty", ref, got)
		}
	}
	withOptions := BackendRef{Scheme: PostgRESTScheme, PostgRESTHost: "api.example.com", PostgRESTAPIPath: "/rest/v1", PostgRESTSchema: "app", PostgRESTIDColumn: "note_id"}
	if got := withOptions.PostgRESTSourceURL(); got != "postgrest://api.example.com/rest/v1?id=note_id&schema=app" {
		t.Fatalf("PostgRESTSourceURL = %q", got)
	}
}

func TestPostgRESTOpenRefusesAnUnsupportedRef(t *testing.T) {
	t.Parallel()
	if _, err := (BackendRef{Scheme: PostgRESTScheme}).Open(context.Background()); !errors.Is(err, errUnsupportedBackend) {
		t.Fatalf("Open of a hostless PostgREST ref = %v, want errUnsupportedBackend", err)
	}
}

// postgRESTNoCloseScanDB is a schema reader without a Close, so the scan wrapper
// must report a nil release instead of requiring one.
type postgRESTNoCloseScanDB struct {
	dal.DB
	dbschema.SchemaReader
}

func TestOpenSchemaScanPostgRESTAcceptsAReaderWithoutACloser(t *testing.T) {
	unsetPostgRESTEnv(t)
	ref, err := Parse("postgrest+http://127.0.0.1:3000/rest/v1")
	if err != nil {
		t.Fatal(err)
	}
	original := newPostgRESTDatabase
	t.Cleanup(func() { newPostgRESTDatabase = original })
	newPostgRESTDatabase = func(context.Context, dalgo2postgrest.Config) (dal.DB, error) {
		return &postgRESTNoCloseScanDB{}, nil
	}
	scan, err := ref.OpenSchemaScan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = scan.Close(); err != nil {
		t.Fatalf("Close of a reader without a closer = %v, want nil", err)
	}
}

func TestOpenSchemaScanPostgRESTCredentialFailure(t *testing.T) {
	unsetPostgRESTEnv(t)
	t.Setenv(PostgRESTBearerEnv, "token\r\nInjected: 1")
	ref, err := Parse("postgrest+http://127.0.0.1:3000/rest/v1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = ref.OpenSchemaScan(context.Background())
	if !IsPostgRESTUnsafeCredential(err) {
		t.Fatalf("OpenSchemaScan = %v, want the unsafe-credential error", err)
	}
}
