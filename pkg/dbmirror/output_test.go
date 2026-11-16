package dbmirror

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ingitdb/ingitdb-go/ingitdb"
)

func TestWriteFileIfChanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b.json")
	if wrote, err := writeFileIfChanged(path, []byte("x")); err != nil || !wrote {
		t.Fatal(wrote, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != fileMode {
		t.Fatalf("%v %v", info, err)
	}
	if wrote, err := writeFileIfChanged(path, []byte("x")); err != nil || wrote {
		t.Fatal("rewrote the same bytes")
	}
	if wrote, err := writeFileIfChanged(path, []byte("y")); err != nil || !wrote || readFile(t, path) != "y" {
		t.Fatal(wrote, err)
	}
	withSeam(t, &createTemp, func(string, string) (*os.File, error) { return nil, errBoom })
	if _, err := writeFileIfChanged(path, []byte("z")); err != errBoom {
		t.Error(err)
	}
	withSeam(t, &createTemp, func(dir, pattern string) (*os.File, error) {
		file, err := os.CreateTemp(dir, pattern)
		if err == nil {
			_ = file.Close() // a closed file: Write and Close fail
		}
		return file, err
	})
	if _, err := writeFileIfChanged(path, []byte("z")); err == nil {
		t.Error("no write error")
	}
	withSeam(t, &createTemp, os.CreateTemp)
	withSeam(t, &renameFile, func(string, string) error { return errBoom })
	if _, err := writeFileIfChanged(path, []byte("z")); err != errBoom {
		t.Error(err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("a temporary file was left: %v", entries)
	}
}

func TestExistingRecords(t *testing.T) {
	records := "$records"
	rf := func(dir *string) *ingitdb.RecordFileDef {
		return &ingitdb.RecordFileDef{Name: "r-{key}.json", Format: "json", RecordType: ingitdb.SingleRecord, RecordsDir: dir}
	}
	spec := &Spec{ID: "a/b", Segments: []string{"a", "b"}, rootRel: "a", prefix: "r-", suffix: ".json",
		chain: []*ingitdb.CollectionDef{{RecordFile: rf(&records)}, {RecordFile: rf(nil)}}}
	out := t.TempDir()
	for _, file := range []string{"a/$records/1/b/$records/r-x.json", "a/$records/1/b/$records/.r-h.json", "a/$records/1/b/$records/other.json",
		"a/$records/1/b/$records/r-.json", "a/$records/.hidden/b/$records/r-y.json", "a/$records/r-1.json"} {
		if err := os.MkdirAll(filepath.Join(out, filepath.Dir(file)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, file), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(out, "a/$records/1/b/$records/r-dir.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := existingRecords(out, spec); !slices.Equal(got, []string{"a/1/b/x"}) {
		t.Errorf("%v", got)
	}
}
