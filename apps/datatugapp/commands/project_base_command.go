package commands

import (
	"errors"
	"fmt"
	"strings"

	"github.com/datatug/datatug-cli/pkg/datatug-core/dtconfig"
	"github.com/datatug/datatug-cli/pkg/datatug-core/storage"
	"github.com/datatug/datatug-cli/pkg/datatug-core/storage/filestore"
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
}

type projectCommandOptions struct {
	projNameRequired, projDirRequired, projNameOrDirRequired bool
}

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
		v.ProjectDir = project.Url
	}

	pathsByID := getProjPathsByID(config)
	v.store, err = filestore.NewStore("local_file_store_from_user_config", pathsByID)
	if err != nil {
		return err
	}

	return nil
}
