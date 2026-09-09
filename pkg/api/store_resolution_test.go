package api

import (
	"errors"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// TestResolveStoreID_Pure covers resolveStoreID's decision table directly
// against an injected configured-store list — including the "several
// configured stores, no explicit param" case (S87's required test), which
// ConfiguredStoreIDs itself can never produce with today's serve
// architecture (a single `datatug serve` process configures exactly one
// filestore-backed store) but the logic must still handle correctly for a
// future multi-store session.
func TestResolveStoreID_Pure(t *testing.T) {
	tests := []struct {
		name       string
		explicit   string
		projectID  string
		configured []string
		wantID     string
		wantErr    error
	}{
		{
			name:       "no explicit param, exactly one configured store resolves",
			explicit:   "",
			projectID:  "proj1",
			configured: []string{"local"},
			wantID:     "local",
		},
		{
			name:       "explicit param matching the configured store is honored",
			explicit:   "local",
			projectID:  "proj1",
			configured: []string{"local"},
			wantID:     "local",
		},
		{
			name:       "explicit param naming an unconfigured store errors (required case)",
			explicit:   "firestore",
			projectID:  "proj1",
			configured: []string{"local"},
			wantErr:    ErrUnknownStoreID,
		},
		{
			name:       "no explicit param, zero configured stores errors",
			explicit:   "",
			projectID:  "not-served",
			configured: nil,
			wantErr:    ErrUnknownStoreID,
		},
		{
			name:       "no explicit param, two configured stores is INVALID_REQUEST-worthy ambiguity (required case)",
			explicit:   "",
			projectID:  "multi-store-project",
			configured: []string{"local", "cloud"},
			wantErr:    ErrAmbiguousStore,
		},
		{
			name:       "explicit param matching one of several configured stores still resolves",
			explicit:   "cloud",
			projectID:  "multi-store-project",
			configured: []string{"local", "cloud"},
			wantID:     "cloud",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, err := resolveStoreID(tc.explicit, tc.projectID, tc.configured)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("resolveStoreID(%q, %q, %v) = (%q, %v), want error %v", tc.explicit, tc.projectID, tc.configured, id, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveStoreID(%q, %q, %v): %v", tc.explicit, tc.projectID, tc.configured, err)
			}
			if id != tc.wantID {
				t.Errorf("id = %q, want %q", id, tc.wantID)
			}
		})
	}
}

// TestConfiguredStoreIDs_ServedProjectSessionResolves is S87's required
// "served-project session with no store param -> resolves" case, exercised
// through the real ConfigureSecureSession wiring `datatug serve` uses.
func TestConfiguredStoreIDs_ServedProjectSessionResolves(t *testing.T) {
	session, err := secureread.NewSession(secureread.SessionOptions{NoPolicies: true})
	if err != nil {
		t.Fatalf("secureread.NewSession: %v", err)
	}
	ConfigureSecureSession(session, map[string]string{"served-project": "/tmp/served-project"}, Capabilities{})

	id, err := ResolveStoreID("", "served-project")
	if err != nil {
		t.Fatalf("ResolveStoreID(\"\", served-project): %v", err)
	}
	if id != LocalStoreID {
		t.Errorf("id = %q, want %q", id, LocalStoreID)
	}

	if _, err := ResolveStoreID("", "unserved-project"); !errors.Is(err, ErrUnknownStoreID) {
		t.Errorf("ResolveStoreID(\"\", unserved-project) = %v, want ErrUnknownStoreID", err)
	}

	if _, err := ResolveStoreID("firestore", "served-project"); !errors.Is(err, ErrUnknownStoreID) {
		t.Errorf("ResolveStoreID(firestore, served-project) = %v, want ErrUnknownStoreID", err)
	}
}
