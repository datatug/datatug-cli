package sneatv

import (
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func TestSneatV(t *testing.T) {
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	app := tview.NewApplication().SetScreen(screen)
	done := make(chan error, 1)
	go func() {
		done <- app.Run()
	}()
	defer func() {
		app.Stop()
		<-done
	}()

	adapter := applicationAdapter{app: app}

	called := make(chan bool, 1)
	adapter.QueueUpdateDraw(func() {
		called <- true
	})

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("QueueUpdateDraw timed out")
	}

	box := tview.NewBox()
	adapter.SetFocus(box)

	tabs := NewTabs(app, UnderlineTabsStyle, WithLabel("test"))
	if tabs == nil {
		t.Fatal("expected non-nil tabs")
	}

	b1 := WithDefaultBorders(box, box)
	if b1.Primitive != box {
		t.Fatal("expected primitive to match")
	}

	b2 := WithBordersWithoutPadding(box, box)
	if b2.Primitive != box {
		t.Fatal("expected primitive to match")
	}

	b3 := WithBoxWithoutBorder(box, box)
	if b3.Primitive != box {
		t.Fatal("expected primitive to match")
	}

	DefaultBorderWithPadding(box)
	DefaultBorderWithoutPadding(box)
	SetPanelTitle(box, "test title")

	crumb := NewBreadcrumb("crumb", func() error { return nil })
	_ = NewBreadcrumbs(crumb)
	_ = NewButtonWithShortcut("btn", 'b')
}
