package databrowser

import (
	"testing"
)

func TestNewDataBrowser(t *testing.T) {
	b := NewDataBrowser()
	if b == nil {
		t.Fatal("expected non-nil DataBrowser")
	}
	if b.Grid == nil || b.Table == nil {
		t.Fatal("expected initialized Grid and Table")
	}
	b.SetTarget()
}
