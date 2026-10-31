package endpoints

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/datatug/datatug-cli/pkg/projectvisibility"
	"strconv"
	"strings"

	detectivePackage "github.com/datatug/datatug-cli/pkg/detective"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

// Keep transport codec failures deterministic without changing owning state.
var marshalVisibilityProjection = json.Marshal

// Owning-service IO ports keep pagination, persistence and recovery failures
// testable after the unchanged principal/scope admission above each call.
var visibilityListSessions = (*sessioncontext.Service).ListSessions
var visibilityExecuteSession = (*sessioncontext.Service).Execute
var visibilityReadOperation = (*sessioncontext.Service).ReadOperation
var visibilityIncidentStoreByID = api.IncidentStoreByID
var visibilityIncidentView = api.IncidentView
var visibilityAcceptedSessionContext = api.ResolveAcceptedSessionContext

// Session lifecycle identity is bound to the real private repository mount,
// while the native service continues to receive only its stored record ID.
func localVisibilitySessionNamespace(ctx context.Context, project string, expected sessioncontext.Principal) (sessioncontext.NativeNamespace, error) {
	_, principal, repository, err := visibilityAcceptedSessionContext(ctx, "local", project)
	if err != nil {
		return sessioncontext.NativeNamespace{}, err
	}
	if repository == nil || principal != expected {
		return sessioncontext.NativeNamespace{}, sessioncontext.ErrForbidden
	}
	return repository.NativeNamespace(), nil
}

func filterLocalVisibleSessions(ctx context.Context, project string, principal sessioncontext.Principal, items []sessioncontext.Session) ([]sessioncontext.Session, error) {
	namespace, err := localVisibilitySessionNamespace(ctx, project, principal)
	if err != nil {
		return nil, err
	}
	identities := make(map[string]string, len(items))
	for _, item := range items {
		id, err := sessioncontext.SessionLifecycleID(namespace, item.ID)
		if err != nil {
			return nil, err
		}
		identities[item.ID] = id
	}
	return filterLocalVisible(project, "session", items, func(item sessioncontext.Session) string { return identities[item.ID] })
}

// PrepareLocalVisibilityItems uses the same owning services as their normal
// list endpoints. A registered project or private history record alone never
// grants access to another actor's working context.
func PrepareLocalVisibilityItems(ctx context.Context, scope datatug.ProjectLifecycleScope) (projectvisibility.Runtime, error) {
	generation := api.SecurityContextID()
	if !api.SecureLocalProjectOwner() || api.SecurePrincipalID() != scope.ActorID {
		return projectvisibility.Runtime{}, sessioncontext.ErrForbidden
	}
	service, principal, _, err := api.ResolveSessionContext(ctx, scope.Storage, scope.Project)
	if err != nil {
		return projectvisibility.Runtime{}, err
	}
	namespace, err := localVisibilitySessionNamespace(ctx, scope.Project, principal)
	if err != nil {
		return projectvisibility.Runtime{}, err
	}
	currentNamespace := func(ctx context.Context) error {
		current, err := localVisibilitySessionNamespace(ctx, scope.Project, principal)
		if err != nil {
			return err
		}
		if current != namespace {
			return sessioncontext.ErrForbidden
		}
		return nil
	}
	detective, _, err := detectiveStore(scope.Project, generation)
	if err != nil {
		return projectvisibility.Runtime{}, err
	}
	// Incident stores are explicit server routes. Qualify IDs so a dedicated
	// store cannot collide with the project's primary repository.
	storeIDs := []string{scope.Project}
	for _, configured := range api.GetCapabilities().IncidentStores {
		if configured.Project == nil || configured.Project.ProjectID == scope.Project {
			storeIDs = append(storeIDs, configured.StoreID)
		}
	}
	inventory := func(ctx context.Context, current datatug.ProjectLifecycleScope) ([]projectvisibility.Target, error) {
		if current != scope || !api.ValidateSecurityContext(generation) || !api.SecureLocalProjectOwner() || api.SecurePrincipalID() != scope.ActorID {
			return nil, sessioncontext.ErrForbidden
		}
		if err := currentNamespace(ctx); err != nil {
			return nil, err
		}
		definitions, err := detective.VisibilityItems(ctx)
		if err != nil {
			return nil, err
		}
		items := make([]projectvisibility.Target, 0, len(definitions))
		for _, item := range definitions {
			items = append(items, projectvisibility.Target{Item: item.ProjectUsageItem, Revision: item.Revision})
		}
		meanings, err := api.LocalProjectMeaningVisibilityItems(scope.Project)
		if err != nil {
			return nil, err
		}
		for _, item := range meanings {
			items = append(items, projectvisibility.Target{Item: item, Revision: "meaning"})
		}
		cursor := ""
		for {
			page, err := visibilityListSessions(service, ctx, principal, cursor, 100)
			if err != nil {
				return nil, err
			}
			for _, item := range page.Items {
				id, err := sessioncontext.SessionLifecycleID(namespace, item.ID)
				if err != nil {
					return nil, err
				}
				items = append(items, projectvisibility.Target{Item: datatug.ProjectUsageItem{Ref: datatug.ProjectObjectRef{Kind: "session", ID: id}, Title: item.Title, Archived: item.State == "archived"}, Revision: strconv.FormatInt(item.Version, 10)})
			}
			if len(items) > 10000 {
				return nil, datatug.ErrProjectUsageIncomplete
			}
			if !page.HasMore {
				break
			}
			if page.NextCursor == "" || page.NextCursor == cursor {
				return nil, datatug.ErrProjectUsageIncomplete
			}
			cursor = page.NextCursor
		}
		seen := map[string]bool{}
		for _, storeID := range storeIDs {
			if seen[storeID] {
				continue
			}
			seen[storeID] = true
			store, err := visibilityIncidentStoreByID(scope.Project, storeID)
			if err != nil {
				return nil, err
			}
			candidates, err := store.List(ctx, incidents.CandidateListQuery{})
			if err != nil {
				return nil, err
			}
			for _, candidate := range candidates {
				events, err := store.Events(ctx, candidate.Ref, 0)
				if err != nil {
					return nil, err
				}
				view, _, err := visibilityIncidentView(ctx, candidate, events)
				if err != nil {
					return nil, err
				}
				belongs := false
				for _, project := range view.Projects {
					if project.StoreID == scope.Storage && project.ProjectID == scope.Project && (scope.Environment == "" || project.Environment == scope.Environment) {
						belongs = true
						break
					}
				}
				if !belongs || view.WorkItemID != "" {
					continue
				}
				items = append(items, projectvisibility.Target{Item: datatug.ProjectUsageItem{Ref: datatug.ProjectObjectRef{Kind: "incident", ID: view.Ref.StoreID + "/" + view.Ref.IncidentID}, Title: view.Title}, Revision: strconv.FormatUint(view.LastSeq, 10)})
				if len(items) > 10000 {
					return nil, datatug.ErrProjectUsageIncomplete
				}
			}
		}
		return items, nil
	}
	authorize := func(ctx context.Context, current datatug.ProjectLifecycleScope, write bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if current != scope || !api.ValidateSecurityContext(generation) || !api.SecureLocalProjectOwner() || api.SecurePrincipalID() != scope.ActorID || write && !api.GetCapabilities().AllowWrites {
			return sessioncontext.ErrForbidden
		}
		return currentNamespace(ctx)
	}

	restore := func(ctx context.Context, current datatug.ProjectLifecycleScope, target projectvisibility.Target, operation string) (projectvisibility.Target, error) {
		if err := authorize(ctx, current, true); err != nil {
			return projectvisibility.Target{}, err
		}
		version, err := strconv.ParseInt(target.Revision, 10, 64)
		if err != nil || version < 1 {
			return projectvisibility.Target{}, datatug.ErrProjectUsageIncomplete
		}
		expected := target
		expected.Item.Archived = false
		switch target.Item.Ref.Kind {
		case "session":
			_, sessionID, qualified := strings.Cut(target.Item.Ref.ID, "/")
			canonical, identityErr := sessioncontext.SessionLifecycleID(namespace, sessionID)
			if !qualified || identityErr != nil || canonical != target.Item.Ref.ID {
				return projectvisibility.Target{}, sessioncontext.ErrForbidden
			}
			result, err := visibilityExecuteSession(service, ctx, principal, sessioncontext.Command{OpID: operation, Action: "transitionSession", SessionID: sessionID, DatabaseID: principal.DatabaseID, ProjectID: principal.ProjectID, ExpectedVersion: version, Transition: "restore"})
			receipt := result.Receipt
			if err != nil {
				if !errors.Is(err, sessioncontext.ErrConflict) && !errors.Is(err, sessioncontext.ErrInvalid) && !errors.Is(err, sessioncontext.ErrNotFound) {
					return projectvisibility.Target{}, err
				}
				if authErr := authorize(ctx, current, true); authErr != nil {
					return projectvisibility.Target{}, authErr
				}
				recovered, readErr := visibilityReadOperation(service, ctx, principal, operation)
				if errors.Is(readErr, sessioncontext.ErrNotFound) {
					return projectvisibility.Target{}, projectvisibility.ErrRestoreConflict
				}
				if readErr != nil {
					return projectvisibility.Target{}, readErr
				}
				receipt = recovered
			}
			if receipt.Actor != principal.Actor || receipt.OpID != operation || receipt.Action != "transitionSession" || receipt.SessionID != sessionID || receipt.SessionVersion != version+1 {
				return projectvisibility.Target{}, projectvisibility.ErrRestoreConflict
			}
			expected.Revision = strconv.FormatInt(receipt.SessionVersion, 10)
			return expected, nil
		case "case":
			restored, err := detective.RestoreVisibilityCase(ctx, target.Item.Ref.ID, current.ActorID, operation, version)
			if errors.Is(err, detectivePackage.ErrVisibilityRestoreConflict) {
				return projectvisibility.Target{}, projectvisibility.ErrRestoreConflict
			}
			if err != nil {
				return projectvisibility.Target{}, err
			}
			expected.Revision = strconv.FormatInt(restored.Version, 10)
			return expected, nil
		default:
			return projectvisibility.Target{}, datatug.ErrProjectUsageIncomplete
		}
	}

	return projectvisibility.Runtime{Inventory: inventory, Restore: restore, Authorize: authorize}, nil
}

func localVisibilityHidden(project, kind, id string) (bool, error) {
	root, ok := api.ProjectDir(project)
	if !ok {
		return false, sessioncontext.ErrForbidden
	}
	return filestore.ProjectItemArchived(root, datatug.ProjectObjectRef{Kind: kind, ID: id})
}

// Filtering changes discovery only; direct endpoints and saved references use
// the owning stores with their existing authorization.
func filterLocalVisible[T any](project, kind string, items []T, id func(T) string) ([]T, error) {
	root, ok := api.ProjectDir(project)
	if !ok {
		return nil, sessioncontext.ErrForbidden
	}
	hiddenItems, err := filestore.ReadProjectArchivedItems(root)
	if err != nil {
		return nil, err
	}
	visible := make([]T, 0, len(items))
	for _, item := range items {
		hidden := hiddenItems[datatug.ProjectObjectRef{Kind: kind, ID: id(item)}]
		if !hidden {
			visible = append(visible, item)
		}
	}
	return visible, nil
}
func annotateDetectiveVisibility(project string, state detectivePackage.State) (map[string]any, error) {
	root, ok := api.ProjectDir(project)
	if !ok {
		return nil, sessioncontext.ErrForbidden
	}
	hidden, err := filestore.ReadProjectArchivedItems(root)
	if err != nil {
		return nil, err
	}
	body, err := marshalVisibilityProjection(state)
	if err != nil {
		return nil, err
	}
	response, err := decodeVisibilityProjection(body)
	if err != nil {
		return nil, err
	}
	kinds := map[string]string{"checks": "check", "validators": "validator", "queryValidators": "query-validator", "scenarios": "scenario", "cases": "case"}
	for collection, kind := range kinds {
		items, _ := response[collection].([]any)
		for _, item := range items {
			fields, ok := item.(map[string]any)
			if !ok {
				continue
			}
			id, _ := fields["id"].(string)
			if kind == "query-validator" {
				id, _ = fields["queryId"].(string)
			}
			fields["archived"] = hidden[datatug.ProjectObjectRef{Kind: kind, ID: id}] || kind == "case" && fields["status"] == "archived"
		}
	}
	return response, nil
}
func filterLocalQueryVisibility(project string, folder *datatug.QueriesFolder, prefix string) error {
	root, ok := api.ProjectDir(project)
	if !ok {
		return sessioncontext.ErrForbidden
	}
	hidden, err := filestore.ReadProjectArchivedItems(root)
	if err != nil {
		return err
	}
	var filter func(*datatug.QueriesFolder, string)
	filter = func(folder *datatug.QueriesFolder, prefix string) {
		if folder == nil {
			return
		}
		visible := folder.Items[:0]
		for _, item := range folder.Items {
			id := strings.TrimPrefix(prefix+"/"+item.ID, "/")
			if !hidden[datatug.ProjectObjectRef{Kind: "query", ID: id}] {
				visible = append(visible, item)
			}
		}
		folder.Items = visible
		children := folder.Folders[:0]
		for _, child := range folder.Folders {
			id := strings.TrimPrefix(prefix+"/"+child.ID, "/")
			if hidden[datatug.ProjectObjectRef{Kind: "query-folder", ID: id}] {
				continue
			}
			filter(child, id)
			children = append(children, child)
		}
		folder.Folders = children
	}
	filter(folder, prefix)
	return nil
}

func decodeVisibilityProjection(body []byte) (map[string]any, error) {
	var response map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&response); err != nil {
		return nil, err
	}
	return response, nil
}
