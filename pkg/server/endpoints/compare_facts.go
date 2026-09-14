package endpoints

import (
	"context"
	"fmt"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dtql"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/investigation"
)

// executeCompareFactsSide binds one current-policy-visible incident cohort to
// one saved DTQL query parameter. It accepts only structured SQLite and
// inGitDB paths whose parsed query proves an exact `field In $parameter` node.
// HTTP templates and opaque SQL cannot provide that proof, so they fail before
// computeRunQuery reaches a data source rather than looping or filtering rows.
func executeCompareFactsSide(ctx context.Context, request apicontract.CompareRequest, side apicontract.CompareSideSpec) (compareSideData, error) {
	if request.Incident == nil {
		return compareSideData{}, newInvalidRequest("incident", "an incident is required for a facts side")
	}
	_, stored, events, err := compareIncidentState(ctx, request)
	if err != nil {
		return compareSideData{}, err
	}
	view, _, err := api.IncidentView(ctx, stored, events)
	if err != nil {
		return compareSideData{}, newAccessDenied("current access policy does not permit this incident")
	}
	cohort, entity, field, factIDs, err := visibleFactCohort(view, side)
	if err != nil {
		return compareSideData{}, err
	}
	parameterID, err := proveNativeFactsBinding(ctx, request.QueryID, side, entity, field)
	if err != nil {
		return compareSideData{}, err
	}
	set, groups, err := apicontract.NormalizeTypedValueSet(apicontract.TypedValueSet{Values: cohort}, factIDs)
	if err != nil {
		return compareSideData{}, newInvalidRequest("cohortRole", "the visible cohort does not form one canonical value set")
	}
	origin := apicontract.BindingOriginEntry{
		ParameterID: parameterID, Origin: apicontract.BindingOriginContext, ValueFactIDs: groups,
	}
	return executeCompareLiveSide(ctx, request, side,
		map[string]apicontract.TypedValueOrSet{parameterID: apicontract.SetValue(set)},
		[]apicontract.BindingOriginEntry{origin})
}

func visibleFactCohort(view incidents.IncidentView, side apicontract.CompareSideSpec) (
	values []apicontract.TypedValue,
	entity, field string,
	factIDs [][]string,
	err error,
) {
	role := investigation.FactRoleAffected
	if side.CohortRole == apicontract.CompareCohortControl {
		role = investigation.FactRoleHealthyControl
	}
	wantedScope := investigation.ProjectScope{StoreID: side.StoreID, ProjectID: side.Project, Environment: side.Environment}
	for _, fact := range view.CanonicalContext.Facts {
		if !fact.Enabled || investigation.NormalizeFactLayer(fact.Layer) != investigation.FactLayerCanonical ||
			fact.Role != role || fact.Scope == nil || *fact.Scope != wantedScope || fact.Redacted() {
			continue
		}
		if entity == "" {
			entity, field = fact.Entity, fact.Field
		} else if fact.Entity != entity || fact.Field != field {
			return nil, "", "", nil, newInvalidRequest("cohortRole", "the visible cohort is ambiguous across more than one entity field")
		}
		values = append(values, *fact.Value.Value)
		factIDs = append(factIDs, []string{fact.ID})
	}
	if len(values) == 0 {
		return nil, "", "", nil, newSourceUnavailable("the incident has no current-policy-visible facts for the requested cohort and scope")
	}
	if len(values) > apicontract.CompareMaximumLimit {
		return nil, "", "", nil, newSourceUnavailable("the current-policy-visible fact cohort exceeds the supported bound")
	}
	return values, entity, field, factIDs, nil
}

func proveNativeFactsBinding(ctx context.Context, queryID string, side apicontract.CompareSideSpec, entity, field string) (string, error) {
	projectDir, ok := api.ProjectDir(side.Project)
	if !ok {
		return "", newNotFound(fmt.Sprintf("unknown project %q", side.Project))
	}
	if resolved, err := api.ResolveStoreID(side.StoreID, side.Project); err != nil || resolved != side.StoreID {
		return "", newInvalidRequest("storeId", "does not identify the exact configured project store")
	}
	canonicalID, err := api.ResolveQueryID(projectDir, queryID)
	if err != nil {
		return "", newNotFound(fmt.Sprintf("query %q not found", queryID))
	}
	projectStore, err := api.ProjectStoreFor(side.Project)
	if err != nil {
		return "", newInvalidRequest("project", err.Error())
	}
	queryDef, err := projectStore.LoadQuery(ctx, canonicalID)
	if err != nil {
		return "", newNotFound(fmt.Sprintf("query %q not found", queryID))
	}
	if queryDef.Type != datatug.QueryTypeDTQL {
		return "", newSourceUnavailable("facts comparison requires a saved structured DTQL query with native set binding")
	}
	parameterID, err := matchingCohortParameter(queryDef, entity, field)
	if err != nil {
		return "", err
	}
	executionRequest := apicontract.ExecutionRequest{
		StoreID: side.StoreID, Project: side.Project, Environment: side.Environment,
		QueryID: canonicalID,
	}
	resolved, err := resolveExecutionSource(ctx, projectStore, projectDir, executionRequest, queryDef)
	if err != nil {
		return "", err
	}
	nativeStructured := resolved.Kind == api.SourceKindSQL && strings.HasPrefix(resolved.URL, "sqlite://") ||
		resolved.Kind == api.SourceKindInGitDB && strings.HasPrefix(resolved.URL, "ingitdb://")
	if !nativeStructured {
		return "", newSourceUnavailable("facts comparison requires a structured SQLite or inGitDB source with native set binding")
	}
	document, err := executionQueryDocument(side.Project, canonicalID, queryDef, "")
	if err != nil {
		return "", newSourceUnavailable("the saved structured query document is unavailable")
	}
	query, err := dtql.Deserialize([]byte(document))
	if err != nil {
		return "", newSourceUnavailable("the saved structured query cannot prove native set binding")
	}
	count, safe := countNativeInBindings(query.Where(), field, parameterID)
	if !safe || count != 1 {
		return "", newSourceUnavailable("the saved structured query must contain one exact native field IN parameter binding")
	}
	return parameterID, nil
}

func matchingCohortParameter(queryDef *datatug.QueryDef, entity, field string) (string, error) {
	parameterID := ""
	for _, parameter := range queryDef.Parameters {
		if parameter.Meta == nil || parameter.Meta.Entity != entity || parameter.Meta.Field != field {
			continue
		}
		if !parameter.IsMultiValue || parameterID != "" {
			return "", newSourceUnavailable("the saved query must declare one unambiguous multi-value parameter for the cohort field")
		}
		parameterID = parameter.ID
	}
	if parameterID == "" {
		return "", newSourceUnavailable("the saved query has no multi-value parameter matching the cohort field")
	}
	return parameterID, nil
}

func countNativeInBindings(condition dal.Condition, field, parameter string) (int, bool) {
	switch value := condition.(type) {
	case dal.Comparison:
		left, leftOK := value.Left.(dal.FieldRef)
		right, rightOK := value.Right.(dal.Param)
		if value.Operator == dal.In && leftOK && left.Source() == "" && left.Name() == field && rightOK && right.Name == parameter {
			return 1, true
		}
		if comparisonMentionsCohortBinding(value, field, parameter) {
			return 0, false
		}
		return 0, true
	case *dal.Comparison:
		if value != nil {
			return countNativeInBindings(*value, field, parameter)
		}
	case dal.GroupCondition:
		if value.Operator() != dal.And {
			return 0, false
		}
		count := 0
		for _, child := range value.Conditions() {
			childCount, safe := countNativeInBindings(child, field, parameter)
			if !safe {
				return 0, false
			}
			count += childCount
		}
		return count, true
	case *dal.GroupCondition:
		if value != nil {
			return countNativeInBindings(*value, field, parameter)
		}
	}
	return 0, false
}

func comparisonMentionsCohortBinding(comparison dal.Comparison, field, parameter string) bool {
	for _, expression := range []dal.Expression{comparison.Left, comparison.Right} {
		switch value := expression.(type) {
		case dal.FieldRef:
			if value.Source() == "" && value.Name() == field {
				return true
			}
		case dal.Param:
			if value.Name == parameter {
				return true
			}
		}
	}
	return false
}
