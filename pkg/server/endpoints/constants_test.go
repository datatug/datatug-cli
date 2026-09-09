package endpoints

import (
	"net/url"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

// configureServedProjects wires api.ConfigureSecureSession the way
// `datatug serve` does at startup (http_server.go's ServeHTTP), so
// fillProjectRef/projectRefByID's api.ResolveStoreID calls have real
// served-project state to resolve against instead of the previous hardcoded
// "firestore" default (S87).
func configureServedProjects(t *testing.T, pathsByID map[string]string) {
	t.Helper()
	session, err := secureread.NewSession(secureread.SessionOptions{NoPolicies: true})
	if err != nil {
		t.Fatalf("secureread.NewSession: %v", err)
	}
	api.ConfigureSecureSession(session, pathsByID, api.Capabilities{})
}

func TestFillProjectRef(t *testing.T) {
	configureServedProjects(t, map[string]string{"proj1": "/tmp/proj1", "myproject": "/tmp/myproject"})

	tests := []struct {
		name          string
		values        url.Values
		wantStoreID   string
		wantProjectID string
		wantErr       bool
	}{
		{
			name:          "explicit storage matching the configured store resolves",
			values:        url.Values{"storage": {api.LocalStoreID}, "project": {"myproject"}},
			wantStoreID:   api.LocalStoreID,
			wantProjectID: "myproject",
		},
		{
			name:          "empty storage defaults to the served project's configured store",
			values:        url.Values{"project": {"proj1"}},
			wantStoreID:   api.LocalStoreID,
			wantProjectID: "proj1",
		},
		{
			// S85's exact reproduction: the client sends no storage param at
			// all; the old code hardcoded "firestore", which no
			// `datatug serve --project` session ever configures.
			name:          "no storage param at all still resolves for a served project",
			values:        url.Values{"project": {"proj1"}},
			wantStoreID:   api.LocalStoreID,
			wantProjectID: "proj1",
		},
		{
			name:    "explicit unknown storage id errors (S87 required case)",
			values:  url.Values{"storage": {"firestore"}, "project": {"proj1"}},
			wantErr: true,
		},
		{
			name:    "project this process does not serve errors instead of defaulting to firestore",
			values:  url.Values{"project": {"not-served"}},
			wantErr: true,
		},
		{
			name:          "no project id skips store resolution (downstream validation reports the missing id)",
			values:        url.Values{},
			wantStoreID:   "",
			wantProjectID: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := newProjectRef(tc.values)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("newProjectRef(%v) = nil error, want one", tc.values)
				}
				return
			}
			if err != nil {
				t.Fatalf("newProjectRef(%v): %v", tc.values, err)
			}
			if ref.StoreID != tc.wantStoreID {
				t.Errorf("StoreID = %q, want %q", ref.StoreID, tc.wantStoreID)
			}
			if ref.ProjectID != tc.wantProjectID {
				t.Errorf("ProjectID = %q, want %q", ref.ProjectID, tc.wantProjectID)
			}
		})
	}
}

func TestFillProjectItemRef(t *testing.T) {
	configureServedProjects(t, map[string]string{"p1": "/tmp/p1"})

	tests := []struct {
		name        string
		values      url.Values
		idParamName string
		wantID      string
		wantStore   string
		wantErr     bool
	}{
		{
			name:        "explicit id param name",
			values:      url.Values{"project": {"p1"}, "myid": {"item42"}},
			idParamName: "myid",
			wantID:      "item42",
			wantStore:   api.LocalStoreID,
		},
		{
			name:        "default id param name",
			values:      url.Values{"project": {"p1"}, "id": {"item7"}},
			idParamName: "",
			wantID:      "item7",
			wantStore:   api.LocalStoreID,
		},
		{
			name:        "missing id",
			values:      url.Values{"project": {"p1"}},
			idParamName: "id",
			wantID:      "",
			wantStore:   api.LocalStoreID,
		},
		{
			name:        "explicit unknown storage id on an item route also errors",
			values:      url.Values{"project": {"p1"}, "storage": {"firestore"}, "id": {"item1"}},
			idParamName: "id",
			wantErr:     true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := newProjectItemRef(tc.values, tc.idParamName)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("newProjectItemRef(%v, %q) = nil error, want one", tc.values, tc.idParamName)
				}
				return
			}
			if err != nil {
				t.Fatalf("newProjectItemRef(%v, %q): %v", tc.values, tc.idParamName, err)
			}
			if ref.ID != tc.wantID {
				t.Errorf("ID = %q, want %q", ref.ID, tc.wantID)
			}
			if ref.StoreID != tc.wantStore {
				t.Errorf("StoreID = %q, want %q", ref.StoreID, tc.wantStore)
			}
		})
	}
}

// TestProjectRefByID covers the id-carrying-project-id route family
// (project_summary/project_full) getting the identical api.ResolveStoreID
// treatment fillProjectRef gets.
func TestProjectRefByID(t *testing.T) {
	configureServedProjects(t, map[string]string{"p1": "/tmp/p1"})

	t.Run("served project with no storage param resolves", func(t *testing.T) {
		ref, err := projectRefByID(url.Values{"id": {"p1"}})
		if err != nil {
			t.Fatalf("projectRefByID: %v", err)
		}
		if ref.StoreID != api.LocalStoreID || ref.ProjectID != "p1" {
			t.Errorf("ref = %+v, want StoreID=%s ProjectID=p1", ref, api.LocalStoreID)
		}
	})

	t.Run("explicit unknown storage id errors", func(t *testing.T) {
		_, err := projectRefByID(url.Values{"id": {"p1"}, "storage": {"firestore"}})
		if err == nil {
			t.Fatal("projectRefByID with an unknown explicit storage id = nil error, want one")
		}
	})

	t.Run("no id at all skips store resolution", func(t *testing.T) {
		ref, err := projectRefByID(url.Values{})
		if err != nil {
			t.Fatalf("projectRefByID: %v", err)
		}
		if ref.StoreID != "" || ref.ProjectID != "" {
			t.Errorf("ref = %+v, want zero value", ref)
		}
	})
}
