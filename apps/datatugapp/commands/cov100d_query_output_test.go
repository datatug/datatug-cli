package commands

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
)

// cov100dFailWriter accepts the first `after` Write calls and fails every
// later one, so each write site in the output helpers can be failed in turn.
type cov100dFailWriter struct {
	after int
	calls int
}

func (w *cov100dFailWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls > w.after {
		return 0, errors.New("cov100d write failed")
	}
	return len(p), nil
}

// cov100dReader is a scripted dal.RecordsReader.
type cov100dReader struct {
	recs []record.Record
	err  error
}

func (r *cov100dReader) Next() (record.Record, error) {
	if len(r.recs) > 0 {
		rec := r.recs[0]
		r.recs = r.recs[1:]
		return rec, nil
	}
	if r.err != nil {
		return nil, r.err
	}
	return nil, dal.ErrNoMoreRecords
}
func (r *cov100dReader) Cursor() (string, error) { return "", nil }
func (r *cov100dReader) Close() error            { return nil }

func cov100dRec(id string, data any) record.Record {
	return record.NewRecordWithData(record.NewKeyWithID("c", id), data)
}

func TestCov100dCollectRowsErrors(t *testing.T) {
	if rows, err := collectRows(nil); rows != nil || err != nil {
		t.Fatalf("nil reader: %v %v", rows, err)
	}
	_, err := collectRows(&cov100dReader{recs: []record.Record{cov100dRec("1", map[string]any{"bad": make(chan int)})}})
	if err == nil || !strings.Contains(err.Error(), "record ") {
		t.Fatalf("unserialisable data: %v", err)
	}
	boom := errors.New("boom")
	if _, err := collectRows(&cov100dReader{err: boom}); !errors.Is(err, boom) {
		t.Fatalf("next error: %v", err)
	}
}

func TestCov100dWriteJSONRowsFailures(t *testing.T) {
	rows := []queryRow{{key: "1", data: map[string]any{"a": 1}}, {key: "2", data: map[string]any{}}}
	for after := 0; after < 4; after++ {
		if err := writeJSONRows(&cov100dFailWriter{after: after}, []string{"a"}, rows, true, nil); err == nil {
			t.Fatalf("array after=%d: expected error", after)
		}
	}
	if err := writeJSONRows(&cov100dFailWriter{}, []string{"a"}, rows, false, nil); err == nil {
		t.Fatal("jsonl: expected error")
	}
	var out bytes.Buffer
	if err := writeJSONRows(&out, []string{"a"}, rows, false, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `{"$key":"2"}`) {
		t.Fatalf("missing column must be skipped: %s", out.String())
	}
}

func TestCov100dWriteJSONFieldUnmarshalable(t *testing.T) {
	var b strings.Builder
	writeJSONField(&b, "x", make(chan int))
	if !strings.HasPrefix(b.String(), `"x":"0x`) {
		t.Fatalf("fallback encoding: %s", b.String())
	}
}

type cov100dBadYAML struct{}

func (cov100dBadYAML) MarshalYAML() (any, error) { return nil, errors.New("nope") }

func TestCov100dWriteYAMLRowsFailures(t *testing.T) {
	bad := []queryRow{{key: "1", data: map[string]any{"a": cov100dBadYAML{}}}}
	if err := writeYAMLRows(io.Discard, []string{"a"}, bad); err == nil {
		t.Fatal("expected encode error for func value")
	}
	ok := []queryRow{{key: "1", data: map[string]any{"a": 1}}}
	if err := writeYAMLRows(&cov100dFailWriter{}, []string{"a"}, ok); err == nil {
		t.Fatal("expected write error")
	}
	// The encoder may buffer until Close; fail every write and also every
	// later one so both Encode and Close paths are reachable.
	for after := 0; after < 3; after++ {
		w := &cov100dFailWriter{after: after}
		err := writeYAMLRows(w, []string{"a"}, ok)
		if after >= w.calls {
			// The writer was never asked to fail, so the write must succeed.
			if err != nil {
				t.Fatalf("after=%d: unexpected error %v", after, err)
			}
			continue
		}
		if err == nil {
			t.Fatalf("after=%d: expected a write error", after)
		}
	}
	var out bytes.Buffer
	if err := writeYAMLRows(&out, []string{"a"}, ok); err != nil {
		t.Fatalf("succeeding writer: %v", err)
	}
	if got, want := out.String(), "- $key: \"1\"\n  a: 1\n"; got != want {
		t.Fatalf("yaml output %q, want %q", got, want)
	}
}

func TestCov100dWriteCSVFailures(t *testing.T) {
	huge := strings.Repeat("x", 9000)
	hugeRow := queryRow{key: "1", data: map[string]any{"a": huge}}
	if err := writeCSVRows(&cov100dFailWriter{}, []string{huge}, nil); err == nil {
		t.Fatal("csv header write must fail")
	}
	if err := writeCSVRows(&cov100dFailWriter{}, []string{"a"}, []queryRow{hugeRow}); err == nil {
		t.Fatal("csv row write must fail")
	}
	if err := writeCSVHeader(&cov100dFailWriter{}, []string{huge}); err == nil {
		t.Fatal("csv stream header write must fail")
	}
	if err := writeCSVDataRow(&cov100dFailWriter{}, []string{"a"}, hugeRow); err == nil {
		t.Fatal("csv stream row write must fail")
	}
}

func TestCov100dWriteGridFailures(t *testing.T) {
	rows := []queryRow{{key: "1", data: map[string]any{"a": "x"}}}
	if err := writeGridRows(&cov100dFailWriter{}, []string{"a"}, rows); err == nil {
		t.Fatal("grid header write must fail")
	}
	if err := writeGridRows(&cov100dFailWriter{after: 1}, []string{"a"}, rows); err == nil {
		t.Fatal("grid row write must fail")
	}
}
