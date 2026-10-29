package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo2typesense"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The environment scripts/check-journey-typesense.sh owns. The URL variables
// name loopback sources for an owned, disposable Typesense 30.2 container; the
// key variables hold scoped keys the fixture created. The test never creates the
// container, never uses a production endpoint and never seeds through the
// adapter under test.
const (
	typesenseRealURLEnv          = "DATATUG_TEST_TYPESENSE_URL"
	typesenseRealSemanticsURLEnv = "DATATUG_TEST_TYPESENSE_SEMANTICS_URL"
	typesenseRealSearchKeyEnv    = "DATATUG_TEST_TYPESENSE_SEARCH_KEY"
	typesenseRealMetadataKeyEnv  = "DATATUG_TEST_TYPESENSE_METADATA_KEY"
	typesenseRealRequireEnv      = "DATATUG_TEST_TYPESENSE_REQUIRE_FIXTURE"
)

// typesenseRealFixture is the owned real Typesense server one journey uses.
type typesenseRealFixture struct {
	source          string
	semanticsSource string
	searchKey       string
	metadataKey     string
	archiveSource   string
}

// requireTypesenseRealFixture returns the fixture when the opt-in environment is
// set. A missing fixture is a failure once the journey script asks for it, never
// a silent skip; without the opt-in the default test run stays fast and offline.
func requireTypesenseRealFixture(t *testing.T) typesenseRealFixture {
	t.Helper()
	source := os.Getenv(typesenseRealURLEnv)
	if source == "" {
		if os.Getenv(typesenseRealRequireEnv) == "1" {
			t.Fatalf("%s is required when %s=1 (run scripts/check-journey-typesense.sh)", typesenseRealURLEnv, typesenseRealRequireEnv)
		}
		t.Skip("set " + typesenseRealURLEnv + " and " + typesenseRealSearchKeyEnv + " to an owned disposable Typesense server")
	}
	for name, value := range map[string]string{
		typesenseRealURLEnv:          source,
		typesenseRealSemanticsURLEnv: os.Getenv(typesenseRealSemanticsURLEnv),
	} {
		parsed, err := url.Parse(value)
		if err != nil || !dbcopy.IsTypesenseScheme(parsed.Scheme) || parsed.Host == "" {
			t.Fatalf("%s must be a Typesense source URL, not %q", name, dbcopy.SourceDisplay(value))
		}
	}
	// The books URL names the owned fixture's collection and nothing else, so the
	// archive source is derived from the parsed origin rather than by trimming an
	// unchecked suffix.
	books, err := url.Parse(source)
	if err != nil || books.Path != "/books" {
		t.Fatalf("%s must name the owned /books collection, not %q", typesenseRealURLEnv, dbcopy.SourceDisplay(source))
	}
	fixture := typesenseRealFixture{
		source:          source,
		semanticsSource: os.Getenv(typesenseRealSemanticsURLEnv),
		searchKey:       os.Getenv(typesenseRealSearchKeyEnv),
		metadataKey:     os.Getenv(typesenseRealMetadataKeyEnv),
		archiveSource:   books.Scheme + "://" + books.Host + "/archive",
	}
	if fixture.searchKey == "" || fixture.metadataKey == "" {
		t.Fatalf("%s and %s are required with %s", typesenseRealSearchKeyEnv, typesenseRealMetadataKeyEnv, typesenseRealURLEnv)
	}
	return fixture
}

// runTypesenseNative runs `datatug query typesense` through the query group, so
// the journey exercises the registered command rather than a helper entry point.
func runTypesenseNative(t *testing.T, stdin string, argv ...string) (stdout, stderr string, code int) {
	t.Helper()
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	root := &cobra.Command{Use: "datatug", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(queryCommand())
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{"query", "typesense"}, argv...))
	err := root.ExecuteContext(context.Background())
	if err != nil {
		var coder ExitCoder
		if errors.As(err, &coder) {
			code = coder.ExitCode()
		} else {
			code = 1
		}
		errOut.WriteString(err.Error() + "\n")
	}
	return out.String(), errOut.String(), code
}

// typesenseSearchDocument is the JSON output shape this journey reads back. The
// document fields stay raw so an integer above 2^53 is compared as its exact
// digits, not as a float64.
type typesenseSearchDocument struct {
	Collection string `json:"collection"`
	Found      int    `json:"found"`
	OutOf      int    `json:"outOf"`
	Returned   int    `json:"returned"`
	Partial    bool   `json:"partial"`
	Complete   bool   `json:"complete"`
	Truncated  bool   `json:"truncated"`
	Provenance string `json:"provenance"`
	Hits       []struct {
		ID       string                     `json:"id"`
		Document map[string]json.RawMessage `json:"document"`
	} `json:"hits"`
	Facets []struct {
		Field  string `json:"field"`
		Counts []struct {
			Value string `json:"value"`
			Count int    `json:"count"`
		} `json:"counts"`
		Stats *struct {
			Min json.RawMessage `json:"min"`
			Max json.RawMessage `json:"max"`
		} `json:"stats"`
	} `json:"facets"`
}

func decodeTypesenseSearchDocument(t *testing.T, stdout string) typesenseSearchDocument {
	t.Helper()
	var document typesenseSearchDocument
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("native output is not JSON: %v\n%s", err, stdout)
	}
	return document
}

func TestTypesenseQueryJourney(t *testing.T) {
	fixture := requireTypesenseRealFixture(t)

	t.Run("structured-from-pages-limit-and-offset", func(t *testing.T) {
		t.Setenv(dbcopy.TypesenseAPIKeyEnv, fixture.searchKey)
		stdout, stderr, code := runQuery(t, "", "--db", fixture.source, "--from", "books", "--limit", "3", "--offset", "2", "--format", "json", "--no-policies")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		rows := decodeObjects(t, stdout)
		if len(rows) != 3 {
			t.Fatalf("rows = %d, want 3: %v", len(rows), rows)
		}
		wantKeys := []string{"010", "009", "008"}
		for i, want := range wantKeys {
			if rows[i]["$key"] != want {
				t.Fatalf("row %d key = %v, want %s in native rank order: %v", i, rows[i]["$key"], want, rows)
			}
		}
		if rows[0]["title"] != "Ender's Game" {
			t.Fatalf("first row = %v", rows[0])
		}
	})

	t.Run("native-search-file-keeps-seven-counts-types-and-facets", func(t *testing.T) {
		request := `{"collection":"books","q":"*","filterBy":"genre:=science fiction","sortBy":[{"field":"seq"}],"facetBy":["genre","year"],"maxFacetValues":10,"limit":5}`
		path := filepath.Join(t.TempDir(), "search.json")
		require.NoError(t, os.WriteFile(path, []byte(request), 0o600))

		// A search-only key reads documents but cannot read collection metadata;
		// the native command must still succeed with exactly that key.
		t.Setenv(dbcopy.TypesenseAPIKeyEnv, fixture.searchKey)
		stdout, stderr, code := runTypesenseNative(t, "", "--db", fixture.source, "--request", path)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		assert.Contains(t, stderr, typesenseNativePolicyNote, "the native boundary must be stated")
		assert.Contains(t, stderr, "not applied")

		document := decodeTypesenseSearchDocument(t, stdout)
		if document.Collection != "books" || document.Found != 10 || document.OutOf != 12 || document.Returned != 5 {
			t.Fatalf("native counts = collection %q, found %d, outOf %d, returned %d", document.Collection, document.Found, document.OutOf, document.Returned)
		}
		if document.Partial || document.Complete || !document.Truncated {
			t.Fatalf("truncation flags = partial %v, complete %v, truncated %v; want a truncated but not partial result", document.Partial, document.Complete, document.Truncated)
		}
		if document.Provenance != dalgo2typesense.ProvenanceSearch || len(document.Hits) != 5 {
			t.Fatalf("provenance/hits = %q/%d", document.Provenance, len(document.Hits))
		}
		if document.Hits[0].ID != "001" {
			t.Fatalf("first hit id = %q, want the preserved string id 001", document.Hits[0].ID)
		}
		first := document.Hits[0].Document
		if string(first["big"]) != "9007199254740993" {
			t.Fatalf("big int64 = %s, want the exact digits above 2^53", first["big"])
		}
		if err := json.Unmarshal(first["tags"], &[]any{}); err != nil {
			t.Fatalf("tags is not a JSON array: %v (%s)", err, first["tags"])
		}
		if !strings.Contains(string(first["tags"]), "classic") || !strings.Contains(string(first["tags"]), "desert") {
			t.Fatalf("tags = %s, want the native array values", first["tags"])
		}
		if string(document.Hits[2].Document["note"]) != "a b (c) && d" {
			t.Fatalf("a stored string note did not survive: %s", document.Hits[2].Document["note"])
		}
		// Typesense keeps an optional field that was stored with a string, but it
		// may omit an optional field whose stored value was null. Assert the
		// supported semantics: if the field is present it is still null, and it is
		// never invented as a non-null value.
		note, present := document.Hits[3].Document["note"]
		if present && string(note) != "null" {
			t.Fatalf("an optional field stored as null came back as %s", note)
		}
		if !present {
			t.Log("the optional field stored as null was omitted by Typesense, as the adapter's documented semantics allow")
		}

		genres := map[string]int{}
		year := -1
		for i, facet := range document.Facets {
			switch facet.Field {
			case "genre":
				for _, count := range facet.Counts {
					genres[count.Value] = count.Count
				}
			case "year":
				year = i
			}
		}
		if genres["science fiction"] != 10 {
			t.Fatalf("genre facet = %v, want 10 science fiction hits", genres)
		}
		if year < 0 {
			t.Fatalf("no native year facet: %+v", document.Facets)
		}
		stats := document.Facets[year].Stats
		if stats == nil || string(stats.Min) != "1951" || string(stats.Max) != "1989" {
			t.Fatalf("native year facet stats = %+v, want min 1951 and max 1989", stats)
		}

		// The same request on stdin is the documented pipe form.
		stdout, stderr, code = runTypesenseNative(t, request, "--db", fixture.source, "--request", "-")
		if code != 0 {
			t.Fatalf("stdin exit %d: %s", code, stderr)
		}
		fromStdin := decodeTypesenseSearchDocument(t, stdout)
		if fromStdin.Found != document.Found || fromStdin.Returned != document.Returned || fromStdin.Hits[0].ID != "001" {
			t.Fatalf("stdin request differed: %+v", fromStdin)
		}
	})

	t.Run("search-only-key-is-refused-metadata", func(t *testing.T) {
		t.Setenv(dbcopy.TypesenseAPIKeyEnv, fixture.searchKey)
		_, err := runDBSchema(t, "--db", fixture.source, "--format", "json")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "metadata is not permitted")
		assert.NotContains(t, err.Error(), fixture.searchKey)
		assert.NotContains(t, err.Error(), "127.0.0.1")
	})

	t.Run("metadata-key-schema-is-scoped-to-its-collections", func(t *testing.T) {
		t.Setenv(dbcopy.TypesenseAPIKeyEnv, fixture.metadataKey)
		for name, source := range map[string]string{"books": fixture.source, "semantics": fixture.semanticsSource} {
			stdout, err := runDBSchema(t, "--db", source, "--format", "json")
			require.NoError(t, err, name)
			var listing schemaListing
			require.NoError(t, json.Unmarshal([]byte(stdout), &listing), name)
			assert.True(t, strings.HasPrefix(listing.Provider, "typesense"), "%s: provider = %q", name, listing.Provider)
			require.Len(t, listing.Tables, 1, name)
			assert.Equal(t, name, listing.Tables[0].Name, name)
			assert.Equal(t, "table", listing.Tables[0].Kind, name)
		}
		// The metadata key is scoped to books and semantics, so it cannot list
		// the archive collection even though the server holds one.
		_, err := runDBSchema(t, "--db", fixture.archiveSource, "--format", "json")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "configured collection is absent from metadata")
	})

	t.Run("metadata-key-scan-writes-an-env-descriptor-only", func(t *testing.T) {
		t.Setenv(dbcopy.TypesenseAPIKeyEnv, fixture.metadataKey)
		t.Setenv(typesenseRealURLEnv, fixture.source)
		t.Setenv(dbcopy.DescriptorEnvAllowList, "")
		t.Cleanup(api.SetOpenSchemaScanForTest(dbcopy.BackendRef.OpenSchemaScan))

		scanned := t.TempDir()
		require.NoError(t, covDRunScan("-d", scanned, "-D", "typesense", "--dsn-env", typesenseRealURLEnv, "--db", "books", "--env", "local"))

		descriptorPath := filepath.Join(scanned, "connections", "local", "books.json")
		descriptorBody, err := os.ReadFile(descriptorPath)
		require.NoError(t, err)
		var descriptor map[string]any
		require.NoError(t, json.Unmarshal(descriptorBody, &descriptor))
		assert.Equal(t, map[string]any{"dsnEnv": typesenseRealURLEnv}, descriptor, "the descriptor names the variable and nothing else")
		catalogBody, err := os.ReadFile(filepath.Join(scanned, "environments", "local", "catalogs", "books", "books.db.json"))
		require.NoError(t, err)
		var catalog map[string]any
		require.NoError(t, json.Unmarshal(catalogBody, &catalog))
		assert.Equal(t, "typesense", catalog["driver"])
		assert.Equal(t, "connections/local/books.json", catalog["path"])
		assert.NotContains(t, catalog, "host")

		columnsPath := ""
		require.NoError(t, filepath.WalkDir(scanned, func(path string, entry os.DirEntry, walkErr error) error {
			require.NoError(t, walkErr)
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".columns.json") && columnsPath == "" {
				columnsPath = path
			}
			return nil
		}))
		require.NotEmpty(t, columnsPath, "the scan wrote no columns")
		var columns struct {
			Columns []struct {
				Name   string `json:"name"`
				DbType string `json:"dbType"`
			} `json:"columns"`
		}
		columnsBody, err := os.ReadFile(columnsPath)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(columnsBody, &columns))
		types := map[string]string{}
		for _, column := range columns.Columns {
			types[column.Name] = column.DbType
		}
		assert.Equal(t, "string", types["id"], "the implicit id is a string column")
		assert.Equal(t, "string[]", types["tags"], "a native string[] keeps its declared type")
		assert.NotEmpty(t, types["title"])

		for _, file := range []string{descriptorPath, filepath.Join(scanned, "environments", "local", "catalogs", "books", "books.db.json")} {
			body, readErr := os.ReadFile(file)
			require.NoError(t, readErr)
			for _, forbidden := range []string{fixture.metadataKey, "127.0.0.1"} {
				assert.NotContains(t, string(body), forbidden, file)
			}
		}
	})

	t.Run("invalid-key-is-refused-without-the-key-or-endpoint", func(t *testing.T) {
		badKey := "not-a-typesense-key-7f3a91"
		t.Setenv(dbcopy.TypesenseAPIKeyEnv, badKey)
		request := `{"q":"*","limit":1}`
		stdout, stderr, code := runTypesenseNative(t, request, "--db", fixture.source, "--request", "-")
		if code != exitCodeDatabase {
			t.Fatalf("exit %d, want %d: %s", code, exitCodeDatabase, stderr)
		}
		if strings.TrimSpace(stdout) != "" {
			t.Fatalf("a refused read printed a result: %q", stdout)
		}
		assert.Contains(t, stderr, "Typesense native search failed")
		assert.Contains(t, stderr, dbcopy.TypesenseAPIKeyEnv)
		assert.NotContains(t, stderr, badKey)
		assert.NotContains(t, stderr, "127.0.0.1")
	})
}

func TestTypesenseSavedQueryAndPolicyJourney(t *testing.T) {
	fixture := requireTypesenseRealFixture(t)
	t.Setenv(dbcopy.TypesenseAPIKeyEnv, fixture.searchKey)

	projectDir := t.TempDir()
	write := func(relative, content string) {
		path := filepath.Join(projectDir, relative)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	}
	write("datatug-project.json", `{"id":"typesense-project","title":"Typesense project","access":"private"}`)
	write("environments/local/local.env.json", `{"id":"local","dbServers":[{"driver":"typesense","catalogs":["books"]}]}`)
	write("connections/local/books.json", `{"dsnEnv":"`+typesenseRealURLEnv+`"}`)
	write("environments/local/catalogs/books/books.db.json", `{"driver":"typesense","path":"connections/local/books.json","dbModel":"books"}`)
	write("queries/books/by-author.query.json", `{"id":"books/by-author","title":"Books by author","type":"TugQL","createdAt":"2026-01-01T00:00:00Z","targets":[{"catalog":"books"}]}`)
	write("queries/books/by-author.query.tugql.yaml", `from: {name: books}
columns:
  - {field: title}
  - {field: author}
  - {field: genre}
  - {field: year}
  - {field: seq}
orderBy:
  - {field: seq}
limit: 10
`)
	write("policies/global.yaml", `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: typesense-books
default: deny
scopes:
  - path: /books
    rules:
      - id: own
        effect: allow
        operations: [query]
        where:
          op: "=="
          left: {field: author}
          right: {param: currentUser}
        fields: [title, author, genre, year, seq]
`)

	run := func() ([]map[string]any, string) {
		stdout, stderr, code := runQuery(t, "", "--project", projectDir, "--query", "books/by-author", "--env", "local", "--as", "Frank Herbert", "--format", "json")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		return decodeObjects(t, stdout), stderr
	}

	rows, stderr := run()
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want the three Frank Herbert books: %v", len(rows), rows)
	}
	for i, want := range []string{"001", "002", "003"} {
		if rows[i]["$key"] != want {
			t.Fatalf("row %d key = %v, want the preserved string id %s", i, rows[i]["$key"], want)
		}
		if rows[i]["author"] != "Frank Herbert" {
			t.Fatalf("row %d leaked or lost its author: %v", i, rows[i])
		}
		for _, denied := range []string{"rank", "big", "ratio", "note"} {
			if _, found := rows[i][denied]; found {
				t.Fatalf("a denied column reached the policy result: %v", rows[i])
			}
		}
	}
	assert.NotContains(t, stderr, "not applied", "a saved query is the policy-enforced path, not the native boundary")

	// Reopening the project is the same query and the same policy decision.
	reopened, _ := run()
	first, err := json.Marshal(rows)
	require.NoError(t, err)
	second, err := json.Marshal(reopened)
	require.NoError(t, err)
	assert.Equal(t, string(first), string(second), "reopening the project changed the policy result")

	for _, relative := range []string{"connections/local/books.json", "environments/local/catalogs/books/books.db.json", "environments/local/local.env.json"} {
		body, err := os.ReadFile(filepath.Join(projectDir, relative))
		require.NoError(t, err)
		for _, forbidden := range []string{fixture.searchKey, "127.0.0.1"} {
			assert.NotContains(t, string(body), forbidden, relative)
		}
	}
}
