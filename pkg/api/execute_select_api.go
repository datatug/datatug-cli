package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/datatug-core/storage"
	"github.com/datatug/datatug-cli/pkg/secureread"
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
	sourceURL, err := resolveSourceURL(ctx, projStore, request.Environment, request.Database)
	if err != nil {
		return QueryResultResponse{}, err
	}

	var result secureread.Result
	if request.SQL != "" {
		result, err = executor.RunNativeSQL(ctx, sourceURL, request.SQL)
	} else {
		var query dal.Query
		if query, err = buildSelectQuery(request); err == nil {
			result, err = executor.RunStructured(ctx, sourceURL, query, nil)
		}
	}
	if err != nil {
		return QueryResultResponse{}, err
	}
	return resultToResponse(result), nil
}

// buildSelectQuery turns a "from"-shaped SelectRequest into a
// dal.StructuredQuery, the same builder pattern
// apps/datatugapp/commands/cmd_query.go's buildQuery uses for `--from`.
// Where carries ";"-separated "field:value" equality conditions, AND-ed
// together — the same format the pre-secureread /exec/select handler
// accepted (SelectRequest.Validate requires the ":" separator).
func buildSelectQuery(request SelectRequest) (dal.Query, error) {
	var builder dal.IQueryBuilder = dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef(request.From, "")))
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
