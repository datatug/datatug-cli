package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/datatug/datatug-cli/pkg/localprojects"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

type ContextualEnvironmentScope struct {
	ProjectID     string `json:"projectId"`
	EnvironmentID string `json:"environmentId"`
	CatalogID     string `json:"catalogId"`
}
type ContextualEnvironmentParent struct {
	Archived bool `json:"archived"`
	ReadOnly bool `json:"readOnly"`
}

// ContextualEnvironments is discovery display metadata, never owning state.
// Available always carries parent/items; unavailable carries only reasonCode.
type ContextualEnvironments struct {
	Version    int                          `json:"version"`
	Status     string                       `json:"status"`
	Scope      ContextualEnvironmentScope   `json:"scope"`
	Parent     *ContextualEnvironmentParent `json:"parent,omitempty"`
	Items      *[]LocalEnvironmentBrief     `json:"items,omitempty"`
	ReasonCode string                       `json:"reasonCode,omitempty"`
}

// BoundContextualEnvironments does not invoke metadata-policy/owner discovery.
// The existing independent source-bound read authority is the only admission.
func BoundContextualEnvironments(ctx context.Context, scope ContextualEnvironmentScope) (ContextualEnvironments, error) {
	out := ContextualEnvironments{Version: 1, Status: "unavailable", Scope: scope, ReasonCode: "TEMPORARILY_UNAVAILABLE"}
	generation := SecurityContextID()
	verify := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		p, e, c, bound, err := BoundReadScope(ctx)
		if err != nil || !bound || p != scope.ProjectID || e != scope.EnvironmentID || c != scope.CatalogID || !ValidateSecurityContext(generation) {
			return secureread.ErrAccessDenied
		}
		return nil
	}
	if err := verify(); err != nil {
		return out, err
	}
	root, err := servedProjectDir(scope.ProjectID)
	if err == nil {
		root, err = filepath.EvalSymlinks(root)
	}
	if err != nil {
		return out, secureread.ErrAccessDenied
	}
	registered, err := nonGitDiscoveryRootStat(root)
	if err != nil || !registered.IsDir() {
		return out, secureread.ErrAccessDenied
	}
	before, gitErr := environmentGitState(ctx, root)
	var gitFailure *EnvironmentError

	var parent bool
	items := make([]LocalEnvironmentBrief, 0, 1)
	readErr := gitErr
	if !errors.As(gitErr, &gitFailure) {
		readErr = environmentReadFilesSeam(ctx, root, func(files *filestore.EnvironmentFiles) error {
			if err := verify(); err != nil {
				return err
			}
			reader, err := openQualifiedProjectReaderSeam(files.Root)
			if err != nil {
				return err
			}
			defer reader.close()
			checkRoot := func() error {
				opened, openErr := reader.root.Stat(".")
				current, pathErr := os.Stat(root)
				if openErr != nil || pathErr != nil || !os.SameFile(registered, opened) || !os.SameFile(registered, current) {
					return secureread.ErrAccessDenied
				}
				return verify()
			}
			if err := checkRoot(); err != nil {
				return err
			}
			// Require the real parent document; registered aliases remain authoritative,
			// just as in localprojects.readProjectState. Never emit opaque metadata.
			var document map[string]json.RawMessage
			if _, err = reader.readJSON("datatug-project.json", &document); err != nil {
				return err
			}
			if document == nil {
				return localprojects.ErrConflict
			}
			parent, err = localprojects.ReadArchivedFromRoot(reader.root)
			if err != nil {
				return err
			}
			archived, err := filestore.ReadProjectArchivedItemsFromRoot(reader.root)
			if err != nil {
				return err
			}
			env, body, err := readQualifiedEnvironment(reader, scope.EnvironmentID)
			if err != nil || body == nil {
				return localprojects.ErrConflict
			}
			presentation, err := parseEnvironmentPresentation(body)
			if err != nil {
				return err
			}
			if !archived[datatug.ProjectObjectRef{Kind: "environment", ID: scope.EnvironmentID}] {
				title := env.Title
				if title == "" {
					title = env.ID
				}
				items = append(items, LocalEnvironmentBrief{ID: env.ID, Title: title, Presentation: presentation})
			}
			return checkRoot()
		})
	}
	// Metadata failures never skip the final authority/root checks, including
	// changes made while either Git probe is in flight.
	verifyRoot := func() error {
		if err := verify(); err != nil {
			return err
		}
		current, pathErr := os.Stat(root)
		if pathErr != nil || !os.SameFile(registered, current) {
			return secureread.ErrAccessDenied
		}
		return nil
	}
	if err := verifyRoot(); err != nil {
		return out, err
	}
	if errors.Is(readErr, secureread.ErrAccessDenied) {
		return out, readErr
	}
	after, afterErr := environmentGitState(ctx, root)
	if err := verifyRoot(); err != nil {
		return out, err
	}
	if readErr != nil || (gitErr == nil) != (afterErr == nil) || gitErr == nil && (before.branch != after.branch || before.head != after.head) || errors.As(afterErr, &gitFailure) {
		return out, nil
	}
	out.Status = "available"
	out.ReasonCode = ""
	out.Parent = &ContextualEnvironmentParent{Archived: parent, ReadOnly: true}
	out.Items = &items
	return out, nil
}
