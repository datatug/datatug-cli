// Package sneatv keeps the small compatibility surface used by datatug-cli
// while filetug's UI helpers live in dedicated packages.
package sneatv

import (
	"github.com/filetug/filetug/pkg/sneatv"
	"github.com/filetug/filetug/pkg/sneatv/crumbs"
	"github.com/rivo/tview"
	"github.com/strongo/strongo-tui/pkg/colors"
	"github.com/strongo/strongo-tui/pkg/components/boxed"
	"github.com/strongo/strongo-tui/pkg/components/button"
	"github.com/strongo/strongo-tui/pkg/themes"
)

type (
	Tab                            = sneatv.Tab
	TabStyles                      = sneatv.TabStyles
	Tabs                           = sneatv.Tabs
	TabsApp                        = sneatv.TabsApp
	TabsOption                     = sneatv.TabsOption
	TabsStyle                      = sneatv.TabsStyle
	Breadcrumb                     = crumbs.Breadcrumb
	Breadcrumbs                    = crumbs.Breadcrumbs
	ButtonWithShortcut             = button.WithShortcut
	PrimitiveWithBox               = boxed.PrimitiveWithBox
	WithBoxType[T tview.Primitive] = boxed.WithBoxType[T]
)

var (
	UnderlineTabsStyle        = sneatv.UnderlineTabsStyle
	DefaultFocusedBorderColor = colors.DefaultFocusedBorderColor
	DefaultBlurBorderColor    = colors.DefaultBlurBorderColor
)

var (
	NewBreadcrumb         = crumbs.NewBreadcrumb
	NewBreadcrumbs        = crumbs.NewBreadcrumbs
	NewButtonWithShortcut = button.NewWithShortcut
	WithLabel             = sneatv.WithLabel
	FocusDown             = sneatv.FocusDown
	FocusLeft             = sneatv.FocusLeft
	FocusUp               = sneatv.FocusUp
)

type applicationAdapter struct{ app *tview.Application }

func (a applicationAdapter) QueueUpdateDraw(f func()) { a.app.QueueUpdateDraw(f) }

func (a applicationAdapter) SetFocus(p tview.Primitive) { a.app.SetFocus(p) }

func NewTabs(app *tview.Application, style TabsStyle, options ...TabsOption) *Tabs {
	return sneatv.NewTabs(applicationAdapter{app: app}, style, options...)
}

func WithDefaultBorders[T tview.Primitive](p T, box *tview.Box) WithBoxType[T] {
	return boxed.WithDefaultBorders(p, box)
}

func WithBordersWithoutPadding[T tview.Primitive](p T, box *tview.Box) WithBoxType[T] {
	return boxed.WithBordersWithoutPadding(p, box)
}

func WithBoxWithoutBorder[T tview.Primitive](p T, box *tview.Box) WithBoxType[T] {
	return boxed.WithBoxWithoutBorder(p, box)
}

func DefaultBorderWithPadding(box *tview.Box) { themes.DefaultBorderWithPadding(box) }

func DefaultBorderWithoutPadding(box *tview.Box) { themes.DefaultBorderWithoutPadding(box) }

func SetPanelTitle(box *tview.Box, title string) { themes.SetPanelTitle(box, title) }
