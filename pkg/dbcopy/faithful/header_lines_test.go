package faithful

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// A header line is never cut: a licence or attribution that would be shortened is refused as a usage error (exit 2), in
// every dialect, before anything is written. A value whose line fits is written byte for byte as before.
func TestHeaderValuesThatWouldBeCutAreRefused(t *testing.T) {
	// "Licence: " is 9 bytes, "Attribution: " 13 and "Dataset: " 9; an edition is the whole line.
	fields := []struct {
		param  string
		prefix int
		set    func(h *Header, v string)
	}{
		{"license", len("Licence: "), func(h *Header, v string) { h.License = v }},
		{"attribution", len("Attribution: "), func(h *Header, v string) { h.Attribution = v }},
		{"edition", 0, func(h *Header, v string) { h.Edition = v }},
		{"name", len("Dataset: "), func(h *Header, v string) { h.Name = v }},
	}
	for _, dialect := range registeredDialects() {
		for _, f := range fields {
			// A line of exactly headerLineMax bytes is written whole, one byte more is refused. A name or an edition is
			// also bounded by the marker row (255 characters), so the multi-byte cases use 2-byte letters.
			room := headerLineMax - f.prefix
			for _, tc := range []struct {
				name  string
				value string
				bad   bool
			}{
				{"fits exactly", strings.Repeat("é", room/2), false},
				{"one byte over", strings.Repeat("é", room/2) + "ab", true},
				{"over by a letter", strings.Repeat("é", room/2+1), true},
				{"short", "Example", false},
				{"blank runs count as one space", "a" + strings.Repeat(" \t\n", 70) + "b", false},
			} {
				if (f.param == "license" || f.param == "attribution") && tc.name == "far over" {
					continue
				}
				e := sqliteWith(t, "CREATE TABLE t (a INTEGER)")
				h := testHeader()
				f.set(&h, tc.value)
				var buf bytes.Buffer
				_, err := ExportScript(context.Background(), ScriptRequest{Dialect: dialect, DB: e.DB, Header: h}, &buf)
				var usage *ScriptUsageError
				if tc.bad {
					if !errors.As(err, &usage) || !strings.Contains(err.Error(), f.param) {
						t.Errorf("%s/%s/%s: err %v, want a usage error naming the parameter", dialect, f.param, tc.name, err)
					}
					if buf.Len() != 0 {
						t.Errorf("%s/%s/%s: %d bytes written before the refusal", dialect, f.param, tc.name, buf.Len())
					}
					continue
				}
				if err != nil {
					t.Errorf("%s/%s/%s: %v", dialect, f.param, tc.name, err)
				}
				if strings.Contains(buf.String(), "...\n") {
					t.Errorf("%s/%s/%s: a header line was cut", dialect, f.param, tc.name)
				}
				if !strings.Contains(buf.String(), strings.Join(strings.Fields(tc.value), " ")) {
					t.Errorf("%s/%s/%s: the value is not in the script whole", dialect, f.param, tc.name)
				}
			}
		}
	}
	// The licence is written whole when it is as long as a line holds: the licence of a dataset is exactly what must not be shortened.
	e := sqliteWith(t, "CREATE TABLE t (a INTEGER)")
	h := testHeader()
	h.License = strings.Repeat("L", headerLineMax-len("Licence: "))
	var buf bytes.Buffer
	if _, err := ExportScript(context.Background(), ScriptRequest{Dialect: "mysql", DB: e.DB, Header: h}, &buf); err != nil || !strings.Contains(buf.String(), "-- Licence: "+h.License+"\n") {
		t.Errorf("a licence of a full line: %v", err)
	}
}

func TestValidateHeaderLinesMessagePointsAtTheNoticeFile(t *testing.T) {
	err := ValidateHeaderLines(Header{License: strings.Repeat("x", 500)})
	if err == nil {
		t.Fatal("a 500 byte licence is accepted")
	}
	for _, want := range []string{"license", "cut", "509 bytes", "at most 400", "notice-file", "16384"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q lacks %q", err, want)
		}
	}
	if err := ValidateHeaderLines(Header{License: strings.Repeat("x", 391), Attribution: strings.Repeat("y", 387), Edition: strings.Repeat("z", 400), Name: strings.Repeat("n", 391)}); err != nil {
		t.Errorf("lines of exactly 400 bytes are refused: %v", err)
	}
}

// A shorter header is written exactly as before: the cut still applies to the machine-made lines (warnings, the
// list of tables), which no request can shorten.
func TestMachineMadeHeaderLinesAreStillCut(t *testing.T) {
	if got := commentText(strings.Repeat("x", 500)); len(got) != headerLineMax+3 || !strings.HasSuffix(got, "...") {
		t.Errorf("%d bytes: %.20q", len(got), got)
	}
	if got := commentText(strings.Repeat("x", 400)); len(got) != 400 {
		t.Errorf("a line of 400 bytes is cut: %d", len(got))
	}
}
