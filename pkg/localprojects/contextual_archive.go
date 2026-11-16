package localprojects

import (
	"encoding/json"
	"errors"
	"os"
)

// ReadArchivedFromRoot reads only the opened project's parent lifecycle flag.
// It conveys no owner inventory or read/write authority. Supported absence uses
// the same active default as readState; malformed/unreadable state is an error.
func ReadArchivedFromRoot(root *os.Root) (bool, error) {
	info, err := registryIO.rootLstat(root, stateName)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxMetadata {
		return false, ErrConflict
	}
	file, err := registryIO.rootOpenFile(root, stateName, os.O_RDONLY|noFollowFlags, 0)
	if err != nil {
		return false, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return false, ErrConflict
	}
	body, err := readMetadata(file)
	if err != nil || len(body) > maxMetadata {
		return false, ErrConflict
	}
	var flags struct {
		Version  *int  `json:"version"`
		Archived *bool `json:"archived"`
	}
	if json.Unmarshal(body, &flags) != nil || flags.Version == nil || flags.Archived == nil {
		return false, ErrConflict
	}
	state := projectState{Version: 1, Operations: map[string]archiveReceipt{}}
	if err = registryIO.unmarshal(body, &state); err != nil {
		return false, err
	}
	if state.Version != 1 || state.Operations == nil || len(state.Operations) > 10000 {
		return false, ErrConflict
	}
	current, err := registryIO.rootLstat(root, stateName)
	if err != nil || !os.SameFile(info, current) {
		return false, ErrConflict
	}
	return state.Archived, nil
}
