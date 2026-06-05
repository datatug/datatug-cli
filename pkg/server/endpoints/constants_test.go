package endpoints

import (
	"net/url"
	"testing"
)

func TestFillProjectRef(t *testing.T) {
	tests := []struct {
		name          string
		values        url.Values
		wantStoreID   string
		wantProjectID string
	}{
		{
			name:          "both present",
			values:        url.Values{"storage": {"mystore"}, "project": {"myproject"}},
			wantStoreID:   "mystore",
			wantProjectID: "myproject",
		},
		{
			name:          "empty storage defaults to firestore",
			values:        url.Values{"project": {"proj1"}},
			wantStoreID:   "firestore",
			wantProjectID: "proj1",
		},
		{
			name:          "both empty",
			values:        url.Values{},
			wantStoreID:   "firestore",
			wantProjectID: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ref := newProjectRef(tc.values)
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
	tests := []struct {
		name        string
		values      url.Values
		idParamName string
		wantID      string
		wantStore   string
	}{
		{
			name:        "explicit id param name",
			values:      url.Values{"storage": {"s1"}, "project": {"p1"}, "myid": {"item42"}},
			idParamName: "myid",
			wantID:      "item42",
			wantStore:   "s1",
		},
		{
			name:        "default id param name",
			values:      url.Values{"project": {"p1"}, "id": {"item7"}},
			idParamName: "",
			wantID:      "item7",
			wantStore:   "firestore",
		},
		{
			name:        "missing id",
			values:      url.Values{"project": {"p1"}},
			idParamName: "id",
			wantID:      "",
			wantStore:   "firestore",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ref := newProjectItemRef(tc.values, tc.idParamName)
			if ref.ID != tc.wantID {
				t.Errorf("ID = %q, want %q", ref.ID, tc.wantID)
			}
			if ref.StoreID != tc.wantStore {
				t.Errorf("StoreID = %q, want %q", ref.StoreID, tc.wantStore)
			}
		})
	}
}
