package gcloudui

import (
	"context"

	"cloud.google.com/go/firestore"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds"
	"github.com/datatug/datatug-cli/pkg/auth/gauth"
	"github.com/datatug/datatug-cli/pkg/schemers"
	"github.com/datatug/datatug-cli/pkg/schemers/firestoreschema"
	"google.golang.org/api/cloudresourcemanager/v3"
)

// getGCloudProjects is a seam over gauth.GetGCloudProjects.
var getGCloudProjects = gauth.GetGCloudProjects

// GCloudContext is what the Google Cloud screens share. It is immutable once
// created: a screen that needs the projects loads them with loadProjects.
type GCloudContext struct {
	// projects, when not nil, are the projects already known (for example the
	// ones `datatug gcloud projects` fetched), so they are not loaded again.
	projects []*cloudresourcemanager.Project
}

var _ clouds.ProjectContext = (*CGProjectContext)(nil)

// CGProjectContext is a Google Cloud project.
type CGProjectContext struct {
	*GCloudContext
	Project *cloudresourcemanager.Project
	schema  schemers.Provider
}

// NewProjectContext creates the context of a project; its schema reads Firestore.
func NewProjectContext(ctx *GCloudContext, project *cloudresourcemanager.Project) *CGProjectContext {
	return &CGProjectContext{
		GCloudContext: ctx,
		Project:       project,
		schema: firestoreschema.NewProvider(func(ctx context.Context) (client *firestore.Client, err error) {
			return newFirestoreClientFunc(ctx, project.ProjectId)
		}),
	}
}

// Schema returns the schema provider of the project.
func (c CGProjectContext) Schema() schemers.Provider { return c.schema }

// projectID returns the ID of the project, or "" when there is none.
func (c CGProjectContext) projectID() string {
	if c.Project == nil {
		return ""
	}
	return c.Project.ProjectId
}
