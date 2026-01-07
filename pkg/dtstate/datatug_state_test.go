package dtstate

import (
	"errors"
	"io/fs"
	"testing"
)

func TestBumpRecentProject(t *testing.T) {
	// Backup original functions
	origGetState := getState
	origSaveState := saveState
	defer func() {
		getState = origGetState
		saveState = origSaveState
	}()

	t.Run("first_project", func(t *testing.T) {
		var savedState *DatatugState
		getState = func() (*DatatugState, error) {
			return nil, fs.ErrNotExist
		}
		saveState = func(state *DatatugState) error {
			savedState = state
			return nil
		}

		err := bumpRecentProject("project1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(savedState.RecentProjects) != 1 {
			t.Fatalf("expected 1 recent project, got %d", len(savedState.RecentProjects))
		}
		if savedState.RecentProjects[0].ID != "project1" {
			t.Errorf("expected project1, got %s", savedState.RecentProjects[0].ID)
		}
	})

	t.Run("multiple_projects", func(t *testing.T) {
		state := &DatatugState{
			RecentProjects: []*RecentProject{
				{ID: "project1"},
				{ID: "project2"},
			},
		}
		getState = func() (*DatatugState, error) {
			return state, nil
		}
		var savedState *DatatugState
		saveState = func(s *DatatugState) error {
			savedState = s
			return nil
		}

		err := bumpRecentProject("project3")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(savedState.RecentProjects) != 3 {
			t.Fatalf("expected 3 recent projects, got %d", len(savedState.RecentProjects))
		}
		if savedState.RecentProjects[0].ID != "project3" {
			t.Errorf("expected project3 at the beginning, got %s", savedState.RecentProjects[0].ID)
		}
	})

	t.Run("reorder_existing", func(t *testing.T) {
		state := &DatatugState{
			RecentProjects: []*RecentProject{
				{ID: "project1"},
				{ID: "project2"},
				{ID: "project3"},
			},
		}
		getState = func() (*DatatugState, error) {
			return state, nil
		}
		var savedState *DatatugState
		saveState = func(s *DatatugState) error {
			savedState = s
			return nil
		}

		err := bumpRecentProject("project2")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(savedState.RecentProjects) != 3 {
			t.Fatalf("expected 3 recent projects, got %d", len(savedState.RecentProjects))
		}
		if savedState.RecentProjects[0].ID != "project2" {
			t.Errorf("expected project2 at the beginning, got %s", savedState.RecentProjects[0].ID)
		}
		if savedState.RecentProjects[1].ID != "project1" {
			t.Errorf("expected project1 at index 1, got %s", savedState.RecentProjects[1].ID)
		}
		if savedState.RecentProjects[2].ID != "project3" {
			t.Errorf("expected project3 at index 2, got %s", savedState.RecentProjects[2].ID)
		}
	})

	t.Run("truncate_max_3", func(t *testing.T) {
		state := &DatatugState{
			RecentProjects: []*RecentProject{
				{ID: "project1"},
				{ID: "project2"},
				{ID: "project3"},
			},
		}
		getState = func() (*DatatugState, error) {
			return state, nil
		}
		var savedState *DatatugState
		saveState = func(s *DatatugState) error {
			savedState = s
			return nil
		}

		err := bumpRecentProject("project4")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(savedState.RecentProjects) != 3 {
			t.Fatalf("expected 3 recent projects, got %d", len(savedState.RecentProjects))
		}
		if savedState.RecentProjects[0].ID != "project4" {
			t.Errorf("expected project4 at the beginning, got %s", savedState.RecentProjects[0].ID)
		}
		if savedState.RecentProjects[2].ID != "project2" {
			t.Errorf("expected project2 at index 2, got %s", savedState.RecentProjects[2].ID)
		}
	})

	t.Run("get_state_error", func(t *testing.T) {
		getState = func() (*DatatugState, error) {
			return nil, errors.New("test error")
		}
		err := bumpRecentProject("project1")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}
