//go:build datatug_query_capture

package querywrite

import (
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
)

// In this build datatug-core exports its screen: the copy this package
// uses until the switch-over must decide every value the same way.
func TestEmbeddedCredentialReason_MatchesCore(t *testing.T) {
	for _, tt := range credentialCases {
		gotReason, gotFound := embeddedCredentialReason(tt.value)
		wantReason, wantFound := datatug.EmbeddedCredentialReason(tt.value)
		if gotFound != wantFound || gotReason != wantReason {
			t.Errorf("%q: copy = (%q, %v), core = (%q, %v)", tt.value, gotReason, gotFound, wantReason, wantFound)
		}
	}
}

func TestQueryCredentialReason_ScreensPurpose(t *testing.T) {
	q := datatug.QueryDef{Purpose: "copied from app:hunter2@tcp(db)/prod"}
	if field, _, found := QueryCredentialReason(&q); !found || field != "purpose" {
		t.Fatalf("QueryCredentialReason = (%q, %v), want a refusal of purpose", field, found)
	}
}
