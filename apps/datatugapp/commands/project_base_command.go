package commands

import (
	"errors"
	"fmt"
	"strings"

	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

type projectDirCommand struct {
	ProjectDir string `short:"d" long:"directory"  required:"false" description:"GetProjectStore directory"`
}

// ProjectBaseCommand defines parameters for show project consoleCommand
type projectBaseCommand struct {
	projectDirCommand
	ProjectName string `short:"p" long:"project"  required:"false" description:"GetProjectStore name"`
	projectID   string
	store       storage.Store

	// FollowProjectLink is --follow-project-link of a command that writes into the project folder
	// it was given: it goes on when the last part of that folder is a link.
	FollowProjectLink bool
}

type projectCommandOptions struct {
	projNameRequired, projDirRequired, projNameOrDirRequired bool

	// writesProject is set by a command that writes into the project folder it was given with
	// -d: it stops when that folder is a link, unless the person said to go on (see
	// checkProjectFolderLink). A command that only reads does not set it.
	writesProject bool
}

// newProjectsStore is a seam over filestore.NewStore, which never fails
// today but whose error return initProjectCommand must still propagate.
// Always filestore.NewStore in production.
var newProjectsStore = filestore.NewStore

func (v *projectBaseCommand) initProjectCommand(o projectCommandOptions) error {
	if o.projNameRequired && v.ProjectName == "" {
		return errors.New("project name parameter is required")
	}
	if o.projDirRequired && v.ProjectDir == "" {
		return errors.New("project name parameter is required")
	}
	if o.projNameOrDirRequired && v.ProjectName == "" && v.ProjectDir == "" {
		return errors.New("either project name or project directory is required")
	}
	if v.ProjectDir != "" && v.projectID == "" {
		if o.writesProject {
			if err := checkProjectFolderLink(v.ProjectDir, v.FollowProjectLink); err != nil {
				return err
			}
		}
		v.store, v.projectID = filestore.NewSingleProjectStore(v.ProjectDir, v.projectID)
		return nil
	}

	config, err := dtconfig.GetSettings()
	if err != nil {
		return fmt.Errorf("failed to get user's DataTug settings: %w", err)
	}

	if v.ProjectName != "" {
		v.projectID = strings.ToLower(v.ProjectName)
		project := config.GetProjectConfig(v.projectID)
		if project == nil {
			return ErrUnknownProjectName
		}
		if project.Path != "" {
			v.ProjectDir = project.Path // locally-added projects store a local Path
		} else {
			v.ProjectDir = project.Url
		}
	}

	pathsByID := getProjPathsByID(config)
	v.store, err = newProjectsStore("local_file_store_from_user_config", pathsByID)
	if err != nil {
		return err
	}

	return nil
}
