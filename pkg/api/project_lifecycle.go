package api

import (
	"context"
	"strings"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
)

type LocalLifecycleSelection struct {
	dto.ProjectRef
	Branch      string `json:"branch,omitempty"`
	ProjectAPI  string `json:"projectApi,omitempty"`
	Environment string `json:"environment,omitempty"`
}

type LocalLifecycleWrite struct {
	LocalLifecycleSelection
	datatug.ProjectLifecycleWrite
}

var lifecycleMutationScope = SecureProjectMutationScope

func localLifecycleStore(selection LocalLifecycleSelection, write bool) (datatug.ProjectLifecycleStore, datatug.ProjectLifecycleScope, error) {
	var scope datatug.ProjectLifecycleScope
	if err := requireLocalReadPrincipal(); err != nil {
		return nil, scope, err
	}
	if err := selection.Validate(); err != nil {
		return nil, scope, err
	}
	if err := ValidateProjectIdentifier("project", selection.ProjectID); err != nil {
		return nil, scope, err
	}
	if selection.StoreID != LocalStoreID {
		return nil, scope, ErrUnknownStoreID
	}
	if len(selection.Branch) > 256 || strings.TrimSpace(selection.Branch) != selection.Branch || strings.ContainsAny(selection.Branch, "\x00\r\n") ||
		len(selection.Environment) > 128 || strings.TrimSpace(selection.Environment) != selection.Environment || strings.ContainsAny(selection.Environment, "/\\\x00\r\n") || selection.Environment == "." || selection.Environment == ".." ||
		(selection.ProjectAPI != "" && (selection.ProjectAPI != "local" || selection.Branch == "")) {
		return nil, scope, datatug.ErrProjectLifecycleInvalid
	}
	secureMu.RLock()
	unrestricted, writable := secureSession.LocalProjectOwner, capabilities.AllowWrites
	secureMu.RUnlock()
	// Limited principals must not gain access to private retained history or
	// bypass their definition policies through the aggregate lifecycle API.
	if !unrestricted || write && !writable {
		return nil, scope, secureread.ErrAccessDenied
	}
	authority, err := lifecycleMutationScope(selection.ProjectRef, selection.Branch, "project-lifecycle")
	if err != nil {
		return nil, scope, secureread.ErrAccessDenied
	}
	store, err := projectStoreForID(selection.StoreID, selection.ProjectID)
	if err != nil {
		return nil, scope, err
	}
	provider, ok := store.(datatug.ProjectLifecycleStore)
	if !ok {
		return nil, scope, datatug.ErrProjectUsageIncomplete
	}
	scope = datatug.ProjectLifecycleScope{ActorID: authority.ActorID, Storage: selection.StoreID, Project: selection.ProjectID, Branch: selection.Branch, Environment: selection.Environment}
	return provider, scope, nil
}

func LocalProjectLifecycleReview(ctx context.Context, selection LocalLifecycleSelection, object datatug.ProjectObjectRef) (datatug.ProjectLifecycleReview, error) {
	var out datatug.ProjectLifecycleReview
	if err := object.Validate(); err != nil {
		return out, err
	}
	provider, scope, err := localLifecycleStore(selection, false)
	if err != nil {
		return out, err
	}
	out, err = provider.ReviewProjectLifecycle(ctx, scope, object)
	if err != nil {
		return datatug.ProjectLifecycleReview{}, err
	}
	if out.Target.Ref != object || out.Revision == "" || len(out.Revision) > 512 || len(out.Incoming) > 10000 {
		return datatug.ProjectLifecycleReview{}, datatug.ErrProjectUsageIncomplete
	}
	secureMu.RLock()
	writable := capabilities.AllowWrites
	secureMu.RUnlock()
	if !out.Complete || !writable {
		out.CanArchive, out.CanDelete, out.CanRestore = false, false, false
	}
	return out, nil
}

func LocalProjectLifecycleChange(ctx context.Context, request LocalLifecycleWrite) (datatug.ProjectLifecycleAck, error) {
	var out datatug.ProjectLifecycleAck
	if err := request.Validate(); err != nil {
		return out, err
	}
	provider, scope, err := localLifecycleStore(request.LocalLifecycleSelection, true)
	if err != nil {
		return out, err
	}
	// The capability owns fresh reference admission and persistence under one
	// provider boundary. A prior client/server review is never a write grant.
	out, err = provider.ChangeProjectLifecycle(ctx, scope, request.ProjectLifecycleWrite)
	if err != nil {
		return datatug.ProjectLifecycleAck{}, err
	}
	if out.OperationID != request.OperationID || out.Change != request.Change || out.Revision == "" || len(out.Revision) > 512 {
		return datatug.ProjectLifecycleAck{}, ErrProjectMutationOutcomeUncertain
	}
	return out, nil
}

func LocalProjectLifecycleCatalog(ctx context.Context, selection LocalLifecycleSelection, archived bool) (datatug.ProjectLifecycleCatalog, error) {
	provider, scope, err := localLifecycleStore(selection, false)
	if err != nil {
		return datatug.ProjectLifecycleCatalog{}, err
	}
	out, err := provider.ListProjectLifecycle(ctx, scope, archived)
	if err != nil {
		return datatug.ProjectLifecycleCatalog{}, err
	}
	if out.Revision == "" || len(out.Revision) > 512 || len(out.Items) > 10000 {
		return datatug.ProjectLifecycleCatalog{}, datatug.ErrProjectUsageIncomplete
	}
	return out, nil
}
