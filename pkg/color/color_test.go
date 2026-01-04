package color_test

import (
	"testing"

	"github.com/datatug/datatug-cli/pkg/color"
)

const (
	ansiRedLight  = "\x1b[91m"
	ansiGreen     = "\x1b[32m"
	ansiBlueLight = "\x1b[94m"
	ansiGray      = "\x1b[90m"
	ansiYellow    = "\x1b[33m"
	ansiDefault   = "\x1b[39m"
	ansiResetAll  = "\x1b[0m"
)

func TestColorize_UsesDefaultReset(t *testing.T) {
	// Save and restore default color
	t.Cleanup(func() { color.SetDefaultColor(ansiDefault) })

	color.SetDefaultColor(ansiResetAll)

	got := color.Colorize("Hello", ansiRedLight)
	want := ansiRedLight + "Hello" + ansiResetAll
	if got != want {
		t.Fatalf("Colorize result mismatch.\n got:  %q\n want: %q", got, want)
	}
}

func TestColorFunctions_WrapWithCorrectCodes(t *testing.T) {
	t.Cleanup(func() { color.SetDefaultColor(ansiDefault) })
	// Ensure default reset is the package's default
	color.SetDefaultColor(ansiDefault)

	tests := []struct {
		name string
		fn   func(string) string
		code string
	}{
		{name: "Red", fn: color.Red, code: ansiRedLight},
		{name: "Green", fn: color.Green, code: ansiGreen},
		{name: "Blue", fn: color.Blue, code: ansiBlueLight},
		{name: "Gray", fn: color.Gray, code: ansiGray},
		{name: "Yellow", fn: color.Yellow, code: ansiYellow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.fn("X")
			want := tt.code + "X" + ansiDefault
			if got != want {
				t.Fatalf("%s result mismatch.\n got:  %q\n want: %q", tt.name, got, want)
			}
		})
	}
}

func TestColorFunctions_EmptyString(t *testing.T) {
	t.Cleanup(func() { color.SetDefaultColor(ansiDefault) })
	color.SetDefaultColor(ansiDefault)

	got := color.Red("")
	want := ansiRedLight + "" + ansiDefault
	if got != want {
		t.Fatalf("Empty string wrapping mismatch. got %q want %q", got, want)
	}
}

func TestStyles_DangerWarningSuccess(t *testing.T) {
	t.Cleanup(func() { color.SetDefaultColor(ansiDefault) })
	color.SetDefaultColor(ansiDefault)

	if got, want := color.Danger("boom"), ansiRedLight+"boom"+ansiDefault; got != want {
		t.Fatalf("Danger mismatch. got %q want %q", got, want)
	}
	if got, want := color.Warning("careful"), ansiYellow+"careful"+ansiDefault; got != want {
		t.Fatalf("Warning mismatch. got %q want %q", got, want)
	}
	if got, want := color.Success("ok"), ansiGreen+"ok"+ansiDefault; got != want {
		t.Fatalf("Success mismatch. got %q want %q", got, want)
	}
}

func TestSetDefaultColor_AffectsHelpers(t *testing.T) {
	// Ensure we restore the default after the test
	t.Cleanup(func() { color.SetDefaultColor(ansiDefault) })

	color.SetDefaultColor(ansiResetAll)
	got := color.Green("done")
	want := ansiGreen + "done" + ansiResetAll
	if got != want {
		t.Fatalf("SetDefaultColor didn't affect result. got %q want %q", got, want)
	}
}
