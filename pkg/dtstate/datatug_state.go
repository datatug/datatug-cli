package dtstate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/strongo/logus"
)

const cliStateFileName = ".datatug-cli-state.json"

type RecentProject struct {
}

type DatatugState struct {
	CurrentScreenPath string `yaml:"current_screen_path,omitempty" json:"current_screen_path,omitempty"`
}

func GetDatatugState() (state *DatatugState, err error) {
	filePath := getFilePath()
	var f *os.File
	f, err = os.Open(filePath)
	if err != nil {
		return
	}
	defer func() {
		_ = f.Close()
	}()
	state = new(DatatugState)
	err = json.NewDecoder(f).Decode(&state)
	return
}

func SaveCurrentScreePath(currentScreenPath string) {
	go func() {
		state, err := GetDatatugState()
		if err != nil {
			ctx := context.Background()
			logus.Errorf(ctx, "failed to get datatug state file: %v", err)
			return
		}
		state.CurrentScreenPath = currentScreenPath
		if err = saveSate(state); err != nil {
			ctx := context.Background()
			logus.Errorf(ctx, "failed to save currentScreenPath to state file: %v", err)
		}
	}()
	return
}

func saveSate(state *DatatugState) (err error) {
	filePath := getFilePath()
	f, err := os.Create(filePath)
	defer func() {
		_ = f.Close()
	}()
	err = json.NewEncoder(f).Encode(state)
	return
}

func getFilePath() string {
	return filepath.Join(datatug.Dir(), cliStateFileName)
}
