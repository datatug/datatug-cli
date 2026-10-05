package api

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/internal/sourcecases"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/strongo/validation"
)

// An environment or a catalog ID that a client sends and that reaches a file path
// must be a plain name. The tests below send every text of sourcecases.UnsafeIdentifiers
// to the places of this package that turn a client's ID into a path, and count
// the reads: a refusal comes before the first one.

func TestValidateIdentifier(t *testing.T) {
	for _, id := range []string{"local", "chinook-local", "UAT", "a.b_c-d", "Chinook1", "données", strings.Repeat("a", 128)} {
		if err := ValidateIdentifier("environment", id); err != nil {
			t.Errorf("ValidateIdentifier(%q) = %v, want a plain name accepted", id, err)
		}
	}
	for _, c := range sourcecases.UnsafeIdentifiers() {
		err := ValidateIdentifier("catalog", c.ID)
		if err == nil || !validation.IsBadRequestError(err) || !validation.IsBadFieldValueError(err) {
			t.Errorf("%s: ValidateIdentifier(%q) = %v, want a bad-request field error", c.Name, c.ID, err)
			continue
		}
		if !strings.Contains(err.Error(), "[catalog]") {
			t.Errorf("%s: the error does not name the field: %v", c.Name, err)
		}
		if len(c.ID) >= 3 && strings.Contains(err.Error(), c.ID) {
			t.Errorf("%s: the error echoes the ID: %v", c.Name, err)
		}
	}
	// "<source id not shown>" is what SourceIDDisplay returns for an ID that is not
	// a plain name, so it must not pass for one.
	if err := ValidateIdentifier("environment", "<source id not shown>"); err == nil {
		t.Error("the text SourceIDDisplay puts in place of an ID is not a plain name")
	}
}

// countReads replaces the catalog file read with a counter for one test.
func countReads(t *testing.T) *int {
	t.Helper()
	reads := 0
	previous := readCatalogFile
	readCatalogFile = func(name string) ([]byte, error) {
		reads++
		return nil, os.ErrNotExist
	}
	t.Cleanup(func() { readCatalogFile = previous })
	return &reads
}

func TestGetCatalogTables_RefusesAnUnsafeIDWithoutReadingAnyFile(t *testing.T) {
	reads := countReads(t)
	dir := t.TempDir()

	// The counter is live: a plain pair reaches the read.
	if _, err := GetCatalogTables(dir, "local", "chinook-local"); !errors.Is(err, ErrCatalogNotFound) || *reads != 1 {
		t.Fatalf("plain IDs: err = %v, reads = %d, want ErrCatalogNotFound after 1 read", err, *reads)
	}
	*reads = 0

	for _, c := range sourcecases.UnsafeIdentifiers() {
		for position, ids := range map[string][2]string{
			"environment": {c.ID, "chinook-local"},
			"catalog":     {"local", c.ID},
			"both":        {c.ID, c.ID},
		} {
			_, err := GetCatalogTables(dir, ids[0], ids[1])
			if err == nil || !validation.IsBadRequestError(err) || errors.Is(err, ErrCatalogNotFound) {
				t.Errorf("%s in %s: GetCatalogTables = %v, want a bad-request refusal", c.Name, position, err)
			}
			if len(c.ID) >= 3 && err != nil && strings.Contains(err.Error(), c.ID) {
				t.Errorf("%s in %s: the refusal echoes the ID: %v", c.Name, position, err)
			}
		}
	}
	if *reads != 0 {
		t.Fatalf("an unsafe ID reached %d file reads, want 0", *reads)
	}
}

// A project store joins an environment ID into a path of its own, so the ID is
// checked before the store is asked.
func TestGetEnvironmentSummary_RefusesAnUnsafeEnvironmentBeforeTheStore(t *testing.T) {
	origNewStore := storage.NewDatatugStore
	defer func() { storage.NewDatatugStore = origNewStore }()
	asked := 0
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore {
			asked++
			return mockProjectStore{loadEnvironmentSummaryFunc: func(context.Context, string) (*datatug.EnvironmentSummary, error) {
				return &datatug.EnvironmentSummary{}, nil
			}}
		}}, nil
	}
	ref := func(id string) dto.ProjectItemRef {
		return dto.ProjectItemRef{ProjectRef: dto.ProjectRef{ProjectID: "p1"}, ID: id}
	}
	if _, err := GetEnvironmentSummary(context.Background(), ref("local")); err != nil || asked != 1 {
		t.Fatalf("plain ID: err = %v, store asked %d times, want 1", err, asked)
	}
	asked = 0
	for _, c := range sourcecases.UnsafeIdentifiers() {
		if c.ID == "" {
			continue // an empty ID is the missing-field error the function already gave
		}
		if _, err := GetEnvironmentSummary(context.Background(), ref(c.ID)); err == nil || !validation.IsBadRequestError(err) {
			t.Errorf("%s: GetEnvironmentSummary = %v, want a bad-request refusal", c.Name, err)
		}
	}
	if asked != 0 {
		t.Fatalf("the store was asked %d times for an unsafe ID, want 0", asked)
	}
}
