package clouds

import "github.com/datatug/datatug-cli/pkg/schemers"

// ProjectContext is a cloud project whose data can be browsed.
type ProjectContext interface {
	Schema() schemers.Provider
}
