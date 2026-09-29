package dtproject

import (
	"testing"

	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
)

func TestModule(t *testing.T) {
	m := Module()
	if m.ID != datatugui.ScreenProjects || m.Text != "Projects" || m.Shortcut != 'p' {
		t.Fatalf("unexpected module %+v", m)
	}
	page := m.Root()
	if page.Content == nil || page.Menu != nil {
		t.Fatalf("root page must have content and leave the menu alone: %+v", page)
	}
}
