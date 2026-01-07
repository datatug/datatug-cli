package dtstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/datatug/datatug-cli/apps/global"
	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/strongo/logus"
)

const cliStateFileName = ".datatug-cli.json"

//const recentDir = ".recent"

type RecentProject struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
}

type DatatugState struct {
	RecentProjects    []*RecentProject `json:"recent_projects"`
	CurrentScreenPath string           `yaml:"current_screen_path,omitempty" json:"current_screen_path,omitempty"`
}

var saveState = SaveState
var getState = GetDatatugState

func GetDatatugState() (state *DatatugState, err error) {
	state = new(DatatugState)
	filePath := getFilePath()
	var fileInfo fs.FileInfo
	if fileInfo, err = os.Stat(filePath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
		return
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
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
	}()
	err = json.NewDecoder(f).Decode(&state)
	if len(state.RecentProjects) > 0 {
		hadRecentProjects = true
	}
	return
}

func BumpRecentProject(projectID string) {
	go func() {
		if err := bumpRecentProject(projectID); err != nil {
			ctx := context.Background()
			logus.Errorf(ctx, "failed to bump recent project '%s': %v", projectID, err)
		}
	}()
}

// bumpRecentProject reads DatatugState using `getState` and:
// - removes the current entry with the same ID from DatatugState.RecentProjects (if exists)
// - inserts a new entry with the same ID to the beginning of DatatugState.RecentProjects
// - Truncates DatatugState.RecentProjects to max 3 items
// - Saves the updated DatatugState using `saveState` func
func bumpRecentProject(projectID string) error {
	state, err := getState()

	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("failed to get datatug state: %w", err)
		}
		state = new(DatatugState)
	}

	// Remove existing entry with the same ID
	for i, p := range state.RecentProjects {
		if p.ID == projectID {
			state.RecentProjects = append(state.RecentProjects[:i], state.RecentProjects[i+1:]...)
			break
		}
	}

	// Insert new entry at the beginning
	newEntry := &RecentProject{
		ID:        projectID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	state.RecentProjects = append([]*RecentProject{newEntry}, state.RecentProjects...)

	// Truncate to max 3 items
	if len(state.RecentProjects) > 3 {
		state.RecentProjects = state.RecentProjects[:3]
	}

	if err = saveState(state); err != nil {
		return fmt.Errorf("failed to save datatug state: %w", err)
	}
	return nil
}

func SaveCurrentScreePathSync(currentScreenPath string) {
	state, err := getState()
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			global.App.Stop()
			time.Sleep(time.Millisecond)
			panic(fmt.Sprintf("failed to get datatug state: %v", err))
		}
		state = new(DatatugState) // File does not exist
	}
	state.CurrentScreenPath = currentScreenPath
	if err = saveState(state); err != nil {
		ctx := context.Background()
		logus.Errorf(ctx, "failed to save currentScreenPath to state file: %v", err)
	}
}

var hadRecentProjects = false

func SaveCurrentScreePath(currentScreenPath string) {
	SaveCurrentScreePathSync(currentScreenPath)
}

func SaveState(state *DatatugState) (err error) {
	if hadRecentProjects && len(state.RecentProjects) == 0 {
		global.App.Stop()
		time.Sleep(time.Millisecond)
		panic("no recent projects found")
	}
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
	return filepath.Join(datatug.DirPath(), cliStateFileName)
}
