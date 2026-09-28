package gauth

import (
	"context"
	"fmt"
	"net/http"

	"google.golang.org/api/cloudresourcemanager/v3"
	"google.golang.org/api/option"
)

var (
	getGoogleCloudClientFn = getGoogleCloudClient
	newCRMServiceFn        = func(ctx context.Context, client *http.Client) (*cloudresourcemanager.Service, error) {
		return cloudresourcemanager.NewService(ctx, option.WithHTTPClient(client))
	}
)

func GetGCloudProjects(ctx context.Context) (projects []*cloudresourcemanager.Project, err error) {
	var client *http.Client
	client, err = getGoogleCloudClientFn(ctx)
	if err != nil {
		err = fmt.Errorf("failed to get HTTP client for Googe Cloud: %v", err)
		return
	}

	// Create Cloud Resource Manager v3 service
	var crmService *cloudresourcemanager.Service
	crmService, err = newCRMServiceFn(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("unable to create Cloud Resource Manager service: %w", err)
	}

	// Search projects (v3): does not require a parent and returns all accessible projects
	req := crmService.Projects.Search()
	if err = req.Pages(ctx, func(page *cloudresourcemanager.SearchProjectsResponse) error {
		projects = append(projects, page.Projects...)
		return nil
	}); err != nil {
		return
	}
	return
}
