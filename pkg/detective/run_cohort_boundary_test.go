package detective

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/investigation"
)

func TestCaseRunLinkRequiresMatchingDurableCohortProof(t *testing.T) {
	s := testStore(t)
	if err := s.authorizeCaseRunLink(Case{ID: "case-1"}, CheckRun{ID: "standalone"}); err != nil {
		t.Fatalf("standalone run could not link to empty-context Case: %v", err)
	}
	if err := s.authorizeCaseRunLink(Case{ID: "case-1"}, CheckRun{ID: "run-1", CaseID: "case-2"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("run from another Case linked: %v", err)
	}
	if err := s.authorizeCaseRunLink(Case{ID: "case-1"}, CheckRun{ID: "missing", CaseID: "case-1"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("run without durable cohort proof linked: %v", err)
	}
	context := map[string]InputValue{"threshold": {Scalar: ptr(investigation.NewNumberValue(math.NaN()))}}
	if err := s.saveRunCohortProof("run-with-invalid-context", "case-1", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if err := s.authorizeCaseRunLink(Case{ID: "case-1", Context: context}, CheckRun{ID: "run-with-invalid-context", CaseID: "case-1"}); err == nil {
		t.Fatal("unhashable Case context linked to durable run")
	}
}

func TestCaseRunCohortProofRejectsFilesystemSubstitution(t *testing.T) {
	s := testStore(t)
	dir := s.privateItemDir("run-cohorts")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "run-1.json")
	if err := os.Symlink(filepath.Join(s.projectDir, "queries", "q.query.json"), path); err != nil {
		t.Fatal(err)
	}
	if err := s.saveRunCohortProof("run-1", "case-1", strings.Repeat("a", 64)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("symlinked proof was overwritten: %v", err)
	}
	if err := s.authorizeCaseRunLink(Case{ID: "case-1"}, CheckRun{ID: "run-1", CaseID: "case-1"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("symlinked proof authorized run: %v", err)
	}
}
