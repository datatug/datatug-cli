package api

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// TestPolicyDenialNamingOriginalResource covers S101 Fix 1's "a denial
// still logs the original request form" requirement.
func TestPolicyDenialNamingOriginalResource(t *testing.T) {
	t.Run("no narrowing leaves the error unchanged", func(t *testing.T) {
		// archive.Customer is a NON-default schema — PolicyCollectionName
		// never strips it, so the error (which already names it) must pass
		// through byte-for-byte, not just equivalently.
		original := fmt.Errorf("%w: resource=/archive.Customer: no matching allow rule", secureread.ErrAccessDenied)
		got := policyDenialNamingOriginalResource(original, "archive.Customer", "sqlite3")
		if got != original {
			t.Errorf("got a different error, want the exact original unchanged: %v", got)
		}
	})

	t.Run("narrowing appends the original requested form", func(t *testing.T) {
		// main.Customer IS the sqlite3 default schema — PolicyCollectionName
		// strips it to "Customer" for the actual policy check; if THAT still
		// denies, the error must still name main.Customer somewhere.
		original := fmt.Errorf("%w: resource=/Customer: no matching allow rule", secureread.ErrAccessDenied)
		got := policyDenialNamingOriginalResource(original, "main.Customer", "sqlite3")
		if !errors.Is(got, secureread.ErrAccessDenied) {
			t.Fatalf("errors.Is(got, ErrAccessDenied) = false, want true (the wrap chain must survive)")
		}
		if !strings.Contains(got.Error(), "main.Customer") {
			t.Errorf("error = %q, want it to name the original requested form main.Customer", got.Error())
		}
		if !strings.Contains(got.Error(), "resource=/Customer") {
			t.Errorf("error = %q, want it to still show the actually-checked resource /Customer", got.Error())
		}
	})

	t.Run("nil error passes through", func(t *testing.T) {
		if got := policyDenialNamingOriginalResource(nil, "main.Customer", "sqlite3"); got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})

	t.Run("a non-access-denial error passes through unchanged", func(t *testing.T) {
		original := errors.New("something else entirely")
		got := policyDenialNamingOriginalResource(original, "main.Customer", "sqlite3")
		if got != original {
			t.Errorf("got a different error, want the exact original unchanged: %v", got)
		}
	})
}
