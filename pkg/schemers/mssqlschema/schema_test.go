package mssqlschema

import (
	"testing"
)

func TestNewSchemaProvider(t *testing.T) {
	if got := NewSchemaProvider(); got == nil {
		t.Errorf("NewSchemaProvider() = %v, want %v", got, nil)
	}
	if got := NewSchemaProvider(nil); got == nil {
		t.Errorf("NewSchemaProvider(nil) = %v, want %v", got, nil)
	}
}
