package dtstate

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/strongo/logus"
)

const cliStateFileName = ".datatug-cli-state.json"

//const recentDir = ".recent"

type RecentProject struct {
	CurrentScreePath string `json:"current_screen_path"`
}

type DatatugState struct {
	CurrentScreenPath string `yaml:"current_screen_path,omitempty" json:"current_screen_path,omitempty"`
}

func GetDatatugState() (state *DatatugState, err error) {
	state = new(DatatugState)
	filePath := getFilePath()
	var fileInfo fs.FileInfo
	if fileInfo, err = os.Stat(filePath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return
		}
	}
	if fileInfo.Size() == 0 {
		return
	}
	var f *os.File
	f, err = os.Open(filePath)
	if err != nil {
		return
	}
	defer func() {
		_ = f.Close()
	}()
	err = json.NewDecoder(f).Decode(&state)
	return
}

func SaveCurrentScreePathSync(currentScreenPath string) {
	state, err := GetDatatugState()
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			ctx := context.Background()
			logus.Errorf(ctx, "failed to get datatug state file: %v", err)
			return
		}
		state = new(DatatugState)
	}
	state.CurrentScreenPath = currentScreenPath
	if err = saveSate(state); err != nil {
		ctx := context.Background()
		logus.Errorf(ctx, "failed to save currentScreenPath to state file: %v", err)
	}
}
func SaveCurrentScreePath(currentScreenPath string) {
	go SaveCurrentScreePathSync(currentScreenPath)
}

func saveSate(state *DatatugState) (err error) {
	filePath := getFilePath()
	var f *os.File
	if f, err = os.Create(filePath); err != nil {
		return
	}
	defer func() {
		if errClose := f.Close(); errClose != nil {
			ctx := context.Background()
			logus.Errorf(ctx, "failed to close DataTug state file: %v", errClose)
			if err == nil {
				err = errClose
			}
		}
	}()
	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "\t")
	err = encoder.Encode(state)
	return
}

func getFilePath() string {
	return filepath.Join(datatug.Dir(), cliStateFileName)
}
