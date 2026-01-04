package dtproject

import (
	"context"
	"fmt"

	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dtconfig"
)

var _ ProjectContext = (*projectContext)(nil)

type projectContext struct {
	context.Context
	tui     *sneatnav.TUI
	config  *dtconfig.ProjectRef
	loader  datatug.ProjectsLoader
	project *datatug.Project
	projErr chan error
}

func (p projectContext) WatchProject() <-chan error {
	return p.projErr
}

func (p projectContext) TUI() *sneatnav.TUI {
	return p.tui
}

func (p projectContext) Config() *dtconfig.ProjectRef {
	return p.config
}

func (p projectContext) Project() *datatug.Project {
	return p.project
}

type ProjectContext interface {
	context.Context
	TUI() *sneatnav.TUI
	Config() *dtconfig.ProjectRef
	Project() *datatug.Project
	WatchProject() <-chan error
}

func NewProjectContext(
	tui *sneatnav.TUI,
	loader datatug.ProjectsLoader,
	config dtconfig.ProjectRef,
) ProjectContext {
	if tui == nil {
		panic("tui cannot be nil")
	}
	if err := config.Validate(); err != nil {
		panic(fmt.Sprintf("invalid project ref: %v", err))
	}
	if loader == nil {
		panic("loader cannot be nil")
	}

	ctx := &projectContext{
		tui:     tui,
		config:  &config,
		loader:  loader,
		projErr: make(chan error, 1),
	}
	go func() {
		project, err := loader.LoadProject(ctx, config.ID)
		if project != nil {
			ctx.project = project
		}
		ctx.projErr <- err
	}()
	return ctx
}
