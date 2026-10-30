package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo2typesense"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// typesenseListingDB is a disposable Typesense handle for the metadata commands:
// one collection with a native array and the adapter's implicit string id.
type typesenseListingDB struct {
	dal.DB
	closed  int
	listErr error
}

func (f *typesenseListingDB) Close() error { f.closed++; return nil }

func (f *typesenseListingDB) ListCollections(context.Context, *record.Key) ([]dal.CollectionRef, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return []dal.CollectionRef{dal.NewRootCollectionRef("books", "")}, nil
}

func (*typesenseListingDB) DescribeCollection(_ context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	if ref.Name() != "books" {
		return nil, fmt.Errorf("collection %q not found", ref.Name())
	}
	return &dbschema.CollectionDef{
		Name: "books",
		Fields: []dbschema.FieldDef{
			{Name: "title", Type: dbschema.String},
			{Name: "tags", Type: dbschema.Null},
			{Name: "id", Type: dbschema.String},
		},
		PrimaryKey: []dal.FieldName{"id"},
		SourceDefinition: &dbschema.SourceDefinition{
			Dialect: "typesense",
			Columns: []dbschema.SourceColumnDef{
				{Name: "title", DeclaredType: "string", NotNull: true},
				{Name: "tags", DeclaredType: "string[]", NotNull: true},
				{Name: "id", DeclaredType: "string", NotNull: true, PrimaryKeyPosition: 1},
			},
		},
	}, nil
}

func (*typesenseListingDB) ListIndexes(context.Context, *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListIndexes", Backend: "typesense"}
}

func (*typesenseListingDB) ListConstraints(context.Context, *dal.CollectionRef) ([]dbschema.ConstraintDef, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListConstraints", Backend: "typesense"}
}

func (*typesenseListingDB) ListReferrers(context.Context, *dal.CollectionRef) ([]dbschema.Referrer, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListReferrers", Backend: "typesense"}
}

var _ dbcopy.SchemaScanDB = (*typesenseListingDB)(nil)

func TestTypesenseDBSchemaListsTheCollectionWithNativeTypes(t *testing.T) {
	fake := &typesenseListingDB{}
	withSchemaSource(t, func(_ context.Context, ref dbcopy.BackendRef) (dal.DB, error) {
		assert.Equal(t, dbcopy.TypesenseHTTPScheme, ref.Scheme)
		return fake, nil
	})
	out, err := runDBSchema(t, "--db", "typesense+http://127.0.0.1:8108/books", "--format", "json", "--columns")
	require.NoError(t, err)
	assert.Equal(t, 1, fake.closed, "the source is released")

	var listing schemaListing
	require.NoError(t, json.Unmarshal([]byte(out), &listing))
	assert.Equal(t, "typesense+http", listing.Provider)
	assert.Equal(t, "typesense+http://127.0.0.1:8108/books", listing.Target)
	require.Len(t, listing.Tables, 1)
	table := listing.Tables[0]
	assert.Equal(t, api.ServerDefaultSchema, table.Schema, "the collection is recorded under the logical grouping")
	assert.Equal(t, "books", table.Name)
	assert.Equal(t, "table", table.Kind, "Typesense has no views")
	require.NotNil(t, table.Columns)
	types := map[string]schemaColumn{}
	for _, column := range *table.Columns {
		types[column.Name] = column
	}
	assert.Equal(t, "string", types["id"].Type, "the implicit id keeps its string type")
	assert.Equal(t, 1, types["id"].PrimaryKeyPosition)
	assert.Equal(t, "string[]", types["tags"].Type, "a native array keeps its declared type")
}

func TestDBSchemaTypesenseSchemesAreOneProvider(t *testing.T) {
	for _, scheme := range []string{"typesense", "typesense+https", "typesense+http"} {
		assert.True(t, isSchemaProvider(scheme), scheme)
	}
	assert.False(t, isSchemaProvider("postgrest"))
}

func TestTypesenseDBSchemaReadFailureIsClassified(t *testing.T) {
	fake := &typesenseListingDB{listErr: errors.New("server said 127.0.0.1:8108/books is broken")}
	withSchemaSource(t, func(context.Context, dbcopy.BackendRef) (dal.DB, error) { return fake, nil })
	_, err := runDBSchema(t, "--db", "typesense+http://127.0.0.1:8108/books", "--format", "json")
	require.Error(t, err)
	assert.Equal(t, exitCodeDatabase, exitCodeOf(t, err))
	assert.Contains(t, err.Error(), "list collections")
	assert.NotContains(t, err.Error(), dbcopy.TypesenseAPIKeyEnv)
	assert.Equal(t, 1, fake.closed)
}

// TestTypesenseDBSchemaCountsIsRefusedBeforeAnyIO pins the order of the usage
// check: a missing credential would make the open fail, but --counts is refused
// first and nothing is opened or read.
func TestTypesenseDBSchemaCountsIsRefusedBeforeAnyIO(t *testing.T) {
	t.Setenv(dbcopy.TypesenseAPIKeyEnv, "")
	withSchemaSource(t, func(context.Context, dbcopy.BackendRef) (dal.DB, error) {
		t.Fatal("--counts must be refused before the source is opened")
		return nil, nil
	})
	_, err := runDBSchema(t, "--db", "typesense+http://127.0.0.1:8108/books", "--counts")
	require.Error(t, err)
	assert.Equal(t, exitCodeUsage, exitCodeOf(t, err), "the refusal is a usage error, not a credential failure")
	assert.Contains(t, err.Error(), "--counts is not available for a Typesense source")
	assert.NotContains(t, err.Error(), dbcopy.TypesenseAPIKeyEnv)
}

// TestTypesenseDBTableCountsIsRefusedBeforeAnyIO proves the same centralized
// refusal answers `db table` before the source is opened, so a missing
// credential never masks the usage error.
func TestTypesenseDBTableCountsIsRefusedBeforeAnyIO(t *testing.T) {
	t.Setenv(dbcopy.TypesenseAPIKeyEnv, "")
	withSchemaSource(t, func(context.Context, dbcopy.BackendRef) (dal.DB, error) {
		t.Fatal("--counts must be refused before the source is opened")
		return nil, nil
	})
	_, err := runMetadata(t, dbTableCommand(), "books", "--db", "typesense+http://127.0.0.1:8108/books", "--counts")
	require.Error(t, err)
	assert.Equal(t, exitCodeUsage, exitCodeOf(t, err), "the refusal is a usage error, not a credential failure")
	assert.Contains(t, err.Error(), "--counts is not available for a Typesense source")
	assert.NotContains(t, err.Error(), dbcopy.TypesenseAPIKeyEnv)
}

func TestScanTypesenseParametersAreBuiltForTheCommand(t *testing.T) {
	const variable = "DATATUG_TEST_TYPESENSE_SCAN_OK"
	t.Setenv(variable, "typesense+http://127.0.0.1:8108/books")
	t.Setenv(dbcopy.DescriptorEnvAllowList, "")

	v := &scanDbCommand{Driver: api.DriverTypesense, DSNEnv: variable, Environment: "local", Database: "books"}
	params, err := v.descriptorConnectionParams()
	require.NoError(t, err)
	assert.Equal(t, api.DriverTypesense, params.Driver())
	assert.Equal(t, "env:"+variable, params.ConnectionString())
	assert.Equal(t, "127.0.0.1", params.Server())
	assert.Equal(t, 8108, params.Port())
	assert.NotContains(t, params.String(), "127.0.0.1", "the parameter text is the env:NAME source")
}

func TestScanTypesenseRefusesAParamsErrorBeforeAnyIO(t *testing.T) {
	const variable = "DATATUG_TEST_TYPESENSE_SCAN_BAD"
	t.Setenv(variable, "postgres://db.example.com/shop")
	t.Setenv(dbcopy.DescriptorEnvAllowList, "")

	projectDir := filepath.Join(t.TempDir(), "typesense-bad")
	require.NoError(t, os.Mkdir(projectDir, 0o755))
	stderr, err := runScanCommand(t, "-d", projectDir, "-D", "typesense", "--dsn-env", variable, "--db", "books", "--env", "local")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must hold a typesense")
	assert.NotContains(t, err.Error(), "db.example.com")
	assert.NotContains(t, stderr, "db.example.com")
	assert.NoFileExists(t, filepath.Join(projectDir, "connections", "local", "books.json"))
}

// TestQueryTypesenseDifferentCollectionIsRefusedByTheAdapterBeforeAnyRequest
// proves the pinned adapter's own configured-collection check with the real
// module: the counting endpoint receives zero requests, so this is not a second
// CLI-side rule.
func TestQueryTypesenseDifferentCollectionIsRefusedByTheAdapterBeforeAnyRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()
	t.Setenv(dbcopy.TypesenseAPIKeyEnv, "scoped-key")

	stdout, stderr, code := runTypesenseNative(t, `{"collection":"other","limit":1}`, "--db", "typesense+http://"+server.Listener.Addr().String()+"/books", "--request", "-")
	if code == 0 {
		t.Fatalf("a different collection was accepted: %s", stdout)
	}
	if !strings.Contains(stderr, "Typesense native search failed") {
		t.Fatalf("stderr = %q", stderr)
	}
	if requests != 0 {
		t.Fatalf("the adapter sent %d requests before refusing the collection", requests)
	}
}

func TestQueryFailureForTypesenseShowsOnlyAFixedClassification(t *testing.T) {
	t.Parallel()
	backend := dbcopy.BackendRef{Scheme: dbcopy.TypesenseScheme}
	const secret = "s3cret-typesense-key"

	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "wrapped permission failure",
			err: fmt.Errorf("wrapper for typesense://search.example.com/books: %w",
				errors.Join(dalgo2typesense.ErrPermissionDenied, errors.New("X-TYPESENSE-API-KEY: "+secret))),
			want: "refused the credential",
		},
		{
			name: "unsupported native SQL",
			err:  fmt.Errorf("outer: %w", dal.ErrNotSupported),
			want: "does not support this operation",
		},
		{
			name: "cancelled",
			err:  fmt.Errorf("outer with %s: %w", secret, errors.Join(dalgo2typesense.ErrRequestFailed, context.Canceled)),
			want: "cancelled",
		},
		{
			name: "metadata forbidden",
			err:  fmt.Errorf("outer: %w", dalgo2typesense.ErrMetadataForbidden),
			want: "not permitted",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := queryFailureFor(backend, tc.err)
			var coder ExitCoder
			require.ErrorAs(t, err, &coder)
			assert.Equal(t, exitCodeDatabase, coder.ExitCode())
			message := err.Error()
			assert.Contains(t, message, "Typesense read failed")
			assert.Contains(t, message, tc.want)
			for _, leaked := range []string{secret, "select *", "search.example.com", "X-TYPESENSE-API-KEY"} {
				assert.NotContains(t, message, leaked)
			}
		})
	}
}

func TestQueryFailureForTypesenseKeepsPolicyRefusals(t *testing.T) {
	t.Parallel()
	backend := dbcopy.BackendRef{Scheme: dbcopy.TypesenseScheme}
	err := queryFailureFor(backend, fmt.Errorf("saved query: %w", access.ErrAccessDenied))
	var coder ExitCoder
	require.ErrorAs(t, err, &coder)
	assert.Equal(t, exitCodeAccessDenied, coder.ExitCode())
	assert.NotContains(t, err.Error(), "Typesense read failed")
}

func TestQueryDefaultTypesenseLimitIsVisibleWithoutAService(t *testing.T) {
	t.Setenv(dbcopy.TypesenseAPIKeyEnv, "scoped-key")
	_, stderr, code := runQuery(t, "", "--db", "typesense+http://127.0.0.1:1/books", "--from", "books", "--format", "json", "--no-policies")
	if code != exitCodeDatabase {
		t.Fatalf("exit %d, want %d: %s", code, exitCodeDatabase, stderr)
	}
	want := fmt.Sprintf("reading at most %d rows (Typesense exploration default", typesenseSourceDefaultLimit)
	if !strings.Contains(stderr, want) {
		t.Fatalf("stderr must state the default bound: %q, want %q", stderr, want)
	}
}

func TestQueryDefaultTypesenseLimitAcceptsAServiceRef(t *testing.T) {
	// A --limit is not replaced by the exploration default: the explicit value is
	// passed through, even when the source cannot be reached.
	t.Setenv(dbcopy.TypesenseAPIKeyEnv, "scoped-key")
	_, stderr, code := runQuery(t, "", "--db", "typesense+http://127.0.0.1:1/books", "--from", "books", "--limit", "7", "--format", "json", "--no-policies")
	if code != exitCodeDatabase {
		t.Fatalf("exit %d, want %d: %s", code, exitCodeDatabase, stderr)
	}
	assert.NotContains(t, stderr, "Typesense exploration default")
}

type noNativeTypesenseDB struct{ dal.DB }

func (noNativeTypesenseDB) Close() error { return nil }

type failingTypesenseOutput struct{}

func (failingTypesenseOutput) Write([]byte) (int, error) { return 0, errors.New("output failed") }

func TestClassifyTypesenseRequestDecodeErrorHasAFixedFallback(t *testing.T) {
	err := classifyTypesenseRequestDecodeError(errors.New("decoder said LEAK_SECRET_typesense_7f3a91"))
	assert.ErrorIs(t, err, errTypesenseRequestInvalid)
	assert.NotContains(t, err.Error(), "LEAK_SECRET_typesense_7f3a91")
}

func TestQueryTypesenseRequiresItsTwoFlags(t *testing.T) {
	for name, tc := range map[string]struct{ source, request string }{
		"no source":  {"", "-"},
		"bad source": {"::not-a-source", "-"},
		"no request": {"typesense+http://127.0.0.1:8108/books", ""},
	} {
		err := runTypesenseNativeCommand(&cobra.Command{}, tc.source, tc.request)
		require.Error(t, err, name)
		assert.Equal(t, exitCodeUsage, exitCodeOf(t, err), name)
	}
}

func TestReadTypesenseSearchRequestReadsAFileAndRejectsAMissingOneWithoutLeaking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "search.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"limit":2}`), 0o600))
	request, err := readTypesenseSearchRequest(nil, path)
	require.NoError(t, err)
	assert.Equal(t, 2, request.Limit)

	missing := filepath.Join(t.TempDir(), "missing-secret-path.json")
	_, err = readTypesenseSearchRequest(nil, missing)
	require.Error(t, err)
	assert.Contains(t, err.Error(), missing, "the caller's own path is useful and holds no provider credential")
	assert.NotContains(t, err.Error(), dbcopy.TypesenseAPIKeyEnv)
}

func TestReadTypesenseSearchRequestRefusesNilStdin(t *testing.T) {
	_, err := readTypesenseSearchRequest(nil, "-")
	require.Error(t, err)
	assert.ErrorIs(t, err, errTypesenseRequestNoStdin)
}

func TestQueryTypesenseOpenFailureIsClassified(t *testing.T) {
	original := openBackend
	t.Cleanup(func() { openBackend = original })
	openBackend = func(context.Context, dbcopy.BackendRef) (dal.DB, error) {
		return nil, errors.New("dial 127.0.0.1:8108 failed")
	}
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	cmd := queryTypesenseCommand()
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetIn(strings.NewReader(`{"limit":1}`))
	require.NoError(t, cmd.Flags().Set(queryDBFlag, "typesense+http://127.0.0.1:8108/books"))
	require.NoError(t, cmd.Flags().Set("request", "-"))
	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	assert.Equal(t, exitCodeDatabase, exitCodeOf(t, err))
	assert.Contains(t, err.Error(), "could not open the source")
	assert.NotContains(t, err.Error(), dbcopy.TypesenseAPIKeyEnv)
	assert.Empty(t, out.String())
}

func TestQueryTypesenseWithoutNativeCapabilityIsRefused(t *testing.T) {
	original := openBackend
	t.Cleanup(func() { openBackend = original })
	openBackend = func(context.Context, dbcopy.BackendRef) (dal.DB, error) { return noNativeTypesenseDB{}, nil }
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	cmd := queryTypesenseCommand()
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetIn(strings.NewReader(`{"limit":1}`))
	require.NoError(t, cmd.Flags().Set(queryDBFlag, "typesense+http://127.0.0.1:8108/books"))
	require.NoError(t, cmd.Flags().Set("request", "-"))
	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	assert.Equal(t, exitCodeDatabase, exitCodeOf(t, err))
	assert.Contains(t, err.Error(), "no native search capability")
	assert.Empty(t, out.String())
}

func TestQueryTypesenseSearchFailureIsClassified(t *testing.T) {
	original := openBackend
	t.Cleanup(func() { openBackend = original })
	openBackend = func(context.Context, dbcopy.BackendRef) (dal.DB, error) {
		return &fakeTypesenseSearcher{err: fmt.Errorf("wrapper 127.0.0.1: %w", dalgo2typesense.ErrPermissionDenied)}, nil
	}
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	cmd := queryTypesenseCommand()
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetIn(strings.NewReader(`{"limit":1}`))
	require.NoError(t, cmd.Flags().Set(queryDBFlag, "typesense+http://127.0.0.1:8108/books"))
	require.NoError(t, cmd.Flags().Set("request", "-"))
	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	assert.Equal(t, exitCodeDatabase, exitCodeOf(t, err))
	assert.Contains(t, err.Error(), "Typesense native search failed")
	assert.NotContains(t, err.Error(), "127.0.0.1")
	assert.Empty(t, out.String())
}

func TestQueryTypesenseOutputFailureIsReturned(t *testing.T) {
	original := openBackend
	t.Cleanup(func() { openBackend = original })
	openBackend = func(context.Context, dbcopy.BackendRef) (dal.DB, error) {
		return &fakeTypesenseSearcher{result: dalgo2typesense.SearchResult{Collection: "books", Returned: 0}}, nil
	}
	cmd := queryTypesenseCommand()
	cmd.SetOut(failingTypesenseOutput{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetIn(strings.NewReader(`{"limit":1}`))
	require.NoError(t, cmd.Flags().Set(queryDBFlag, "typesense+http://127.0.0.1:8108/books"))
	require.NoError(t, cmd.Flags().Set("request", "-"))
	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "output failed")
}

func TestQueryTypesenseKeepsAndRestoresTheCallerContext(t *testing.T) {
	original := openBackend
	t.Cleanup(func() { openBackend = original })
	openBackend = func(context.Context, dbcopy.BackendRef) (dal.DB, error) {
		return &fakeTypesenseSearcher{result: dalgo2typesense.SearchResult{Collection: "books"}}, nil
	}

	runner := func(t *testing.T, parent context.Context) {
		t.Helper()
		cmd := queryTypesenseCommand()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetIn(strings.NewReader(`{"limit":1}`))
		cmd.SetContext(parent)
		require.NoError(t, cmd.Flags().Set(queryDBFlag, "typesense+http://127.0.0.1:8108/books"))
		require.NoError(t, cmd.Flags().Set("request", "-"))
		require.NoError(t, cmd.RunE(cmd, nil))
		assert.Equal(t, parent, cmd.Context())
	}
	t.Run("non-nil context", func(t *testing.T) {
		runner(t, context.Background())
	})
	t.Run("nil context", func(t *testing.T) {
		runner(t, nil)
	})
}
