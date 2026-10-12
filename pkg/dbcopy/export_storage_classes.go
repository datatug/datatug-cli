package dbcopy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dal-go/dalgo/dbschema"
)

const maxStorageClassFileBytes = 20 * 1024 * 1024

// storageClassWriter retains SQLite's per-cell physical class for exact
// reconstruction of decimal-string transport. It is sparse and rotates before
// any sidecar reaches 20 MiB. Every line is keyed by the native record ID.
type storageClassWriter struct {
	dir    string
	files  []string
	file   *os.File
	buffer *bufio.Writer
	size   int64
	closed bool
}

func newStorageClassWriter(dir string) *storageClassWriter { return &storageClassWriter{dir: dir} }

func (w *storageClassWriter) Append(id string, fields []dbschema.FieldDef, classes map[string]string) error {
	if len(classes) == 0 {
		return nil
	}
	relevant := map[string]string{}
	for _, field := range fields {
		if field.Type != dbschema.Decimal {
			continue
		}
		if class := classes[string(field.Name)]; class != "" && class != "null" {
			relevant[string(field.Name)] = class
		}
	}
	if len(relevant) == 0 {
		return nil
	}
	line, err := json.Marshal(struct {
		ID      string            `json:"id"`
		Classes map[string]string `json:"classes"`
	}{id, relevant})
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if w.file == nil || (w.size > 0 && w.size+int64(len(line)) > maxStorageClassFileBytes) {
		if err := w.rotate(); err != nil {
			return err
		}
	}
	n, err := w.buffer.Write(line)
	w.size += int64(n)
	return err
}

func (w *storageClassWriter) rotate() error {
	if err := w.finishFile(); err != nil {
		return err
	}
	name := fmt.Sprintf("source-storage-%04d.jsonl", len(w.files)+1)
	file, err := os.OpenFile(filepath.Join(w.dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	w.files = append(w.files, name)
	w.file = file
	w.buffer = bufio.NewWriterSize(file, 128*1024)
	w.size = 0
	return nil
}

func (w *storageClassWriter) finishFile() error {
	if w.file == nil {
		return nil
	}
	if err := w.buffer.Flush(); err != nil {
		return err
	}
	if err := w.file.Sync(); err != nil {
		return err
	}
	if err := w.file.Close(); err != nil {
		return err
	}
	w.file, w.buffer = nil, nil
	return nil
}

func (w *storageClassWriter) Close() ([]string, error) {
	if !w.closed {
		if err := w.finishFile(); err != nil {
			return nil, err
		}
		w.closed = true
	}
	return w.files, nil
}
