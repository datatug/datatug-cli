package dtviewers

import "github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"

type Viewer struct {
	ID          ViewerID
	Name        string
	Description string
	Shortcut    rune
	Action      func(tui *sneatnav.TUI, focusTo sneatnav.FocusTo) error
}
