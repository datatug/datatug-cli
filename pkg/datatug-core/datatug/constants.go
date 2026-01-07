package datatug

import (
	"path/filepath"

	"github.com/mitchellh/go-homedir"
)

var homedirDir = homedir.Dir

const Dir = "datatug"

func DirPath() string {
	homeDir, err := homedirDir()
	if err != nil {
		panic("could not get home Dir: " + err.Error())
	}
	return filepath.Join(homeDir, Dir)
}
