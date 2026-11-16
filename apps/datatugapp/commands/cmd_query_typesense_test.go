package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2typesense"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// typesenseLeak is a unique value that must never appear in an error this
// command produces. A request document can hold a secret, and encoding/json
// quotes an unknown field's name in its own text.
const typesenseLeak = "LEAK_SECRET_typesense_7f3a91"

func TestDecodeTypesenseSearchRequestAcceptsTheDocumentedCamelCaseFields(t *testing.T) {
	request, err := decodeTypesenseSearchRequest([]byte(`{
		"collection": "books",
		"q": "sea",
		"queryBy": ["title", "author"],
		"filterBy": "year:>2000",
		"sortBy": [{"field": "year", "descending": true}],
		"facetBy": ["genre"],
		"maxFacetValues": 3,
		"limit": 5,
		"offset": 2
	}`))
	require.NoError(t, err)
	assert.Equal(t, "books", request.Collection)
	assert.Equal(t, "sea", request.Q)
	assert.Equal(t, []string{"title", "author"}, request.QueryBy)
	assert.Equal(t, "year:>2000", request.FilterBy)
	require.Len(t, request.SortBy, 1)
	assert.Equal(t, "year", request.SortBy[0].Field)
	assert.True(t, request.SortBy[0].Descending)
	assert.Equal(t, []string{"genre"}, request.FacetBy)
	assert.Equal(t, 3, request.MaxFacetValues)
	assert.Equal(t, 5, request.Limit)
	assert.Equal(t, 2, request.Offset)
}

func TestDecodeTypesenseSearchRequestRefusesAnUnknownFieldWithoutEchoingIt(t *testing.T) {
	for name, document := range map[string]string{
		"unknown snake_case field": `{"limit":1,"query_by":["title"]}`,
		"unknown secret field":     `{"limit":1,"` + typesenseLeak + `":true}`,
		"unknown nested field":     `{"limit":1,"sortBy":[{"field":"year","` + typesenseLeak + `":true}]}`,
	} {
		_, err := decodeTypesenseSearchRequest([]byte(document))
		require.Error(t, err, name)
		assert.ErrorIs(t, err, errTypesenseRequestUnknownField, name)
		assert.NotContains(t, err.Error(), typesenseLeak, name)
		assert.NotContains(t, err.Error(), "query_by", name)
	}
}

func TestDecodeTypesenseSearchRequestRefusesAWrongTypeWithoutEchoingTheValue(t *testing.T) {
	for name, document := range map[string]string{
		"limit as a string":  `{"limit":"` + typesenseLeak + `"}`,
		"queryBy as a value": `{"limit":1,"queryBy":"` + typesenseLeak + `"}`,
		"sortBy as a field":  `{"limit":1,"sortBy":"` + typesenseLeak + `"}`,
	} {
		_, err := decodeTypesenseSearchRequest([]byte(document))
		require.Error(t, err, name)
		assert.ErrorIs(t, err, errTypesenseRequestWrongType, name)
		assert.NotContains(t, err.Error(), typesenseLeak, name)
	}
}

func TestDecodeTypesenseSearchRequestRefusesMalformedInputWithAFixedClassification(t *testing.T) {
	for name, document := range map[string]string{
		"truncated":          `{"limit":`,
		"unterminated value": `{"limit":1,"q":"` + typesenseLeak,
		"bare token":         `{"limit":1} ` + typesenseLeak,
	} {
		_, err := decodeTypesenseSearchRequest([]byte(document))
		require.Error(t, err, name)
		assert.NotContains(t, err.Error(), typesenseLeak, name)
		assert.NotContains(t, err.Error(), "invalid character", name)
	}
}

func TestDecodeTypesenseSearchRequestRequiresOneObjectWithAPositiveLimit(t *testing.T) {
	for name, tc := range map[string]struct {
		document string
		want     error
	}{
		"empty":            {"", errTypesenseRequestEmpty},
		"whitespace only":  {" \n\t ", errTypesenseRequestEmpty},
		"null":             {"null", errTypesenseRequestNotObject},
		"array":            {"[{\"limit\":1}]", errTypesenseRequestNotObject},
		"bare value":       {"1", errTypesenseRequestNotObject},
		"two objects":      {`{"limit":1}{"limit":2}`, errTypesenseRequestTrailing},
		"trailing garbage": {`{"limit":1} ` + typesenseLeak, errTypesenseRequestTrailing},
		"zero limit":       {`{"limit":0}`, errTypesenseRequestLimit},
		"missing limit":    {`{}`, errTypesenseRequestLimit},
	} {
		_, err := decodeTypesenseSearchRequest([]byte(tc.document))
		require.Error(t, err, name)
		assert.ErrorIs(t, err, tc.want, name)
		assert.NotContains(t, err.Error(), typesenseLeak, name)
	}
}

// The hard bound is applied to the raw bytes, before whitespace is trimmed.
// Padding a small object with whitespace to go past 64 KiB is refused, so
// overflow cannot be hidden behind the trim; an object of exactly 64 KiB is
// still accepted.
func TestDecodeTypesenseSearchRequestBoundsTheRawBytesBeforeTrimming(t *testing.T) {
	object := []byte(`{"limit":1}`)

	paddedOverflow := append(bytes.Repeat([]byte(" "), typesenseRequestMaxBytes), object...)
	_, err := decodeTypesenseSearchRequest(paddedOverflow)
	require.Error(t, err, "raw %d bytes must exceed the bound", len(paddedOverflow))
	assert.ErrorIs(t, err, errTypesenseRequestTooLarge)
	assert.NotContains(t, err.Error(), typesenseLeak)

	atBound := append(bytes.Repeat([]byte(" "), typesenseRequestMaxBytes-len(object)), object...)
	require.Len(t, atBound, typesenseRequestMaxBytes)
	request, err := decodeTypesenseSearchRequest(atBound)
	require.NoError(t, err)
	assert.Equal(t, 1, request.Limit)
}

// A reader whose error text quotes a secret is replaced by a fixed
// classification: the command never shows a reader's own sentence.
func TestReadTypesenseSearchRequestRefusesAReaderFailureWithoutEchoingIt(t *testing.T) {
	_, err := readTypesenseSearchRequest(failingTypesenseReader{}, "-")
	require.Error(t, err)
	assert.ErrorIs(t, err, errTypesenseRequestUnreadable)
	assert.NotContains(t, err.Error(), typesenseLeak)
	assert.NotContains(t, err.Error(), "reader failed")
}

// The command prints the policy boundary before execution and refuses a
// malformed request before it opens a source, so zero provider I/O happens for
// an unbounded or invalid document.
func TestQueryTypesenseRefusesBadInputBeforeAnyProviderIO(t *testing.T) {
	original := openBackend
	t.Cleanup(func() { openBackend = original })
	opens := 0
	openBackend = func(context.Context, dbcopy.BackendRef) (dal.DB, error) {
		opens++
		return nil, errors.New("must not open")
	}

	for name, request := range map[string]string{
		"empty":            ``,
		"missing limit":    `{}`,
		"zero limit":       `{"limit":0}`,
		"unknown field":    `{"limit":1,"query_by":["title"]}`,
		"secret attribute": `{"limit":1,"` + typesenseLeak + `":true}`,
		"not one object":   `[{"limit":1}]`,
		"trailing object":  `{"limit":1}{"limit":2}`,
		"overflow":         strings.Repeat(" ", typesenseRequestMaxBytes) + `{"limit":1}`,
	} {
		out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
		cmd := queryTypesenseCommand()
		cmd.SetOut(out)
		cmd.SetErr(errOut)
		cmd.SetIn(strings.NewReader(request))
		require.NoError(t, cmd.Flags().Set(queryDBFlag, "typesense+http://127.0.0.1:8108/books"), name)
		require.NoError(t, cmd.Flags().Set("request", "-"), name)

		err := cmd.RunE(cmd, nil)
		require.Error(t, err, name)
		var coder ExitCoder
		require.True(t, errors.As(err, &coder), name)
		assert.Equal(t, exitCodeUsage, coder.ExitCode(), name)
		assert.NotContains(t, err.Error(), typesenseLeak, name)
		assert.Contains(t, errOut.String(), typesenseNativePolicyNote, "the boundary is stated before a request is read: %s", name)
		assert.Empty(t, out.String(), name)
	}
	assert.Zero(t, opens, "no invalid request may open a source")
}

func TestQueryTypesenseRefusesANonTypesenseSource(t *testing.T) {
	original := openBackend
	t.Cleanup(func() { openBackend = original })
	openBackend = func(context.Context, dbcopy.BackendRef) (dal.DB, error) {
		t.Fatal("a non-Typesense source must be refused before open")
		return nil, nil
	}
	err := runTypesenseNativeCommand(&cobra.Command{}, "sqlite:///book.db", "-")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a typesense://")
}

func TestQueryTypesenseHelpStatesThePolicyBoundaryAndNoProjectAliases(t *testing.T) {
	cmd := queryTypesenseCommand()
	assert.Contains(t, cmd.Long, typesenseNativePolicyNote)
	assert.Contains(t, cmd.Long, "not applied")
	assert.Contains(t, cmd.Long, "not SQL, TugQL or a SQL aggregate")
	assert.Contains(t, cmd.Long, "64 KiB")
	assert.Contains(t, cmd.Long, "unknown field")
	assert.NotNil(t, cmd.Flags().Lookup(queryDBFlag))
	assert.NotNil(t, cmd.Flags().Lookup("request"))
	assert.Nil(t, cmd.Flags().Lookup(queryProjectFlag), "native search never resolves a project")
	assert.Nil(t, cmd.Flags().Lookup(queryQueryFlag), "native search never resolves a saved query")

	// The command is registered on the query group, next to the policy path.
	found := false
	for _, sub := range queryCommand().Commands() {
		found = found || sub.Name() == "typesense"
	}
	assert.True(t, found, "`query typesense` is registered")
}

func TestPagingOrderBypassesMetadataForTypesense(t *testing.T) {
	order := pagingOrder(context.Background(), nil, dbcopy.TypesenseScheme, "books")
	assert.Empty(t, order, "a Typesense --from page keeps the engine's native order without a metadata request")
}

func TestOffsetHelpIsHonestAboutTypesenseOrdering(t *testing.T) {
	usage := queryRunCommand().Flags().Lookup(queryOffsetFlag).Usage
	assert.Contains(t, usage, "native relevance order")
	assert.Contains(t, usage, "no generic stable-key guarantee")
}

// TestQueryTypesensePrintsTheNativeResultPreservingTypes covers the success path
// with a substituted backend: the result keeps native counts, string-id hits,
// arrays, nulls, int64 values and facets, instead of being flattened.
func TestQueryTypesensePrintsTheNativeResultPreservingTypes(t *testing.T) {
	original := openBackend
	t.Cleanup(func() { openBackend = original })
	fake := &fakeTypesenseSearcher{
		result: dalgo2typesense.SearchResult{
			Collection:   "books",
			Found:        7,
			OutOf:        7,
			Page:         1,
			Returned:     2,
			SearchTimeMS: 3,
			Partial:      true,
			Complete:     false,
			Truncated:    true,
			Provenance:   dalgo2typesense.ProvenanceSearch,
			Hits: []dalgo2typesense.SearchHit{
				{ID: "b-1", Document: map[string]any{"id": "b-1", "tags": []any{"a", "b"}, "deleted": nil, "copies": int64(9007199254740993)}, TextMatch: 101, Curated: true},
				{ID: "b-2", Document: map[string]any{"id": "b-2", "tags": []any{}, "copies": int64(1)}},
			},
			Facets: []dalgo2typesense.Facet{
				{
					Field:  "genre",
					Counts: []dalgo2typesense.FacetValue{{Value: "fiction", Count: 2, Highlighted: "fiction"}},
					Stats:  &dalgo2typesense.FacetStats{Min: int64(1), Max: int64(2), Sum: int64(3), Avg: 1.5, TotalValues: 2},
				},
			},
		},
	}
	openBackend = func(context.Context, dbcopy.BackendRef) (dal.DB, error) { return fake, nil }

	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	cmd := queryTypesenseCommand()
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetIn(strings.NewReader(`{"collection":"books","q":"*","queryBy":["title"],"limit":2}`))
	require.NoError(t, cmd.Flags().Set(queryDBFlag, "typesense+http://127.0.0.1:8108/books"))
	require.NoError(t, cmd.Flags().Set("request", "-"))

	require.NoError(t, cmd.RunE(cmd, nil))
	assert.True(t, fake.searched)
	assert.Equal(t, 2, fake.request.Limit)
	assert.Equal(t, "books", fake.request.Collection)
	assert.Contains(t, errOut.String(), typesenseNativePolicyNote)

	var printed map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(out.Bytes(), &printed))
	for _, field := range []string{"found", "outOf", "returned", "partial", "complete", "truncated", "provenance", "hits", "facets"} {
		assert.Contains(t, printed, field)
	}
	assert.Contains(t, string(printed["hits"]), "9007199254740993", "an int64 above 2^53 must survive verbatim")
	assert.Contains(t, string(printed["hits"]), `"deleted":null`)
	assert.Contains(t, string(printed["hits"]), `"tags":["a","b"]`)
	assert.Contains(t, string(printed["facets"]), `"stats"`)
}

type failingTypesenseReader struct{}

func (failingTypesenseReader) Read([]byte) (int, error) {
	return 0, errors.New("reader failed for " + typesenseLeak)
}

var _ io.Reader = failingTypesenseReader{}

// fakeTypesenseSearcher is a dal.DB that exposes only the adapter's native
// search capability; Close is a no-op so CloseSource is safe.
type fakeTypesenseSearcher struct {
	dal.DB
	searched bool
	request  dalgo2typesense.SearchRequest
	result   dalgo2typesense.SearchResult
	err      error
}

func (f *fakeTypesenseSearcher) Search(_ context.Context, request dalgo2typesense.SearchRequest) (dalgo2typesense.SearchResult, error) {
	f.searched = true
	f.request = request
	return f.result, f.err
}

func (f *fakeTypesenseSearcher) Close() error { return nil }
