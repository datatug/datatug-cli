package secureread

import (
	"context"
	"strings"
	"testing"
)

func TestDuckDBRefusesExternalReadsInGuardedSession(t *testing.T) {
	e := NewExecutor(Session{AllowOpaqueSQL: true})
	_, err := e.RunNativeSQL(context.Background(), "duckdb://:memory:", "SELECT * FROM read_parquet('/tmp/private.parquet')")
	if err == nil || !strings.Contains(err.Error(), "external files and URLs") {
		t.Fatalf("expected external resource refusal, got %v", err)
	}
}
