package privateadmission

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var physicalAbs = filepath.Abs
var physicalEvalSymlinks = filepath.EvalSymlinks
var physicalStat = os.Stat
var physicalReadDir = os.ReadDir

// PhysicalDirectory refuses alternate physical spellings of the same directory
// on case-insensitive or normalization-insensitive filesystems. Existing native
// journal hashes retain their canonical pathname; aliases cannot create a second
// admission lock or silently move previously accepted work to another store.
func PhysicalDirectory(path string) (string, error) {
	absolute, err := physicalAbs(path)
	if err != nil {
		return "", err
	}
	resolved, err := physicalEvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	volume := filepath.VolumeName(resolved)
	current := volume + string(filepath.Separator)
	rest := strings.TrimPrefix(resolved, current)
	for _, part := range strings.Split(rest, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		wanted, err := physicalStat(filepath.Join(current, part))
		if err != nil {
			return "", err
		}
		if !wanted.IsDir() {
			return "", errors.New("private admission path is not a directory")
		}
		entries, err := physicalReadDir(current)
		if err != nil {
			return "", err
		}
		actual := ""
		for _, entry := range entries {
			if entry.Name() == part {
				actual = part
				break
			}
		}
		if actual == "" {
			for _, entry := range entries {
				info, statErr := physicalStat(filepath.Join(current, entry.Name()))
				if statErr == nil && os.SameFile(wanted, info) {
					actual = entry.Name()
					break
				}
			}
		}
		if actual == "" || actual != part {
			return "", errors.New("use canonical directory spelling for private admission")
		}
		current = filepath.Join(current, actual)
	}
	return current, nil
}
