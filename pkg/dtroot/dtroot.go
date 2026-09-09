// Package dtroot resolves the CLI-local data directory (~/datatug), used for
// CLI-only state and the "clone GitHub projects under here" convention. This
// is unrelated to and predates ~/.datatug.yaml (datatug-core's
// dtconfig.Settings) - it used to live as datatug.DirPath/datatug.Dir in
// datatug-cli's now-removed vendored copy of the model; datatug-core v0.17.0
// does not carry it, so it stays here as CLI-only.
package dtroot

import (
	"path/filepath"

	"github.com/mitchellh/go-homedir"
)

// Dir is the directory name under $HOME holding CLI-local state and
// conventionally-cloned projects (~/datatug/github.com/...).
const Dir = "datatug"

var homedirDir = homedir.Dir

// Path returns the absolute path to ~/datatug.
func Path() string {
	homeDir, err := homedirDir()
	if err != nil {
		panic("could not get home dir: " + err.Error())
	}
	return filepath.Join(homeDir, Dir)
}
