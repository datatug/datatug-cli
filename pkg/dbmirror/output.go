package dbmirror

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"unicode/utf16"
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
	createTemp = func(dir, pattern string) (*os.File, error) { return outFS.CreateTemp(dir, pattern) }
	renameFile = func(from, to string) error { return outFS.Rename(from, to) }
	removeAll  = func(path string) error { return outFS.RemoveAll(path) }
	removeFile = func(path string) error { return outFS.Remove(path) }
)

// writeFileIfChanged writes content to path atomically (a temporary file in
// the same folder, then a rename), creating folders as needed. When the file
// already holds content it is left as it is. It reports whether it wrote.
func writeFileIfChanged(path string, content []byte) (bool, error) {
	if current, err := outFS.ReadFile(path); err == nil && bytes.Equal(current, content) {
		return false, nil
	}
	if err := outFS.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return false, err
	}
	temp, err := createTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return false, err
	}
	name := temp.Name()
	_, writeErr := temp.Write(content)
	closeErr := temp.Close()
	if err = firstError(writeErr, closeErr, outFS.Chmod(name, fileMode)); err == nil {
		err = renameFile(name, path)
	}
	if err != nil {
		_ = outFS.Remove(name)
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
// are in out now, by walking the folders the layout puts them in. A name that
// encodeID does not write (a hidden file, a temporary file) is no record.
func existingRecords(out string, spec *Spec) []string {
	var found []string
	var walk func(level int, dir string, parts []string)
	walk = func(level int, dir string, parts []string) {
		col := spec.chain[level]
		recordsDir := filepath.Join(dir, col.RecordFile.RecordsBasePath())
		entries, _ := outFS.ReadDir(recordsDir) // a missing folder holds no record
		if level == len(spec.chain)-1 && spec.multi {
			records, err := readRecords(filepath.Join(recordsDir, spec.Def.RecordFile.Name), spec)
			if err != nil {
				return // a file the mirror cannot read: left as it is (a run that reads its records fails on it)
			}
			for _, id := range slices.Sorted(maps.Keys(records)) {
				found = append(found, strings.Join(append(slices.Clone(parts), spec.Segments[level], id), "/"))
			}
			return
		}
		if level == len(spec.chain)-1 {
			for _, entry := range entries {
				name := entry.Name()
				if !entry.Type().IsRegular() || !strings.HasPrefix(name, spec.prefix) || !strings.HasSuffix(name, spec.suffix) || len(name) <= len(spec.prefix)+len(spec.suffix) {
					continue
				}
				if id, ok := keyEncoding.Decode(name[len(spec.prefix) : len(name)-len(spec.suffix)]); ok {
					found = append(found, strings.Join(append(slices.Clone(parts), spec.Segments[level], id), "/"))
				}
			}
			return
		}
		for _, entry := range entries {
			id, ok := keyEncoding.Decode(entry.Name())
			if !entry.IsDir() || !ok {
				continue
			}
			next := append(slices.Clone(parts), spec.Segments[level], id)
			walk(level+1, filepath.Join(recordsDir, entry.Name(), spec.Segments[level+1]), next)
		}
	}
	walk(0, filepath.Join(out, spec.rootRel), nil)
	return found
}

// maxPathChars is the longest path of a file the output writes: Windows
// counts 259 characters (UTF-16 code units) without long-path support, Linux
// and macOS a little more than 1000 bytes at the least.
var maxPathChars = map[bool]int{true: 259, false: 1023}[runtime.GOOS == "windows"]

// pathTooLong is why a file cannot be written at path, or "".
func pathTooLong(path string) string {
	if n := len(utf16.Encode([]rune(path))); n > maxPathChars {
		return fmt.Sprintf("its file path would be %d characters long; this system writes at most %d", n, maxPathChars)
	}
	return ""
}
