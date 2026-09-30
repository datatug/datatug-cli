package dtsettings

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/nav/navtest"
	"github.com/tuigoff/tuigoff/pkg/uitest"
	"github.com/tuigoff/tuigoff/pkg/widgets"
)

// stub replaces the package seams and returns the restore function.
func stub(t *testing.T, settings func() (dtconfig.Settings, error), marshal func(any) ([]byte, error), lexer func(string) chroma.Lexer) {
	t.Helper()
	oldS, oldM, oldL := getSettingsFn, marshalFn, getLexerFn
	t.Cleanup(func() { getSettingsFn, marshalFn, getLexerFn = oldS, oldM, oldL })
	if settings != nil {
		getSettingsFn = settings
	}
	if marshal != nil {
		marshalFn = marshal
	}
	if lexer != nil {
		getLexerFn = lexer
	}
}

func okSettings() (dtconfig.Settings, error) { return dtconfig.Settings{}, nil }

func TestModule(t *testing.T) {
	m := Module()
	if m.ID != datatugui.ScreenSettings || m.Text != "Settings" || m.Shortcut != 's' {
		t.Fatalf("unexpected module: %+v", m)
	}
}

func TestSettingsShowsConfig(t *testing.T) {
	stub(t, okSettings, func(any) ([]byte, error) { return []byte("server:\n  host: example.org\n"), nil }, nil)
	h := navtest.New(t, Module().Root())
	h.RequireContains("Config File: ~/.datatug.yaml")
	h.RequireContains("host: example.org")
	if !strings.Contains(h.Styled(), "\x1b[") {
		t.Fatal("expected syntax highlighting")
	}
}

func TestSettingsRealMarshal(t *testing.T) {
	stub(t, okSettings, nil, nil)
	h := navtest.New(t, Module().Root())
	h.RequireContains("Config File")
}

func TestSettingsReadError(t *testing.T) {
	stub(t, func() (dtconfig.Settings, error) { return dtconfig.Settings{}, errors.New("cannot read config") }, nil, nil)
	h := navtest.New(t, Module().Root())
	h.RequireContains("cannot read config")
}

func TestSettingsMarshalError(t *testing.T) {
	stub(t, okSettings, func(any) ([]byte, error) { return nil, errors.New("cannot marshal") }, nil)
	h := navtest.New(t, Module().Root())
	h.RequireContains("cannot marshal")
}

// failingLexer cannot tokenise.
type failingLexer struct{ chroma.Lexer }

func (failingLexer) Tokenise(*chroma.TokeniseOptions, string) (chroma.Iterator, error) {
	return nil, errors.New("lexer broke")
}

func TestSettingsHighlightError(t *testing.T) {
	stub(t, okSettings, nil, func(string) chroma.Lexer { return failingLexer{} })
	h := navtest.New(t, Module().Root())
	h.RequireContains("show settings: lexer broke")
}

func TestSettingsScreenKeysAndFocus(t *testing.T) {
	var many strings.Builder
	for i := 0; i < 60; i++ {
		many.WriteString("key: value\n")
	}
	stub(t, okSettings, func(any) ([]byte, error) { return []byte(many.String()), nil }, nil)

	var s nav.Screen = newSettings()
	s, _ = s.Update(tea.WindowSizeMsg{Width: 40, Height: 5})
	msgs := uitest.Msgs(s.Init())
	if len(msgs) != 1 {
		t.Fatalf("Init should load once, got %v", msgs)
	}
	s, _ = s.Update(msgs[0])
	b := s.(widgets.Boundary)
	if !b.AtEdge(widgets.Up) || b.AtEdge(widgets.Down) {
		t.Fatal("expected to sit at the top of a long text")
	}
	s, _ = s.Update(nav.ScreenFocusMsg{Focused: true})
	s, _ = s.Update(uitest.Key("down"))
	if b = s.(widgets.Boundary); b.AtEdge(widgets.Up) {
		t.Fatal("down should scroll")
	}
	s, _ = s.Update(nav.ScreenFocusMsg{Focused: false})
	s, _ = s.Update(uitest.Key("up"))
	if s.(widgets.Boundary).AtEdge(widgets.Up) {
		t.Fatal("an unfocused screen ignores keys")
	}
	if s.(nav.Titled).Title() != " Config File: ~/.datatug.yaml" {
		t.Fatalf("title = %q", s.(nav.Titled).Title())
	}
}
