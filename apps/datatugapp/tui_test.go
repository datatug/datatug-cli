package datatugapp

import (
	"testing"

	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/stretchr/testify/assert"
)

func TestNewDatatugTUI(t *testing.T) {
	tui := NewDatatugTUI()
	if tui == nil {
		t.Fatalf("NewDatatugTUI() returned nil")
		return
	}
	if tui.App == nil {
		t.Fatal("expected tui.App to be not nil")
	}
	if tui.Header == nil {
		t.Fatal("expected tui.Header to be not nil")
	}
	called := false
	goProjectScreen = func(tui *sneatnav.TUI, focusTo sneatnav.FocusTo) error {
		called = true
		return nil
	}
	assert.NoError(t, tui.Header.Breadcrumbs().GoHome())
	assert.True(t, called)
}
