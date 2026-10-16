package queryresults

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	store := newStore(t)
	c := NewCollector(5)
	c.Add("1", map[string]any{"n": int64(7)})
	data := c.Build("t1", []string{"n"}, nil)
	result, err := store.Create("t1")
	if err != nil {
		t.Fatal(err)
	}
	meta := NewMeta("t1", "v", data)
	if err := result.Write(data.Bytes, meta.Encode()); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("t1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Meta.ID != "t1" || len(loaded.Recordset.Rows) != 1 || loaded.Recordset.Rows[0][1].Str != "7" || len(loaded.DataJSON) != len(data.Bytes) {
		t.Fatalf("loaded %+v", loaded)
	}
	_, err = store.Load("nope")
	reasonIs(t, err, ReasonNotFound)

	dir := filepath.Join(store.Root(), "t1")
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	original, _ := os.ReadFile(filepath.Join(dir, MetaFile))
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	write(MetaFile, `{"format":"datatug.query-result.meta","version":2,"expiresAt":"`+expires+`","dataBytes":`+strconv.Itoa(len(data.Bytes))+`}`)
	_, err = store.Load("t1")
	reasonIs(t, err, ReasonVersion)
	write(MetaFile, `{"version":[],"expiresAt":"`+expires+`","dataBytes":`+strconv.Itoa(len(data.Bytes))+`}`)
	_, err = store.Load("t1")
	reasonIs(t, err, ReasonIncomplete)
	write(MetaFile, string(original))

	write(DataFile, strings.Repeat(" ", len(data.Bytes)))
	_, err = store.Load("t1")
	reasonIs(t, err, ReasonIncomplete)
	padded := strings.Replace(string(data.Bytes), `"version":1`, `"version":3`, 1)
	write(DataFile, padded)
	_, err = store.Load("t1")
	reasonIs(t, err, ReasonVersion)

	calls := 0
	swap(t, &osOpen, func(name string) (*os.File, error) {
		calls++
		if calls == 2 {
			return nil, errBoom
		}
		return os.Open(name)
	})
	_, err = store.Load("t1")
	reasonIs(t, err, ReasonIncomplete)
	calls = 10
	osOpen = func(name string) (*os.File, error) {
		calls++
		if calls == 13 {
			return nil, errBoom
		}
		return os.Open(name)
	}
	write(DataFile, string(data.Bytes))
	_, err = store.Load("t1")
	reasonIs(t, err, ReasonIncomplete)
}
