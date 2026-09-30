package datatugui

import (
	"strings"
	"testing"

	"github.com/strongo/strongo-tui/pkg/nav"
)

func TestModulePageDefaults(t *testing.T) {
	m := Module{ID: "x", Text: "Ex", Root: func() nav.Page {
		return nav.Page{Menu: nav.Static("own", "menu"), Focus: nav.FocusToContent}
	}}
	page := m.page(nav.FocusToMenu)
	if page.Title != "Ex" || page.Menu != nil || page.Focus != nav.FocusToMenu {
		t.Errorf("page = %#v", page)
	}
	m.Root = func() nav.Page { return nav.Page{Title: "Custom"} }
	if got := m.page(nav.FocusToMenu).Title; got != "Custom" {
		t.Errorf("title = %q", got)
	}
}

func TestCheckModulesPanics(t *testing.T) {
	root := func() nav.Page { return nav.Page{} }
	cases := map[string]struct {
		modules []Module
		want    string
	}{
		"no_id":   {[]Module{{Text: "A", Root: root}}, "needs an ID"},
		"no_root": {[]Module{{ID: "a", Text: "A"}}, "needs an ID"},
		"dup":     {[]Module{{ID: "a", Text: "A", Root: root}, {ID: "a", Text: "B", Root: root}}, "duplicate module"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				r, _ := recover().(string)
				if !strings.Contains(r, tc.want) {
					t.Errorf("panic = %q, want it to contain %q", r, tc.want)
				}
			}()
			checkModules(tc.modules)
		})
	}
	checkModules(testModules()) // valid modules do not panic
}

func TestStartScreen(t *testing.T) {
	modules := testModules()
	cases := map[string]struct {
		modules []Module
		path    string
		want    string
	}{
		"restores_module":      {modules, "viewers", ScreenViewers},
		"restores_by_prefix":   {modules, "settings/yaml", ScreenSettings},
		"empty_is_projects":    {modules, "", ScreenProjects},
		"unknown_is_projects":  {modules, "gone/deep", ScreenProjects},
		"no_projects_is_first": {modules[1:], "gone", ScreenViewers},
		"no_modules":           {nil, "viewers", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := StartScreen(tc.modules, tc.path); got != tc.want {
				t.Errorf("StartScreen(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}
