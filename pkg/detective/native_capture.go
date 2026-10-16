package detective

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/datatug/datatug-core/pkg/apicontract"
	core "github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
)

// NativeQuerySnapshot must load the saved definition and revision together.
// It performs no source execution. The resulting bytes are privately pinned.
type NativeQuerySnapshot func(context.Context, string) (sessioncontext.DetectiveAcceptedQueryPin, error)

type NativeCaptureRequest struct {
	CheckID, CaseID, NativeOperationID, Actor string
	Inputs                                    map[string]InputValue
	Reference                                 apicontract.SessionInputReference
}

type NativeCapture struct {
	Pin     sessioncontext.DetectiveInputPin
	Case    *core.Case
	Binding core.OperationBinding
}

// CaptureNativeInput captures the existing native catalog and Case under native
// admission. Session Pin must run after this method releases admission, then
// execution uses only its sealed snapshot through the existing Core evaluator.
func (s *LocalStore) CaptureNativeInput(ctx context.Context, req NativeCaptureRequest, querySnapshot NativeQuerySnapshot) (NativeCapture, error) {
	var out NativeCapture
	if querySnapshot == nil || req.Actor == "" || !validSlug(req.NativeOperationID) {
		return out, ErrInvalid
	}
	err := s.withLock(ctx, func() error {
		catalog, err := core.LoadCatalog(ctx, req.CheckID, "", catalogSnapshotReader{s})
		if err != nil {
			return err
		}
		definition := sessioncontext.DetectiveCheckDefinitionPin{SchemaVersion: 1, Check: catalog.Checks[req.CheckID]}
		for id, check := range catalog.Checks {
			if id != req.CheckID {
				definition.Dependencies = append(definition.Dependencies, check)
			}
		}
		for _, validator := range catalog.Validators {
			definition.PublishedValidators = append(definition.PublishedValidators, validator)
		}
		for _, validators := range catalog.QueryValidators {
			definition.QueryValidators = append(definition.QueryValidators, validators)
		}
		_, err = captureQueryRevisions(ctx, catalog, "", func(ctx context.Context, id string) (string, error) {
			query, err := querySnapshot(ctx, id)
			if err != nil {
				return "", err
			}
			if query.QueryID != id {
				return "", ErrInvalid
			}
			definition.Queries = append(definition.Queries, query)
			return query.Revision, nil
		})
		if err != nil {
			return err
		}
		sort.Slice(definition.Dependencies, func(i, j int) bool { return definition.Dependencies[i].ID < definition.Dependencies[j].ID })
		sort.Slice(definition.PublishedValidators, func(i, j int) bool {
			return definition.PublishedValidators[i].ID < definition.PublishedValidators[j].ID
		})
		sort.Slice(definition.QueryValidators, func(i, j int) bool {
			return definition.QueryValidators[i].QueryID < definition.QueryValidators[j].QueryID
		})
		definition, digest, err := sessioncontext.CanonicalDetectiveDefinition(ctx, definition)
		if err != nil {
			return err
		}
		var caseContext map[string]InputValue
		dispatch := sessioncontext.DetectiveDispatchBinding{NativeOperationID: req.NativeOperationID, NativeStoreID: "local", NativeProjectID: s.projectID, Action: "run-check", CheckID: req.CheckID, CheckVersion: definition.Check.Version, DefinitionDigest: digest}
		if req.CaseID != "" {
			item, err := s.getCase(req.CaseID)
			if err != nil {
				return err
			}
			if item.Status != "active" {
				return ErrInvalid
			}
			out.Case = &item
			caseContext = item.Context
			dispatch.CaseID, dispatch.CaseVersion = item.ID, item.Version
			dispatch.CaseContextDigest, err = caseContextDigest(caseContext)
			if err != nil {
				return err
			}
		}
		inputs, err := core.BindCaseInputs(definition.Check.Inputs, req.Inputs, caseContext)
		if err != nil {
			return err
		}
		reference := req.Reference
		reference.DefinitionDigest = digest
		bound := map[string]bool{}
		for _, binding := range reference.Bindings {
			bound[binding.InputName] = true
		}
		for name := range inputs {
			if bound[name] {
				continue
			}
			binding := apicontract.SessionInputBindingRef{InputName: name, Origin: "manual"}
			if out.Case != nil {
				if _, ok := out.Case.Context[name]; ok {
					binding.Origin = "case"
					binding.CaseID = out.Case.ID
					binding.CaseVersion = out.Case.Version
				}
			}
			reference.Bindings = append(reference.Bindings, binding)
		}
		reference, err = reference.Canonical()
		if err != nil {
			return err
		}
		action := core.ActionRequest{Type: "run-check", Project: s.projectID, CheckID: req.CheckID, CaseID: req.CaseID, Environment: reference.EnvironmentID, Inputs: inputs, InputReference: &reference, ExpectedDefinitionDigest: digest, ExpectedCaseContextDigest: dispatch.CaseContextDigest}
		payload, err := core.ActionOperationPayload(action)
		if err != nil {
			return err
		}
		out.Binding, err = nativeBindOperation(s.projectID, req.Actor, "run-check", req.CheckID, payload)
		if err != nil {
			return err
		}
		dispatch.NativeRequestDigest = out.Binding.PayloadDigest
		out.Pin = sessioncontext.DetectiveInputPin{Reference: reference, Definition: definition, EffectiveInputs: inputs, Dispatch: dispatch}
		return nil
	})
	if err != nil {
		return NativeCapture{}, err
	}
	return out, nil
}

// pinnedNativeCatalog uses Core.LoadCatalog to validate and seal the captured
// graph again. It never falls back to current files or implements validation.
type pinnedNativeCatalog struct {
	definition sessioncontext.DetectiveCheckDefinitionPin
}

func (r pinnedNativeCatalog) GetCheck(_ context.Context, id string) (core.Check, error) {
	if r.definition.Check.ID == id {
		return r.definition.Check, nil
	}
	for _, item := range r.definition.Dependencies {
		if item.ID == id {
			return item, nil
		}
	}
	return core.Check{}, ErrNotFound
}
func (r pinnedNativeCatalog) GetPublishedValidator(_ context.Context, id string) (core.PublishedValidator, error) {
	for _, item := range r.definition.PublishedValidators {
		if item.ID == id {
			return item, nil
		}
	}
	return core.PublishedValidator{}, ErrNotFound
}
func (r pinnedNativeCatalog) GetQueryValidators(_ context.Context, id string) (core.QueryValidators, bool, error) {
	for _, item := range r.definition.QueryValidators {
		if item.QueryID == id {
			return item, true, nil
		}
	}
	return core.QueryValidators{}, false, nil
}
func (r pinnedNativeCatalog) QueryExists(_ context.Context, id string) (bool, error) {
	for _, item := range r.definition.Queries {
		if item.QueryID == id {
			return true, nil
		}
	}
	return false, nil
}

// NativeAcceptedQuery returns an owned exact accepted saved-query snapshot to
// the ordinary query executor, which must still enforce its current policy.
func NativeAcceptedQuery(ctx context.Context, id string) (sessioncontext.DetectiveAcceptedQueryPin, bool) {
	queries, ok := ctx.Value(nativeQueriesKey{}).(map[string]sessioncontext.DetectiveAcceptedQueryPin)
	if !ok {
		return sessioncontext.DetectiveAcceptedQueryPin{}, false
	}
	item, ok := queries[id]
	item.DefinitionJSON = append(json.RawMessage(nil), item.DefinitionJSON...)
	return item, ok
}

type nativeQueriesKey struct{}
