// Package datatugui is the application layer of the DataTug terminal UI: the
// registry of root modules, the shared main menu, the Bubble Tea app model that
// hosts the tuigoff navigation shell, and the helpers every screen uses.
//
// A root module (projects, viewers, settings, API monitor) describes itself
// with a Module value and the app is assembled from them:
//
//	err := datatugui.Run(
//		[]datatugui.Module{dtproject.Module(), dtviewers.Module(), ...},
//		datatugui.Options{Start: datatugui.ScreenViewers},
//	)
//
// Nothing here calls back: highlighting a menu item, opening a module and
// opening the web UI are messages handled by App. See docs/tui-screens.md for
// how to write a screen.
package datatugui
