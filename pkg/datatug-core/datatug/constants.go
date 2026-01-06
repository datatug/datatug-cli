package datatug

import (
	"path/filepath"

	"github.com/mitchellh/go-homedir"
)

var homedirDir = homedir.Dir

const dir = "DataTug"

func Dir() string {
	homeDir, err := homedirDir()
	if err != nil {
		panic("could not get home dir: " + err.Error())
	}
	return filepath.Join(homeDir, dir)
}
