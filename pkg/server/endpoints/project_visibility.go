package endpoints

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/datatug/datatug-cli/pkg/detective"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

// PrepareLocalVisibilityItems uses the same owning services as their normal
// list endpoints. A registered project or private history record alone never
// grants access to another actor's working context.
func PrepareLocalVisibilityItems(ctx context.Context, scope datatug.ProjectLifecycleScope) (func(context.Context, datatug.ProjectLifecycleScope) ([]datatug.ProjectUsageItem, error), error) {
	generation := api.SecurityContextID()
	if !api.SecureLocalProjectOwner() || api.SecurePrincipalID() != scope.ActorID {
		return nil, sessioncontext.ErrForbidden
	}
	service, principal, _, err := api.ResolveSessionContext(ctx, scope.Storage, scope.Project)
	if err != nil {
		return nil, err
	}
	detective, _, err := detectiveStore(scope.Project, generation)
	if err != nil {
		return nil, err
	}
	// Incident stores are explicit server routes. Qualify IDs so a dedicated
	// store cannot collide with the project's primary repository.
	storeIDs := []string{scope.Project}
	for _, configured := range api.GetCapabilities().IncidentStores {
		if configured.Project == nil || configured.Project.ProjectID == scope.Project {
			storeIDs = append(storeIDs, configured.StoreID)
		}
	}
	return func(ctx context.Context, current datatug.ProjectLifecycleScope) ([]datatug.ProjectUsageItem, error) {
		if current != scope || !api.ValidateSecurityContext(generation) || !api.SecureLocalProjectOwner() || api.SecurePrincipalID() != scope.ActorID {
			return nil, sessioncontext.ErrForbidden
		}
		items, err := detective.VisibilityItems(ctx)
		if err != nil {
			return nil, err
		}
		meanings, err := api.LocalProjectMeaningVisibilityItems(scope.Project)
		if err != nil {
			return nil, err
		}
		items = append(items, meanings...)
		cursor := ""
		for {
			page, err := service.ListSessions(ctx, principal, cursor, 100)
			if err != nil {
				return nil, err
			}
			for _, item := range page.Items {
				items = append(items, datatug.ProjectUsageItem{Ref: datatug.ProjectObjectRef{Kind: "session", ID: item.ID}, Title: item.Title})
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
			store, err := api.IncidentStoreByID(scope.Project, storeID)
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
				view, _, err := api.IncidentView(ctx, candidate, events)
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
				items = append(items, datatug.ProjectUsageItem{Ref: datatug.ProjectObjectRef{Kind: "incident", ID: view.Ref.StoreID + "/" + view.Ref.IncidentID}, Title: view.Title})
				if len(items) > 10000 {
					return nil, datatug.ErrProjectUsageIncomplete
				}
			}
		}
		return items, nil
	}, nil
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
func annotateDetectiveVisibility(project string, state detective.State) (map[string]any, error) {
	root, ok := api.ProjectDir(project)
	if !ok {
		return nil, sessioncontext.ErrForbidden
	}
	hidden, err := filestore.ReadProjectArchivedItems(root)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	var response map[string]any
	if err = json.Unmarshal(body, &response); err != nil {
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
			fields["archived"] = hidden[datatug.ProjectObjectRef{Kind: kind, ID: id}]
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
