package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

var nonGitDiscoveryRootStat = os.Stat

// A branchless discovery read is admitted only for an actually non-Git
// registered project. It shares environment-state's read policy and session
// fences, never the owner-only private lifecycle inventory.
func localNonGitProjectEnvironments(ctx context.Context, ref dto.ProjectRef) (out []LocalEnvironmentBrief, err error) {
	if err = ref.Validate(); err != nil {
		return nil, err
	}
	generation := SecurityContextID()
	verifyCurrent := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !ValidateSecurityContext(generation) {
			return environmentError("ACCESS_DENIED", 403)
		}
		return nil
	}
	if err = verifyCurrent(); err != nil {
		return nil, err
	}
	// Snapshot only a server-registered physical root, after the same metadata
	// policy admits the read. This identity must predate the directory lock:
	// taking it in the callback could accept a replacement inode after locking.
	_, readCapabilities, _, _, err := envPolicy(ref.ProjectID)
	if err != nil {
		return nil, err
	}
	if !readCapabilities.CanReadOrder {
		return nil, environmentError("ACCESS_DENIED", 403)
	}
	root, err := servedProjectDir(ref.ProjectID)
	if err == nil {
		root, err = filepath.EvalSymlinks(root)
	}
	var registered os.FileInfo
	if err == nil {
		registered, err = nonGitDiscoveryRootStat(root)
	}
	if err != nil || !registered.IsDir() {
		return nil, environmentError("PROJECT_NOT_FOUND", 404)
	}
	scope := dto.EnvironmentScope{StoreID: ref.StoreID, ProjectID: ref.ProjectID}
	err = withLocalEnvironment(ctx, scope, false, func(files *filestore.EnvironmentFiles, resolved dto.EnvironmentScope, _ string, _ dto.EnvironmentCapabilities, _ string) error {
		// Keep the authority captured before filesystem preflight. A later
		// session must not become this read's initial authority or open its root.
		if err := verifyCurrent(); err != nil {
			return err
		}
		if resolved.ResolvedBranch != nil {
			return errors.New("branch is required for a qualified project read")
		}
		reader, err := openQualifiedProjectReaderSeam(files.Root)
		if err != nil {
			return err
		}
		defer reader.close()
		// An operator may replace the registered path while it is being read.
		// Keep the anchored reader and reject a presentation from a rebound root.
		verifyBound := func() error {
			if err := verifyCurrent(); err != nil {
				return err
			}
			opened, openErr := reader.root.Stat(".")
			current, pathErr := os.Stat(files.Root)
			if openErr != nil || pathErr != nil || !os.SameFile(registered, opened) || !os.SameFile(registered, current) {
				return environmentError("ACCESS_DENIED", 403)
			}
			return nil
		}
		if err := verifyBound(); err != nil {
			return err
		}
		out, err = readActiveEnvironmentBriefs(reader)
		if err != nil {
			return err
		}
		// A repository can be initialized while this read waits for files. The
		// outer state helper fences existing Git only; verify non-Git again
		// before publishing so this route cannot bypass branch admission.
		_, gitErr := environmentGitState(ctx, files.Root)
		if gitErr == nil {
			return errors.New("branch is required for a qualified project read")
		}
		var gitFailure *EnvironmentError
		if errors.As(gitErr, &gitFailure) {
			return gitErr
		}
		return verifyBound()
	})
	if err == nil {
		err = verifyCurrent()
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Call while holding the environment read lock so definitions and visibility
// form one discovery projection. Direct environment loads remain complete.
func readActiveEnvironmentBriefs(reader *qualifiedProjectReader) ([]LocalEnvironmentBrief, error) {
	environments, err := readQualifiedEnvironments(reader)
	if err != nil {
		return nil, err
	}
	archived, err := filestore.ReadProjectArchivedItems(reader.root.Name())
	if err != nil {
		return nil, err
	}
	briefs := make([]LocalEnvironmentBrief, 0, len(environments))
	for _, environment := range environments {
		env := environment.env
		if archived[datatug.ProjectObjectRef{Kind: "environment", ID: env.ID}] {
			continue
		}
		title := env.Title
		if title == "" {
			title = env.ID
		}
		briefs = append(briefs, LocalEnvironmentBrief{ID: env.ID, Title: title, Presentation: environment.presentation})
	}
	return briefs, nil
}
