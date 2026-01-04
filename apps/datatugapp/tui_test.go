package datatugapp

import (
	"testing"

	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/stretchr/testify/assert"
)

func TestNewDatatugTUI(t *testing.T) {
	tui := NewDatatugTUI()
	if tui == nil {
		t.Fatal("expected tui to be not nil")
	}
	if tui.App == nil {
		t.Error("expected tui.App to be not nil")
	}
	called := false
	goProjectScreen = func(tui *sneatnav.TUI, focusTo sneatnav.FocusTo) error {
		called = true
		return nil
	}
	assert.NoError(t, tui.Header.Breadcrumbs().GoHome())
	assert.True(t, called)
}
