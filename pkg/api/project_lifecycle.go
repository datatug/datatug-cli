package api

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/projectvisibility"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
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

// LocalVisibilityInventoryFactory prepares native services before Core takes
// its environment lock. Core invokes the returned callback outside that lock.
type LocalVisibilityInventoryFactory func(context.Context, datatug.ProjectLifecycleScope) (projectvisibility.Runtime, error)

var visibilityInventoryFactory LocalVisibilityInventoryFactory

func ConfigureLocalVisibilityInventory(factory LocalVisibilityInventoryFactory) {
	secureMu.Lock()
	defer secureMu.Unlock()
	visibilityInventoryFactory = factory
}

func localLifecycleStore(ctx context.Context, selection LocalLifecycleSelection, write bool) (datatug.ProjectLifecycleStore, datatug.ProjectLifecycleScope, error) {
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
	scope = datatug.ProjectLifecycleScope{ActorID: authority.ActorID, Storage: selection.StoreID, Project: selection.ProjectID, Branch: selection.Branch, Environment: selection.Environment}
	secureMu.RLock()
	factory := visibilityInventoryFactory
	secureMu.RUnlock()
	if factory != nil {
		native, err := factory(ctx, scope)
		if err != nil {
			return nil, scope, err
		}
		root, ok := ProjectDir(selection.ProjectID)
		if !ok {
			return nil, scope, secureread.ErrAccessDenied
		}
		provider := filestore.NewProjectStoreWithVisibilityItems(selection.ProjectID, root, func(ctx context.Context, scope datatug.ProjectLifecycleScope) ([]datatug.ProjectUsageItem, error) {
			targets, err := native.Inventory(ctx, scope)
			if err != nil {
				return nil, err
			}
			items := make([]datatug.ProjectUsageItem, 0, len(targets))
			for _, target := range targets {
				item := target.Item
				item.Archived = false
				items = append(items, item)
			}
			return items, nil
		}).(datatug.ProjectLifecycleStore)
		private, err := executionstore.ResolvePrivateDir(GetCapabilities().EvidencePrivateDir)
		if err != nil {
			return nil, scope, err
		}
		wrapped, err := projectvisibility.NewStore(provider, scope, private, root, native)
		if err != nil {
			return nil, scope, err
		}
		return wrapped, scope, nil
	}
	provider, ok := store.(datatug.ProjectLifecycleStore)
	if !ok {
		return nil, scope, datatug.ErrProjectUsageIncomplete
	}

	return provider, scope, nil
}

func localProjectLifecycleReview(ctx context.Context, selection LocalLifecycleSelection, object datatug.ProjectObjectRef) (datatug.ProjectLifecycleReview, error) {
	var out datatug.ProjectLifecycleReview
	if err := object.Validate(); err != nil {
		return out, err
	}
	provider, scope, err := localLifecycleStore(ctx, selection, false)
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
	if !out.Complete {
		out.CanDelete = false
	}
	if !out.Complete && !out.ArchiveSupported || !writable {
		out.CanArchive, out.CanRestore = false, false
	}
	if !writable {
		out.CanDelete = false
	}
	return out, nil
}

func localProjectLifecycleChange(ctx context.Context, request LocalLifecycleWrite) (datatug.ProjectLifecycleAck, error) {
	var out datatug.ProjectLifecycleAck
	if err := request.Validate(); err != nil {
		return out, err
	}
	provider, scope, err := localLifecycleStore(ctx, request.LocalLifecycleSelection, true)
	if err != nil {
		return out, err
	}
	// The capability owns fresh reference admission and persistence under one
	// provider boundary. A prior client/server review is never a write grant.
	out, err = provider.ChangeProjectLifecycle(ctx, scope, request.ProjectLifecycleWrite)
	if err != nil {
		if errors.Is(err, projectvisibility.ErrOutcomeUncertain) {
			return datatug.ProjectLifecycleAck{}, errors.Join(ErrProjectMutationOutcomeUncertain, err)
		}
		return datatug.ProjectLifecycleAck{}, err
	}
	if out.OperationID != request.OperationID || out.Change != request.Change || out.Revision == "" || len(out.Revision) > 512 {
		return datatug.ProjectLifecycleAck{}, ErrProjectMutationOutcomeUncertain
	}
	return out, nil
}

func localProjectLifecycleCatalog(ctx context.Context, selection LocalLifecycleSelection, archived bool) (datatug.ProjectLifecycleCatalog, error) {
	provider, scope, err := localLifecycleStore(ctx, selection, false)
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
	// Provider's bool may mean include-archived; this transport returns one
	// exact discovery state while retaining the complete catalog's revision.
	items := make([]datatug.ProjectUsageItem, 0, len(out.Items))
	for _, item := range out.Items {
		if item.Archived == archived {
			items = append(items, item)
		}
	}
	out.Items = items
	return out, nil
}

var lifecycleBranchGuard = withLocalProjectFiles

func guardLocalLifecycleBranch(ctx context.Context, selection LocalLifecycleSelection, write bool, fn func(LocalLifecycleSelection) error) error {
	if selection.ProjectAPI != "" && (selection.ProjectAPI != "local" || selection.Branch == "") || len(selection.Branch) > 256 || strings.TrimSpace(selection.Branch) != selection.Branch || strings.ContainsAny(selection.Branch, "\x00\r\n") {
		return datatug.ErrProjectLifecycleInvalid
	}
	if selection.Branch == "" {
		return fn(selection)
	}
	return lifecycleBranchGuard(ctx, selection.ProjectRef, selection.Branch, write, func(_ *os.Root, _ gitState) error {
		// The host owns branch admission. Visibility belongs to this registered
		// local checkout, while the surrounding guard fences branch/HEAD changes.
		selection.Branch, selection.ProjectAPI = "", ""
		return fn(selection)
	})
}
func LocalProjectLifecycleReview(ctx context.Context, selection LocalLifecycleSelection, object datatug.ProjectObjectRef) (out datatug.ProjectLifecycleReview, err error) {
	err = guardLocalLifecycleBranch(ctx, selection, false, func(selected LocalLifecycleSelection) error {
		out, err = localProjectLifecycleReview(ctx, selected, object)
		return err
	})
	return
}
func LocalProjectLifecycleCatalog(ctx context.Context, selection LocalLifecycleSelection, archived bool) (out datatug.ProjectLifecycleCatalog, err error) {
	err = guardLocalLifecycleBranch(ctx, selection, false, func(selected LocalLifecycleSelection) error {
		out, err = localProjectLifecycleCatalog(ctx, selected, archived)
		return err
	})
	return
}
func LocalProjectLifecycleChange(ctx context.Context, request LocalLifecycleWrite) (out datatug.ProjectLifecycleAck, err error) {
	err = guardLocalLifecycleBranch(ctx, request.LocalLifecycleSelection, true, func(selected LocalLifecycleSelection) error {
		request.LocalLifecycleSelection = selected
		out, err = localProjectLifecycleChange(ctx, request)
		return err
	})
	return
}
