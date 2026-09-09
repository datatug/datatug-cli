package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/strongo/validation"
)

// SelectRequest holds request data for GET /datatug/exec/select.
type SelectRequest struct {
	Project     string
	Environment string
	Database    string
	From        string
	SQL         string
	Where       string
	Limit       int
	Columns     []string
}

// Validate returns error if not valid
func (v SelectRequest) Validate() error {
	if v.From == "" && v.SQL == "" {
		return validation.NewErrRequestIsMissingRequiredField("from OR sql")
	}
	if v.SQL != "" {
		if v.From != "" {
			return validation.NewErrBadRequestFieldValue("from", "can't be supplied when 'sql' parameter passed")
		}
		if v.Where != "" {
			return validation.NewErrBadRequestFieldValue("where", "can't be supplied when 'sql' parameter passed")
		}
		if len(v.Columns) > 0 {
			return validation.NewErrBadRequestFieldValue("cols", "can't be supplied when 'sql' parameter passed")
		}
	}
	if v.Project == "" {
		return validation.NewErrRequestIsMissingRequiredField("project")
	}
	if v.Environment == "" {
		return validation.NewErrRequestIsMissingRequiredField("environment")
	}
	if v.Database == "" {
		return validation.NewErrRequestIsMissingRequiredField("database")
	}
	if v.Where != "" && !strings.Contains(v.Where, ":") {
		return validation.NewErrBadRequestFieldValue("where", "should have : char to separate field from value")
	}
	if v.Limit < -1 {
		return validation.NewErrBadRequestFieldValue("limit", "should be >= -1")
	}
	return nil
}

// ExecuteSelect executes a select through the policy-enforced
// secureread.Executor (REQ:server-acl-all-reads): a "from" selection runs
// through RunStructured, raw "sql" text through RunNativeSQL. Every read the
// web UI can trigger through `datatug serve` MUST come through this one
// path — see routes.go's executeRoutes / execute_endpoints.go.
func ExecuteSelect(ctx context.Context, storeID string, request SelectRequest) (QueryResultResponse, error) {
	if err := request.Validate(); err != nil {
		return QueryResultResponse{}, err
	}
	executor, ok := SecureExecutor()
	if !ok {
		return QueryResultResponse{}, errors.New("exec/select: server has no policy-enforced session configured")
	}
	store, err := storage.NewDatatugStore(storeID)
	if err != nil {
		return QueryResultResponse{}, err
	}
	projStore := store.GetProjectStore(request.Project)
	projDir, _ := projectDir(request.Project)
	sourceURL, driver, err := resolveSourceURL(ctx, projStore, request.Environment, request.Database, projDir)
	if err != nil {
		return QueryResultResponse{}, err
	}

	var result secureread.Result
	// executionProfile/collection mirror exec/run_query's own
	// Provenance.ExecutionProfile distinction (S101, Fix 2): native SQL
	// text is opaque-privileged (no row/column policy applied to it — see
	// pkg/secureread/native_sql.go's REQ:opaque-sql-limitation); a "from"
	// selection is a policy-enforced structured read. Collection is empty
	// for SQL text, which names no single collection.
	executionProfile := apicontract.ExecutionProfileProtected
	collection := request.From
	if request.SQL != "" {
		executionProfile = apicontract.ExecutionProfileOpaquePrivileged
		collection = ""
		result, err = executor.RunNativeSQL(ctx, sourceURL, request.SQL)
	} else {
		var query dal.Query
		if query, err = buildSelectQuery(request, driver); err == nil {
			result, err = executor.RunStructured(ctx, sourceURL, query, nil)
			err = policyDenialNamingOriginalResource(err, request.From, driver)
		}
	}
	if err != nil {
		return QueryResultResponse{}, err
	}
	return resultToResponse(result, executionProfile, request.Database, collection), nil
}

// policyDenialNamingOriginalResource re-names an access-denied error's
// resource back to the client's originally-requested physical name when
// buildSelectQuery narrowed it for policy matching (S101: "a denial still
// logs the original request form") — every other error, and every
// non-denial outcome, passes through completely unchanged. Wraps with %w
// so errors.Is(err, secureread.ErrAccessDenied) still matches through it
// (util_error_handling.go's existing ACCESS_DENIED/403 classification is
// unaffected).
func policyDenialNamingOriginalResource(err error, originalFrom, driver string) error {
	if err == nil || !errors.Is(err, secureread.ErrAccessDenied) {
		return err
	}
	if PolicyCollectionName(originalFrom, driver) == originalFrom {
		return err // nothing was narrowed; the error already names originalFrom
	}
	return fmt.Errorf("%w (requested as %q)", err, originalFrom)
}

// buildSelectQuery turns a "from"-shaped SelectRequest into a
// dal.StructuredQuery, the same builder pattern
// apps/datatugapp/commands/cmd_query.go's buildQuery uses for `--from`.
// Where carries ";"-separated "field:value" equality conditions, AND-ed
// together — the same format the pre-secureread /exec/select handler
// accepted (SelectRequest.Validate requires the ":" separator).
//
// The query's FROM collection uses PolicyCollectionName(request.From,
// driver), not request.From verbatim (S101): dal-go/dalgo's own
// access.SecureReadSession derives its policy-matching Resource path
// directly from this same collection name — there is no separate
// "policy-only" name in DALgo's model — so a policy authored against the
// semantic layer's bare collection convention ("/Customer") would
// otherwise never match a browser's schema-qualified physical request
// ("main.Customer"), denying every read regardless of row content. Safe
// for execution too: stripping only ever happens when the qualifier IS the
// driver's own default schema, in which case the qualified and bare forms
// name the identical physical table (SQLite's implicit "main", Postgres's
// default "public" search_path entry).
func buildSelectQuery(request SelectRequest, driver string) (dal.Query, error) {
	collectionName := PolicyCollectionName(request.From, driver)
	var builder dal.IQueryBuilder = dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef(collectionName, "")))
	if request.Where != "" {
		for _, condition := range strings.Split(request.Where, ";") {
			parts := strings.SplitN(condition, ":", 2)
			if len(parts) != 2 || parts[0] == "" {
				return nil, validation.NewErrBadRequestFieldValue("where", fmt.Sprintf("each condition must be field:value, got %q", condition))
			}
			builder = builder.WhereField(parts[0], dal.Equal, parts[1])
		}
	}
	if request.Limit >= 0 {
		builder = builder.Limit(request.Limit)
	}
	columns := make([]dal.Column, 0, len(request.Columns))
	for _, name := range request.Columns {
		columns = append(columns, dal.Column{Expression: dal.Field(name)})
	}
	return builder.SelectColumns(columns...), nil
}
