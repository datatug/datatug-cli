package mssqlschema

import (
	"context"
	"testing"

	"github.com/datatug/datatug-cli/pkg/datatug-core/schemer"
)

func TestTablesProvider_GetCollections(t *testing.T) {
	v := collectionsProvider{}
	ctx := context.Background()
	t.Run("panics on nil db", func(t *testing.T) {
		defer func() {
			if err := recover(); err == nil {
				t.Error("expected to panic if DB parameter is not supplied")
			}
		}()
		_, _ = v.GetCollections(ctx, schemer.NewSchemaKey("catalog", "schema"))
	})
}
