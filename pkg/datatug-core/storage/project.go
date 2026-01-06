package storage

import (
	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
)

type ProjectStoreRef interface {
	ProjectStore() datatug.ProjectStore
}
