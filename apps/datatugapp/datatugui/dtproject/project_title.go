package dtproject

import (
	"strings"

	"github.com/datatug/datatug-core/pkg/dtconfig"
)

// projectTitle is the name a project is shown under: its title, else its ID,
// else its URL.
func projectTitle(p *dtconfig.ProjectRef) string {
	switch {
	case p.Title != "":
		return p.Title
	case p.ID != "":
		return p.ID
	}
	return p.Url
}

// projectShortTitle is the last path element of projectTitle.
func projectShortTitle(p *dtconfig.ProjectRef) string {
	title := projectTitle(p)
	return title[strings.LastIndex(title, "/")+1:]
}
