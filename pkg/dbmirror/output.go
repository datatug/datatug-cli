package dbmirror

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// stateDirName is the folder of the output that describes the last run: its
// report and the records that did not fit their definition. inGitDB reads no
// folder whose name starts with ".", so it is not part of the database.
const stateDirName = ".datatug-mirror"

// Output folders and files may hold personal data: only the operator reads them.
const (
	dirMode  os.FileMode = 0o700
	fileMode os.FileMode = 0o600
)

var (
	createTemp = os.CreateTemp
	renameFile = os.Rename
	removeAll  = os.RemoveAll
)

// writeFileIfChanged writes content to path atomically (a temporary file in
// the same folder, then a rename), creating folders as needed. When the file
// already holds content it is left as it is. It reports whether it wrote.
func writeFileIfChanged(path string, content []byte) (bool, error) {
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, content) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return false, err
	}
	temp, err := createTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return false, err
	}
	name := temp.Name()
	_, writeErr := temp.Write(content)
	closeErr := temp.Close()
	if err = firstError(writeErr, closeErr, os.Chmod(name, fileMode)); err == nil {
		err = renameFile(name, path)
	}
	if err != nil {
		_ = os.Remove(name)
		return false, err
	}
	return true, nil
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// existingRecords lists the document paths of the record files of spec that
// are in out now, by walking the folders the layout puts them in.
func existingRecords(out string, spec *Spec) []string {
	var found []string
	var walk func(level int, dir string, parts []string)
	walk = func(level int, dir string, parts []string) {
		col := spec.chain[level]
		recordsDir := filepath.Join(dir, col.RecordFile.RecordsBasePath())
		entries, _ := os.ReadDir(recordsDir) // a missing folder holds no record
		if level == len(spec.chain)-1 {
			for _, entry := range entries {
				name := entry.Name()
				if entry.Type().IsRegular() && !strings.HasPrefix(name, ".") && strings.HasPrefix(name, spec.prefix) && strings.HasSuffix(name, spec.suffix) && len(name) > len(spec.prefix)+len(spec.suffix) {
					id := name[len(spec.prefix) : len(name)-len(spec.suffix)]
					found = append(found, strings.Join(append(slices.Clone(parts), spec.Segments[level], id), "/"))
				}
			}
			return
		}
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			next := append(slices.Clone(parts), spec.Segments[level], entry.Name())
			walk(level+1, filepath.Join(recordsDir, entry.Name(), spec.Segments[level+1]), next)
		}
	}
	walk(0, filepath.Join(out, spec.rootRel), nil)
	return found
}
