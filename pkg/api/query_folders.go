package api

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
)

type QueryFolderCreate struct {
	dto.ProjectRef
	Branch string `json:"branch,omitempty"`
	Path   string `json:"path"`
	Name   string `json:"name"`
}

func (r QueryFolderCreate) Validate() error {
	if r.ProjectRef.Validate() != nil || ValidateProjectIdentifier("project", r.ProjectID) != nil || ValidateIdentifier("name", r.Name) != nil || len(r.Branch) > 256 || strings.TrimSpace(r.Branch) != r.Branch || strings.ContainsAny(r.Branch, "\x00\r\n") {
		return datatug.ErrProjectLifecycleInvalid
	}
	parent := strings.TrimPrefix(r.Path, "~/")
	if parent == "~" {
		parent = ""
	}
	if parent != "" && ValidatePathIdentifier("path", parent) != nil {
		return datatug.ErrProjectLifecycleInvalid
	}
	return nil
}

// General FoldersStore metadata is a separate resource. This bridge uses the
// owning query-directory capability under the existing project/branch lock.
// Limited principals lack a folder policy contract and therefore cannot create.
func CreateProjectQueryFolder(ctx context.Context, r QueryFolderCreate) (*datatug.QueriesFolder, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	secureMu.RLock()
	generation := securityContextID
	allowed := generation != "" && capabilities.AllowWrites && secureSession.LocalProjectOwner && secureSession.Principal != nil && secureSession.Principal.ID != nil
	actor := ""
	if allowed {
		actor = fmt.Sprint(secureSession.Principal.ID)
	}
	secureMu.RUnlock()
	if !allowed {
		return nil, secureread.ErrAccessDenied
	}
	currentAuthority := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		secureMu.RLock()
		defer secureMu.RUnlock()
		if securityContextID != generation || !capabilities.AllowWrites || !secureSession.LocalProjectOwner || secureSession.Principal == nil || secureSession.Principal.ID == nil || fmt.Sprint(secureSession.Principal.ID) != actor {
			return secureread.ErrAccessDenied
		}
		return nil
	}
	parent := strings.TrimPrefix(r.Path, "~/")
	if parent == "~" {
		parent = ""
	}
	err := withLocalProjectFiles(ctx, r.ProjectRef, r.Branch, true, func(root *os.Root, state gitState) error {
		if err := currentAuthority(); err != nil {
			return err
		}
		store, err := projectStoreForID(r.StoreID, r.ProjectID)
		if err != nil {
			return err
		}
		creator, ok := store.(datatug.GuardedQueryFolderDirectories)
		if !ok {
			return datatug.ErrProjectUsageIncomplete
		}
		return creator.CreateQueryFolderGuarded(ctx, parent, r.Name, func() error {
			current, err := localGitStateSeam(ctx, root.Name())
			if err != nil {
				return err
			}
			if current.branch != state.branch || current.head != state.head {
				return dto.ErrBranchHeadConflict
			}
			return currentAuthority()
		})
	})
	if err != nil {
		return nil, err
	}
	return &datatug.QueriesFolder{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: r.Name, Title: r.Name}}, Folders: datatug.QueryFolders{}, Items: datatug.QueryDefs{}}, nil
}

// Folder names use the existing project-metadata read contract, like query IDs
// in all_queries and project_full. This grants no definition writes or execution.
func DeclaredProjectQueryFolders(ctx context.Context, ref dto.ProjectRef) ([]string, error) {
	if err := requireLocalReadPrincipal(); err != nil {
		return nil, err
	}
	store, err := projectStoreForID(ref.StoreID, ref.ProjectID)
	if err != nil {
		return nil, err
	}
	reader, ok := store.(datatug.QueryFolderDirectories)
	if !ok {
		return []string{}, nil
	}
	return reader.DeclaredQueryFolders(ctx)
}
