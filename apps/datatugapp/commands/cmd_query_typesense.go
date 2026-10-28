package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2typesense"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/spf13/cobra"
)

const (
	// typesenseRequestMaxBytes is the hard bound on a native search request
	// document. The adapter bounds the request it sends; this bounds the JSON the
	// person hands the CLI, so an oversized document is refused before it is
	// decoded or any source is opened.
	typesenseRequestMaxBytes = 64 << 10

	// typesenseNativePolicyNote is the fixed sentence every native Typesense run
	// prints before the search executes, and that the command's help repeats. It is
	// the explicit boundary between this command and the policy-enforced structured
	// path: a native search is authorized by the scoped Typesense key alone.
	typesenseNativePolicyNote = "access: native Typesense search; the scoped Typesense API key's ACL decides what can be read, and DataTug application access policies are not applied"
)

// queryTypesenseCommand runs one explicit native Typesense search. It is
// deliberately separate from `query run`: it does not resolve a project, a saved
// query or a datatug:// alias, so it can never imply that DataTug policies were
// enforced.
func queryTypesenseCommand() *cobra.Command {
	var source, requestPath string
	cmd := &cobra.Command{
		Use:   "typesense --db <URL|env:NAME> --request <file|->",
		Short: "Run one native Typesense search using a scoped API key",
		Long: `Run one bounded native Typesense search and print its JSON result.

The source is an explicit typesense://HOST/COLLECTION, typesense+https://HOST/COLLECTION
or a loopback typesense+http://127.0.0.1:8108/COLLECTION URL, or env:NAME holding one.
The scoped Typesense API key comes from ` + dbcopy.TypesenseAPIKeyEnv + ` for every request.
Project references, datatug:// aliases and saved queries are not resolved here.

` + typesenseNativePolicyNote + `.
This is not SQL, TugQL or a SQL aggregate, and it is not the policy-enforced path:
use ` + "`datatug query run`" + ` for a structured query that DataTug access policies enforce.

--request names a JSON document, or '-' to read stdin. It is one object with at most
64 KiB and these camelCase fields (encoding/json also accepts another casing of the
same name; an unknown field such as query_by is refused, never ignored):
  collection (must match the collection of the source when given),
  q, queryBy (array of fields; required unless q is "*"), filterBy,
  sortBy (array of {"field": "...", "descending": true|false}),
  facetBy (array of fields), maxFacetValues, limit (required, positive), offset.
The adapter's own bounds apply, and a request that needs more pages, bytes or rows
than its budget is refused rather than truncated. The result keeps the native
Found/OutOf/Returned counts, the hits as stored (arrays, nulls and 64-bit integers
included), the native facets and stats, and the Partial/Complete/Truncated flags.

Examples:
  datatug query typesense --db env:DATATUG_TYPESENSE_BOOKS --request search.json
  echo '{"q":"*","queryBy":["title"],"limit":5}' | datatug query typesense --db env:DATATUG_TYPESENSE_BOOKS --request -`,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTypesenseNativeCommand(cmd, source, requestPath)
		},
	}
	cmd.Flags().StringVar(&source, queryDBFlag, "", "Typesense source: typesense://HOST[:PORT]/COLLECTION, typesense+http://127.0.0.1:8108/COLLECTION for a loopback server, or env:NAME holding one")
	cmd.Flags().StringVar(&requestPath, "request", "", "JSON search request file, or '-' for stdin")
	return cmd
}

// runTypesenseNativeCommand is `query typesense`. The command keeps the caller's
// own context (even nil) and gets it back when the invocation ends, exactly as
// `query run` does, so a reused Cobra command never inherits a cancelled
// per-invocation context.
func runTypesenseNativeCommand(cmd *cobra.Command, source, requestPath string) error {
	if source == "" {
		return Exit("--db is required: a typesense://HOST/COLLECTION source or env:NAME holding one", exitCodeUsage)
	}
	if requestPath == "" {
		return Exit("--request is required: a JSON search request file, or '-' for stdin", exitCodeUsage)
	}
	original := cmd.Context()
	parent := original
	if parent == nil {
		parent = context.Background()
	}
	ctx, _, stop := interruptContext(parent)
	cmd.SetContext(ctx)
	defer func() {
		cmd.SetContext(original)
		stop()
	}()

	// Parse only an explicit Typesense source. parseSourceArg is deliberately not
	// used: it resolves datatug:// references and bare catalog IDs, and this
	// command must never look like a project or saved-query path.
	backend, err := dbcopy.Parse(source)
	if err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}
	if !dbcopy.IsTypesenseScheme(backend.Scheme) {
		return Exit("--db must be a typesense://, typesense+https:// or typesense+http:// source, or env:NAME holding one; native search does not resolve project, saved-query or datatug:// references", exitCodeUsage)
	}
	backend = backend.WithFlag(queryDBFlag)

	stderr := cmd.ErrOrStderr()
	_, _ = fmt.Fprintln(stderr, typesenseNativePolicyNote)

	request, err := readTypesenseSearchRequest(cmd.InOrStdin(), requestPath)
	if err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}

	db, err := openBackend(ctx, backend)
	if err != nil {
		return Exit(backend.OpenFailure(err).Error(), exitCodeDatabase)
	}
	defer dbcopy.CloseSource(db)

	searcher, ok := dal.As[dalgo2typesense.NativeSearcher](db)
	if !ok {
		return Exit("the Typesense adapter exposes no native search capability", exitCodeDatabase)
	}
	result, err := searcher.Search(ctx, request)
	if err != nil {
		return Exit("Typesense native search failed: "+dbcopy.TypesenseFailureReason(err)+"; check "+dbcopy.TypesenseAPIKeyEnv+" and the key's scoped read grant on the collection", exitCodeDatabase)
	}
	encoder := json.NewEncoder(cmd.OutOrStdout())
	return encoder.Encode(nativeSearchOutputOf(result))
}

// The fixed classifications of a request this command refuses before any
// provider I/O. They say what is wrong and never echo a field name, a value or a
// decoder's own sentence: a request document can hold a secret, and
// encoding/json quotes the unknown field's name in its error. The errors stay
// comparable with errors.Is so a test can prove which refusal a malformed
// request produced.
var (
	errTypesenseRequestEmpty        = errors.New("the search request is empty: expected one JSON object")
	errTypesenseRequestTooLarge     = fmt.Errorf("the search request is larger than %d bytes (64 KiB)", typesenseRequestMaxBytes)
	errTypesenseRequestNotObject    = errors.New("the search request must be one JSON object, not null, an array or a bare value")
	errTypesenseRequestUnknownField = errors.New("the search request contains an unknown field: see --help for the accepted camelCase fields")
	errTypesenseRequestWrongType    = errors.New("the search request states a field of the wrong JSON type: see --help for the accepted fields")
	errTypesenseRequestMalformed    = errors.New("the search request is not valid JSON")
	errTypesenseRequestInvalid      = errors.New("the search request is not a valid search request")
	errTypesenseRequestTrailing     = errors.New("the search request must be one JSON object and nothing after it")
	errTypesenseRequestLimit        = errors.New("the search request must state a positive limit explicitly")
	errTypesenseRequestUnreadable   = errors.New("the search request could not be read")
	errTypesenseRequestNoStdin      = errors.New("--request - needs stdin")
)

// readTypesenseSearchRequest reads one bounded JSON search request from stdin when
// requestPath is "-", or from the file it names. The document is one object with
// no unknown field and nothing after it, and it must state a positive limit: that
// is checked before any provider I/O, so a malformed or unbounded request never
// opens a source. A read failure is reported as a fixed classification, never as
// the reader's own sentence, which could hold a value from the request.
func readTypesenseSearchRequest(stdin io.Reader, requestPath string) (dalgo2typesense.SearchRequest, error) {
	var data []byte
	var err error
	if requestPath == "-" {
		if stdin == nil {
			return dalgo2typesense.SearchRequest{}, errTypesenseRequestNoStdin
		}
		data, err = readBoundedTypesenseRequest(stdin)
	} else {
		file, openErr := os.Open(requestPath)
		if openErr != nil {
			return dalgo2typesense.SearchRequest{}, fmt.Errorf("the search request file %q could not be opened", requestPath)
		}
		defer func() { _ = file.Close() }()
		data, err = readBoundedTypesenseRequest(file)
	}
	if err != nil {
		return dalgo2typesense.SearchRequest{}, err
	}
	return decodeTypesenseSearchRequest(data)
}

// readBoundedTypesenseRequest reads at most one byte beyond the hard bound, so an
// oversized document is detected without reading all of it. The extra byte lets
// decodeTypesenseSearchRequest distinguish "exactly at the bound" from "over it".
func readBoundedTypesenseRequest(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, typesenseRequestMaxBytes+1))
	if err != nil {
		return nil, errTypesenseRequestUnreadable
	}
	return data, nil
}

// decodeTypesenseSearchRequest is the strict decoder of the bounded request
// document. The adapter's SearchRequest has no JSON tags, so encoding/json binds
// the documented camelCase names (collection, q, queryBy, filterBy, sortBy with
// {field, descending}, facetBy, maxFacetValues, limit, offset) by case-folded
// field name; an unknown field, a snake_case name included, is refused.
//
// The hard 64 KiB bound is applied to the raw bytes before any whitespace is
// trimmed, so padding cannot carry a document past the bound, and a hidden suffix
// beyond the bound cannot be dropped and forgotten. Both decoder and reader
// failures become fixed classifications: the decoder's sentence quotes an unknown
// field's name, and a field value can be secret.
func decodeTypesenseSearchRequest(data []byte) (dalgo2typesense.SearchRequest, error) {
	if len(data) > typesenseRequestMaxBytes {
		return dalgo2typesense.SearchRequest{}, errTypesenseRequestTooLarge
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return dalgo2typesense.SearchRequest{}, errTypesenseRequestEmpty
	}
	if trimmed[0] != '{' {
		return dalgo2typesense.SearchRequest{}, errTypesenseRequestNotObject
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	var request dalgo2typesense.SearchRequest
	if err := decoder.Decode(&request); err != nil {
		return dalgo2typesense.SearchRequest{}, classifyTypesenseRequestDecodeError(err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return dalgo2typesense.SearchRequest{}, errTypesenseRequestTrailing
	}
	if request.Limit <= 0 {
		return dalgo2typesense.SearchRequest{}, errTypesenseRequestLimit
	}
	return request, nil
}

// classifyTypesenseRequestDecodeError maps a decoder error to one fixed
// classification. It never returns err itself: encoding/json puts the unknown
// field name in its text, and the name is request data.
func classifyTypesenseRequestDecodeError(err error) error {
	var (
		syntax  *json.SyntaxError
		typeErr *json.UnmarshalTypeError
	)
	switch {
	case errors.As(err, &syntax), errors.Is(err, io.ErrUnexpectedEOF):
		return errTypesenseRequestMalformed
	case errors.As(err, &typeErr):
		return errTypesenseRequestWrongType
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		return errTypesenseRequestUnknownField
	default:
		return errTypesenseRequestInvalid
	}
}

// nativeSearchOutput is the JSON shape of one native search. It mirrors the
// adapter's SearchResult field for field: the native counts, the hits as stored
// (arrays, nulls and 64-bit integers preserved), the native facets and stats, and
// the partial/complete/truncated flags. Nothing is flattened into a SQL aggregate
// and no count is recomputed locally.
type nativeSearchOutput struct {
	Collection   string            `json:"collection"`
	Found        int               `json:"found"`
	OutOf        int               `json:"outOf"`
	Page         int               `json:"page"`
	Returned     int               `json:"returned"`
	SearchTimeMS int               `json:"searchTimeMs"`
	Partial      bool              `json:"partial"`
	Complete     bool              `json:"complete"`
	Truncated    bool              `json:"truncated"`
	Provenance   string            `json:"provenance"`
	Hits         []nativeSearchHit `json:"hits"`
	Facets       []nativeFacet     `json:"facets"`
}

// nativeSearchHit is one native hit: the Typesense string id and the document as
// the adapter decoded it.
type nativeSearchHit struct {
	ID        string         `json:"id"`
	Document  map[string]any `json:"document"`
	TextMatch int64          `json:"textMatch"`
	Curated   bool           `json:"curated"`
}

// nativeFacet is one native facet: its field, its counts and its native stats.
type nativeFacet struct {
	Field  string             `json:"field"`
	Counts []nativeFacetValue `json:"counts"`
	Stats  *nativeFacetStats  `json:"stats,omitempty"`
}

// nativeFacetValue is one native facet count.
type nativeFacetValue struct {
	Value       string `json:"value"`
	Count       int    `json:"count"`
	Highlighted string `json:"highlighted"`
}

// nativeFacetStats keeps the native numeric class of each statistic: an integral
// value stays int64 or uint64, a fractional one stays float64.
type nativeFacetStats struct {
	Min         any `json:"min"`
	Max         any `json:"max"`
	Sum         any `json:"sum"`
	Avg         any `json:"avg"`
	TotalValues int `json:"totalValues"`
}

// nativeSearchOutputOf mirrors result into the documented output shape.
func nativeSearchOutputOf(result dalgo2typesense.SearchResult) nativeSearchOutput {
	out := nativeSearchOutput{
		Collection:   result.Collection,
		Found:        result.Found,
		OutOf:        result.OutOf,
		Page:         result.Page,
		Returned:     result.Returned,
		SearchTimeMS: result.SearchTimeMS,
		Partial:      result.Partial,
		Complete:     result.Complete,
		Truncated:    result.Truncated,
		Provenance:   result.Provenance,
		Hits:         make([]nativeSearchHit, 0, len(result.Hits)),
		Facets:       make([]nativeFacet, 0, len(result.Facets)),
	}
	for _, hit := range result.Hits {
		out.Hits = append(out.Hits, nativeSearchHit{
			ID:        hit.ID,
			Document:  hit.Document,
			TextMatch: hit.TextMatch,
			Curated:   hit.Curated,
		})
	}
	for _, facet := range result.Facets {
		converted := nativeFacet{Field: facet.Field, Counts: make([]nativeFacetValue, 0, len(facet.Counts))}
		for _, value := range facet.Counts {
			converted.Counts = append(converted.Counts, nativeFacetValue{
				Value:       value.Value,
				Count:       value.Count,
				Highlighted: value.Highlighted,
			})
		}
		if facet.Stats != nil {
			converted.Stats = &nativeFacetStats{
				Min:         facet.Stats.Min,
				Max:         facet.Stats.Max,
				Sum:         facet.Stats.Sum,
				Avg:         facet.Stats.Avg,
				TotalValues: facet.Stats.TotalValues,
			}
		}
		out.Facets = append(out.Facets, converted)
	}
	return out
}
