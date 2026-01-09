package gcloudui

import (
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/filetug/pkg/sneatv"
)

func NewGoogleCloudBreadcrumbs(cContext *GCloudContext) sneatnav.Breadcrumbs {
	breadcrumbs := dtviewers.GetViewersBreadcrumbs(cContext.TUI)
	breadcrumbs.Push(sneatv.NewBreadcrumb("Google", func() error {
		return goHome(cContext, sneatnav.FocusToContent)
	}))
	return breadcrumbs
}

func newBreadcrumbsProjects(cContext *GCloudContext) sneatnav.Breadcrumbs {
	breadcrumbs := NewGoogleCloudBreadcrumbs(cContext)
	breadcrumbs.Push(sneatv.NewBreadcrumb("Projects", func() error {
		return GoGCloudProjects(cContext, sneatnav.FocusToContent)
	}))
	return breadcrumbs
}
