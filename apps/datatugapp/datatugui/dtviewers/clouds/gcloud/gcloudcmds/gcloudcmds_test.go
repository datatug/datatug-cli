package gcloudcmds

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
	"google.golang.org/api/cloudresourcemanager/v3"
)

// runProjects invokes the gcloud projects subcommand with the provided extra args.
// It overrides the getGCloudProjects seam so no real Google Cloud auth is needed.
func runProjects(t *testing.T, fakeProjects []*cloudresourcemanager.Project, fakeErr error, extraArgs ...string) error {
	t.Helper()

	// Override seams for this test.
	origGet := getGCloudProjects
	origOpen := openGCloudProjectsScreen
	t.Cleanup(func() {
		getGCloudProjects = origGet
		openGCloudProjectsScreen = origOpen
	})

	getGCloudProjects = func(_ context.Context) ([]*cloudresourcemanager.Project, error) {
		return fakeProjects, fakeErr
	}
	openGCloudProjectsScreen = func(_ []*cloudresourcemanager.Project) error {
		return nil
	}

	root := &cli.Command{
		Name:           "datatug",
		Commands:       []*cli.Command{GoogleCloudCommand()},
		ExitErrHandler: func(_ context.Context, _ *cli.Command, _ error) {},
	}
	argv := append([]string{"datatug", "gcloud", "projects"}, extraArgs...)
	return root.Run(context.Background(), argv)
}

// sampleProjects returns a small slice of fake projects for table-driven tests.
func sampleProjects() []*cloudresourcemanager.Project {
	return []*cloudresourcemanager.Project{
		{ProjectId: "proj-1", DisplayName: "Project One", State: "ACTIVE", Name: "projects/123456789"},
		{ProjectId: "proj-2", DisplayName: "Project Two", State: "ACTIVE", Name: "projects/987654321"},
	}
}

// --- GoogleCloudCommand shape ---

func TestGoogleCloudCommand_Name(t *testing.T) {
	cmd := GoogleCloudCommand()
	assert.Equal(t, "gcloud", cmd.Name)
}

func TestGoogleCloudCommand_HasLoginAndProjects(t *testing.T) {
	cmd := GoogleCloudCommand()
	require.Len(t, cmd.Commands, 2)
	names := []string{cmd.Commands[0].Name, cmd.Commands[1].Name}
	assert.Contains(t, names, "login")
	assert.Contains(t, names, "projects")
}

// --- loginCommand ---

func TestLoginCommand_Name(t *testing.T) {
	cmd := loginCommand()
	assert.Equal(t, "login", cmd.Name)
}

func TestLoginCommand_ActionReturnsNil(t *testing.T) {
	cmd := loginCommand()
	require.NotNil(t, cmd.Action)
	err := cmd.Action(context.Background(), cmd)
	assert.NoError(t, err)
}

// --- projectsCommand ---

func TestProjectsCommand_Name(t *testing.T) {
	cmd := projectsCommand()
	assert.Equal(t, "projects", cmd.Name)
}

func TestProjectsCommand_HasFormatFlag(t *testing.T) {
	cmd := projectsCommand()
	var found bool
	for _, f := range cmd.Flags {
		if f.Names()[0] == "format" {
			found = true
		}
	}
	assert.True(t, found, "expected --format flag")
}

func TestProjectsCommand_GetProjectsError(t *testing.T) {
	wantErr := errors.New("auth failed")
	err := runProjects(t, nil, wantErr)
	assert.ErrorIs(t, err, wantErr)
}

func TestProjectsCommand_FormatID(t *testing.T) {
	err := runProjects(t, sampleProjects(), nil, "--format", "id")
	assert.NoError(t, err)
}

func TestProjectsCommand_FormatJSON(t *testing.T) {
	err := runProjects(t, sampleProjects(), nil, "--format", "json")
	assert.NoError(t, err)
}

func TestProjectsCommand_FormatCSV(t *testing.T) {
	err := runProjects(t, sampleProjects(), nil, "--format", "csv")
	assert.NoError(t, err)
}

func TestProjectsCommand_FormatEmpty_OpensScreen(t *testing.T) {
	// Override openGCloudProjectsScreen separately to assert it is called.
	origGet := getGCloudProjects
	origOpen := openGCloudProjectsScreen
	t.Cleanup(func() {
		getGCloudProjects = origGet
		openGCloudProjectsScreen = origOpen
	})

	called := false
	getGCloudProjects = func(_ context.Context) ([]*cloudresourcemanager.Project, error) {
		return sampleProjects(), nil
	}
	openGCloudProjectsScreen = func(_ []*cloudresourcemanager.Project) error {
		called = true
		return nil
	}

	root := &cli.Command{
		Name:           "datatug",
		Commands:       []*cli.Command{GoogleCloudCommand()},
		ExitErrHandler: func(_ context.Context, _ *cli.Command, _ error) {},
	}
	err := root.Run(context.Background(), []string{"datatug", "gcloud", "projects", "--format", ""})
	assert.NoError(t, err)
	assert.True(t, called, "openGCloudProjectsScreen should have been called")
}

func TestProjectsCommand_FormatInvalid_ReturnsError(t *testing.T) {
	err := runProjects(t, sampleProjects(), nil, "--format", "xml")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid flag")
}
