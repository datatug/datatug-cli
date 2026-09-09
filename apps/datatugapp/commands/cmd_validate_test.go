package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
)

// TestQueryDefTarget_Validate_RejectsEmbeddedCredentials proves datatug-core
// v0.17.0 is actually live (not the pre-migration vendored model, which had
// no QueryDefTarget.Validate() or QueryTypeDTQL at all): the project's
// Validate() now runs the new query-target credential check, matching
// `datatug validate`'s semantics (validateProject calls project.Validate()).
func TestQueryDefTarget_Validate_RejectsEmbeddedCredentials(t *testing.T) {
	project := &datatug.Project{
		ProjectItem: datatug.ProjectItem{
			ProjItemBrief: datatug.ProjItemBrief{ID: "proj1"},
			Access:        "private",
		},
		Created: &datatug.ProjectCreated{},
		Queries: &datatug.QueriesFolder{
			Items: datatug.QueryDefs{
				{
					ProjectItem: datatug.ProjectItem{
						ProjItemBrief: datatug.ProjItemBrief{ID: "q1", Title: "Query 1"},
					},
					Type: datatug.QueryTypeDTQL,
					Targets: []datatug.QueryDefTarget{
						{Driver: "postgres", Credentials: datatug.Credentials{Password: "hunter2"}},
					},
				},
			},
		},
	}

	err := project.Validate()
	if err == nil {
		t.Fatal("expected project.Validate() to reject a query target with an embedded password, got nil")
	}
	if got := err.Error(); !strings.Contains(got, "password") {
		t.Errorf("expected error to mention the credential field, got: %v", got)
	}
}

// TestQueryDefTarget_Validate_AcceptsCredentialFreeDTQLQuery proves a
// QueryTypeDTQL query (new in v0.17.0 - the pre-migration model had no DTQL
// query type at all) with no embedded credentials passes validation.
func TestQueryDefTarget_Validate_AcceptsCredentialFreeDTQLQuery(t *testing.T) {
	project := &datatug.Project{
		ProjectItem: datatug.ProjectItem{
			ProjItemBrief: datatug.ProjItemBrief{ID: "proj1"},
			Access:        "private",
		},
		Created: &datatug.ProjectCreated{},
		Queries: &datatug.QueriesFolder{
			Items: datatug.QueryDefs{
				{
					ProjectItem: datatug.ProjectItem{
						ProjItemBrief: datatug.ProjItemBrief{ID: "q1", Title: "Query 1"},
					},
					Type:    datatug.QueryTypeDTQL,
					Targets: []datatug.QueryDefTarget{{Driver: "postgres", Catalog: "mydb"}},
				},
			},
		},
	}

	if err := project.Validate(); err != nil {
		t.Fatalf("expected a credential-free DTQL query to validate, got: %v", err)
	}
}

// TestValidateProject_RealDemoProject exercises the exact path `datatug
// validate` uses (validateProject: initProjectCommand -> LoadProject ->
// Validate) against the real, committed demo project, proving the
// datatug-core v0.17.0 module loads and validates it end to end. Skips off
// this VM (no sibling datatug-demo-projects checkout, e.g. CI).
func TestValidateProject_RealDemoProject(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("cannot resolve home dir: %v", err)
	}
	dir := filepath.Join(home, "projects", "datatug", "datatug-demo-projects", "demo-project-1")
	if _, err := os.Stat(filepath.Join(dir, "datatug-project.json")); err != nil {
		t.Skipf("demo project fixture not present at %s (expected on the dev VM, not in CI): %v", dir, err)
	}

	if err := validateProject(dir); err != nil {
		t.Fatalf("validateProject(%s): %v", dir, err)
	}
}
