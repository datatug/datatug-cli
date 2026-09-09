package dtroot

import (
	"errors"
	"testing"
)

func TestPath_success(t *testing.T) {
	orig := homedirDir
	defer func() { homedirDir = orig }()

	homedirDir = func() (string, error) { return "/tmp", nil }
	if got, want := Path(), "/tmp/datatug"; got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestPath_panic(t *testing.T) {
	orig := homedirDir
	defer func() { homedirDir = orig }()

	homedirDir = func() (string, error) { return "", errors.New("no home") }
	defer func() {
		if recover() == nil {
			t.Error("expected Path() to panic when the home dir cannot be resolved")
		}
	}()
	Path()
}
