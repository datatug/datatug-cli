package dbmirror

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/genproto/googleapis/type/latlng"
)

// TestFirestoreMirrorEmulator copies the example mirror from a real Firestore
// emulator, never a production project: run it with FIRESTORE_EMULATOR_HOST
// set to a task-owned emulator (scripts/check-watch-firestore.sh does).
// Only this test writes, to seed the emulator; the mirror itself only reads.
func TestFirestoreMirrorEmulator(t *testing.T) {
	host := os.Getenv("FIRESTORE_EMULATOR_HOST")
	if host == "" {
		if os.Getenv("FIRESTORE_QUERY_GATE_REQUIRED") == "1" {
			t.Fatal("required Firestore emulator is absent")
		}
		t.Skip("requires Firestore emulator")
	}
	if !strings.HasPrefix(host, "127.0.0.1:") && !strings.HasPrefix(host, "localhost:") && !strings.HasPrefix(host, "[::1]:") {
		t.Fatalf("FIRESTORE_EMULATOR_HOST %q is not a loopback emulator", host)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// A fresh demo project per run: its data is the emulator's own, in memory.
	project := "demo-datatug-mirror-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	client, err := firestore.NewClient(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	seed := exampleSource().docs
	seed["teams/t1"]["settings"] = map[string]any{
		"bytes": []byte{0, 1, 2}, "ref": client.Doc("teams/t2"), "geo": &latlng.LatLng{Latitude: 52.5, Longitude: 13.4},
		"nan": math.NaN(), "list": []any{int64(1), 1.5, nil, "x", map[string]any{"deep": created}},
	}
	for path, data := range seed {
		if _, err := client.Doc(path).Set(ctx, data); err != nil {
			t.Fatal(err)
		}
	}
	source, err := OpenFirestore(ctx, project, "(default)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()

	out := t.TempDir()
	report, err := Run(ctx, source, testDefinitions(t), testPolicy(t), Options{Output: out, PageSize: 2, MaxReads: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if report.Records != 9 || len(report.Rejected) != 2 || len(report.Validation) != 0 {
		t.Fatalf("%d records, rejected %+v, validation %v", report.Records, report.Rejected, report.Validation)
	}
	got := readFile(t, filepath.Join(out, "teams", "t1.json"))
	for _, want := range []string{`"$bytes": "AAEC"`, `"$ref": "teams/t2"`, `"latitude": 52.5`, `"$float": "NaN"`, `"deep": "2026-11-01T09:00:00Z"`, `"score": 2.0`, `"createdAt": "2026-11-01T09:00:00Z"`} {
		if !strings.Contains(got, want) {
			t.Errorf("teams/t1.json lacks %s:\n%s", want, got)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "teams/t1/ext/cal/events/e1/notes/n1.json")); err != nil {
		t.Error(err)
	}
	if r := result(t, report, "teams/ext/events"); r.BelowMissingParent != 2 {
		t.Errorf("%+v", r)
	}
	// One space-like slice, a dry run, and a re-run that changes nothing.
	under, err := Run(ctx, source, testDefinitions(t), testPolicy(t), Options{Output: t.TempDir(), Under: "teams/t1"})
	if err != nil || under.Records != 5 || under.Reads > 10 {
		t.Fatalf("%v %+v", err, under)
	}
	dry, err := Run(ctx, source, testDefinitions(t), testPolicy(t), Options{DryRun: true})
	if err != nil || *result(t, dry, "teams").Count != 3 {
		t.Fatalf("%v %+v", err, dry)
	}
	again, err := Run(ctx, source, testDefinitions(t), testPolicy(t), Options{Output: out, PageSize: 2})
	if err != nil || again.Written != 0 || again.Unchanged != report.Records {
		t.Fatalf("%v: wrote %d, kept %d", err, again.Written, again.Unchanged)
	}
}
