package clouds

import (
	"github.com/datatug/datatug-cli/pkg/schemers"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
)

type CloudContext struct {
	TUI *sneatnav.TUI
}

type ProjectContext interface {
	Schema() schemers.Provider
}
