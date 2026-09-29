package dtviewers

import "github.com/strongo/strongo-tui/pkg/nav"

// ViewerID identifies a viewer; it is the second element of the persisted
// screen path, for example "viewers/sql".
type ViewerID string

// Viewer is one entry of the Viewers screen: a way to browse data, such as a
// SQL database or a cloud account.
type Viewer struct {
	// ID identifies the viewer.
	ID ViewerID
	// Name is the label shown in the list.
	Name string
	// Description is shown muted under the name.
	Description string
	// Shortcut, when not zero, selects the viewer from the list.
	Shortcut rune
	// Root builds the page pushed when the viewer is chosen. An empty Title
	// defaults to Name. A page that needs its own menu sets Menu; otherwise the
	// main menu stays.
	Root func() nav.Page
}
