package gcloudui

import (
	"context"
	"sync"

	"cloud.google.com/go/firestore"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds"
	"github.com/datatug/datatug-cli/pkg/auth/gauth"
	"github.com/datatug/datatug-cli/pkg/schemers"
	"github.com/datatug/datatug-cli/pkg/schemers/firestoreschema"
	"google.golang.org/api/cloudresourcemanager/v3"
)

type GCloudContext struct {
	*clouds.CloudContext
	loadingProjects sync.Mutex
	projects        []*cloudresourcemanager.Project
}

func (v *GCloudContext) GetProjects() (projects []*cloudresourcemanager.Project, err error) {
	if v.projects == nil {
		v.loadingProjects.Lock()
		defer v.loadingProjects.Unlock()
		if v.projects == nil {
			v.projects, err = gauth.GetGCloudProjects(context.Background())
		}
	}
	return v.projects, err
}

var _ clouds.ProjectContext = (*CGProjectContext)(nil)

type CGProjectContext struct {
	*GCloudContext
	Project *cloudresourcemanager.Project
	schema  schemers.Provider
}

func NewProjectContext(ctx *GCloudContext, project *cloudresourcemanager.Project) *CGProjectContext {
	return &CGProjectContext{
		GCloudContext: ctx,
		Project:       project,
		schema: firestoreschema.NewProvider(func(ctx context.Context) (client *firestore.Client, err error) {
			return newFirestoreClient(ctx, project.ProjectId)
		}),
	}
}

func (c CGProjectContext) Schema() schemers.Provider {

	return c.schema
}
