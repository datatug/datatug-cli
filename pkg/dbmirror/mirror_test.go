package dbmirror

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ingitdb/ingitdb-go/ingitdb"
)

func runExample(t *testing.T, src Source, opts Options) (*Report, string) {
	t.Helper()
	if opts.Output == "" && !opts.DryRun {
		opts.Output = t.TempDir()
	}
	report, err := Run(context.Background(), src, testDefinitions(t), testPolicy(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	return report, opts.Output
}

// claimed writes the output state of a mirror of source into out.
func claimed(t *testing.T, out string, source SourceIdentity) {
	t.Helper()
	content, _ := json.Marshal(outputState{Format: outputFormat, Source: source, Encoding: keyEncoding.Name()})
	if err := os.MkdirAll(filepath.Join(out, stateDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outputFile(out), content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func runWith(t *testing.T, src Source, policy *Policy, opts Options) *Report {
	t.Helper()
	report, err := Run(context.Background(), src, testDefinitions(t), policy, opts)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func result(t *testing.T, report *Report, id string) CollectionResult {
	t.Helper()
	for _, r := range report.Collections {
		if r.Collection == id {
			return r
		}
	}
	t.Fatalf("no result for %s", id)
	return CollectionResult{}
}

// allBytes is every file of a folder, for a check that a value is in none.
func allBytes(t *testing.T, dir string) string {
	t.Helper()
	var all strings.Builder
	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && entry.Type().IsRegular() {
			all.WriteString(path + "\n" + readFile(t, path))
		}
		return nil
	})
	return all.String()
}

func TestRunWritesTheMirrorLayout(t *testing.T) {
	src := exampleSource()
	var log, progress bytes.Buffer
	report, out := runExample(t, src, Options{Source: "env:SOURCE", Log: &log, Progress: &progress, PageSize: 2})

	// Records sit exactly where the definitions put them, at any depth, also below a missing parent.
	for _, file := range []string{"teams/t1.json", "teams/t1x.json", "teams/t1/ext/cal.json", "teams/t1/ext/cal/events/e1.json",
		"teams/t1/ext/cal/events/e1/notes/n1.json", "teams/t1/ext/gone/events/e2.json", "teams/t1x/ext/cal/events/e3.json",
		"teams/t2/ext/cal/events/~2Ehidden.json", "ext/crm/customers/$records/c1.json"} {
		if _, err := os.Stat(filepath.Join(out, file)); err != nil {
			t.Errorf("%s: %v", file, err)
		}
	}
	// Lossless and deterministic values; a timestamp has 6 fraction digits.
	if got := readFile(t, filepath.Join(out, "teams/t1.json")); got != `{
  "createdAt": "2026-11-01T09:00:00.000000Z",
  "score": 2.0,
  "settings": {
    "a": null,
    "z": true
  },
  "size": 3,
  "tags": [
    "a",
    "b"
  ],
  "title": "Team one"
}
` {
		t.Errorf("teams/t1.json:\n%s", got)
	}
	// Masks: strings become ***, other values go.
	if got := readFile(t, filepath.Join(out, "ext/crm/customers/$records/c1.json")); !strings.Contains(got, `"email": "***"`) || !strings.Contains(got, `"contacts": []`) ||
		strings.Contains(got, "someone@example.com") || strings.Contains(got, "+000") || strings.Contains(got, `"work"`) {
		t.Errorf("masked customer:\n%s", got)
	}
	// The definitions are copied; the mirror's own example records are not.
	if _, err := os.Stat(filepath.Join(out, "teams/.collection/subcollections/ext/definition.yaml")); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(out, "teams/t0.json")); !os.IsNotExist(err) {
		t.Errorf("an example record of the mirror was copied: %v", err)
	}
	// Excluded and undeclared collections are never read.
	for _, q := range src.queries {
		if q.Collection == "secrets" || q.Collection == "loose" || q.Collection == "bundles" || q.Collection == "logs" {
			t.Errorf("read %+v", q)
		}
	}
	if r := result(t, report, "ext/secrets"); r.Action != ActionExclude || r.Selected {
		t.Errorf("%+v", r)
	}
	if r := result(t, report, "ext/loose"); r.Action != ActionUndeclared || r.Selected {
		t.Errorf("%+v", r)
	}
	if r := result(t, report, "ext/bundles"); r.Action != ActionUnsupported || !strings.Contains(r.Note, "several records") {
		t.Errorf("%+v", r)
	}
	if r := result(t, report, "ext/customers"); r.Action != ActionMask || !slices.Equal(r.Masked, []string{"contacts", "email", "name"}) {
		t.Errorf("%+v", r)
	}
	events := result(t, report, "teams/ext/events")
	if events.Records != 4 || events.Rejected != 1 || events.BelowMissingParent != 3 || events.Complete {
		t.Errorf("%+v", events)
	}
	if r := result(t, report, "teams"); r.Records != 3 || !r.Complete || r.Query != "collection teams" {
		t.Errorf("%+v", r)
	}
	if !slices.Equal(report.UndefinedRootCollections, []string{"other"}) {
		t.Errorf("%v", report.UndefinedRootCollections)
	}
	if len(report.Outside) != 2 || report.Outside[0] != (OutsideRead{Collection: "other/events", Documents: 1}) || report.Outside[1].Collection != "other/notes" {
		t.Errorf("%+v", report.Outside)
	}
	if len(report.Rejected) != 1 || report.Rejected[0].Path != "teams/t2/ext/cal/events/bad" || !strings.Contains(report.Rejected[0].Reasons[0], "wrong type") ||
		!slices.Equal(report.Rejected[0].Fields, []string{"title"}) {
		t.Errorf("%+v", report.Rejected)
	}
	if _, err := os.Stat(filepath.Join(out, stateDirName, "rejected")); !os.IsNotExist(err) {
		t.Errorf("a rejected record was written by default: %v", err)
	}
	if len(report.Validation) != 0 {
		t.Errorf("validation: %v", report.Validation)
	}
	if report.Complete || len(report.Incomplete) != 3 {
		t.Errorf("incomplete: %v", report.Incomplete)
	}
	// The report file is deterministic: no time, no host.
	var saved Report
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(out, stateDirName, "report.json"))), &saved); err != nil || saved.Source != "env:SOURCE" || saved.Records != report.Records {
		t.Errorf("report file: %v %+v", err, saved)
	}
	for _, want := range []string{"rejected: teams/t2/ext/cal/events/bad", "undefined   other", "read, not copied: 1 document(s) of other/events (not defined in the mirror)", "below a parent document that does not exist", "incomplete: "} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, log.String())
		}
	}
	if !strings.Contains(progress.String(), "collection group events: 2 documents read") {
		t.Errorf("progress:\n%s", progress.String())
	}

	// A re-run over unchanged data changes no file; a deleted document is reported, not removed.
	reportBytes := readFile(t, filepath.Join(out, stateDirName, "report.json"))
	delete(src.docs, "teams/t2")
	again, _ := runExample(t, src, Options{Output: out, Source: "env:SOURCE"})
	if again.Written != 0 || again.Unchanged != report.Records-1 {
		t.Errorf("re-run wrote %d, kept %d", again.Written, again.Unchanged)
	}
	if !slices.Equal(again.Stale, []string{"teams/t2.json"}) || len(again.Pruned) != 0 {
		t.Errorf("stale: %v, pruned %v", again.Stale, again.Pruned)
	}
	if _, err := os.Stat(filepath.Join(out, "teams/t2.json")); err != nil {
		t.Error("a stale file was deleted without --prune")
	}
	src.docs["teams/t2"] = map[string]any{"title": "Team two", "createdAt": created}
	third, _ := runExample(t, src, Options{Output: out, Source: "env:SOURCE", PageSize: 2})
	if third.Written != 0 || readFile(t, filepath.Join(out, stateDirName, "report.json")) != reportBytes {
		t.Error("the same source gave another report")
	}
}

// B1: a rejected document's values never reach the disk or the log, unless asked.
func TestRunRejectedDocumentsKeepNoValues(t *testing.T) {
	src := newMemSource()
	src.docs["teams/t3"] = map[string]any{"title": "x", "createdAt": created, "phone": "+353-PII", "kind": "SECRET-KIND"}
	src.docs["teams/t4"] = map[string]any{"title": "y", "createdAt": "not a time PERSONAL-123"}
	var log bytes.Buffer
	report, out := runExample(t, src, Options{Collections: []string{"teams"}, Log: &log})
	if len(report.Rejected) != 2 {
		t.Fatalf("%+v", report.Rejected)
	}
	t3 := report.Rejected[0]
	if t3.Path != "teams/t3" || !slices.Equal(t3.Fields, []string{"kind"}) || !strings.Contains(strings.Join(t3.Reasons, ";"), "1 field(s) the definition does not declare") {
		t.Errorf("%+v", t3)
	}
	if t4 := report.Rejected[1]; !slices.Equal(t4.Fields, []string{"createdAt"}) || !strings.Contains(t4.Reasons[0], "not a valid datetime") {
		t.Errorf("%+v", t4)
	}
	for _, secret := range []string{"+353-PII", "SECRET-KIND", "PERSONAL-123", "phone"} {
		if strings.Contains(allBytes(t, out), secret) || strings.Contains(log.String(), secret) {
			t.Errorf("%s reached the output or the log", secret)
		}
	}
	// --keep-rejected keeps the (masked) record, under the output's state folder.
	report, out = runExample(t, src, Options{Collections: []string{"teams"}, KeepRejected: true})
	if got := readFile(t, filepath.Join(out, stateDirName, "rejected", "teams", "t3.json")); !strings.Contains(got, "+353-PII") || len(report.Rejected) != 2 {
		t.Errorf("%s", got)
	}
	// A run without it clears what an earlier run kept.
	runExample(t, src, Options{Output: out, Collections: []string{"teams"}})
	if _, err := os.Stat(filepath.Join(out, stateDirName, "rejected")); !os.IsNotExist(err) {
		t.Error("rejected records of an earlier run were left")
	}
}

// M1: paths pin a collection to its parents; other parents are read but never written or dumped.
func TestRunPinsCollectionsToTheirPaths(t *testing.T) {
	src := exampleSource()
	src.docs["teams/t1/ext/other"] = map[string]any{"data": map[string]any{"phone": "+353-PII"}}
	src.docs["teams/t1/ext/gone/events/e2"] = map[string]any{"title": "an event of another module", "secret": "+353-PII"}
	policy := testPolicy(t)
	policy.Paths = map[string][]string{
		"teams/ext":              {"teams/*/ext/cal"},
		"teams/ext/events":       {"teams/*/ext/cal/events"},
		"teams/ext/events/notes": {"teams/*/ext/cal/events/*/notes"},
	}
	out := t.TempDir()
	report := runWith(t, src, policy, Options{Output: out})
	ext, events := result(t, report, "teams/ext"), result(t, report, "teams/ext/events")
	if ext.Records != 1 || ext.OtherParents != 1 || !slices.Equal(ext.OtherParentPaths, []string{"teams/*/ext/*"}) {
		t.Errorf("%+v", ext)
	}
	if events.Records != 3 || events.OtherParents != 1 || !slices.Equal(events.OtherParentPaths, []string{"teams/*/ext/gone/events/*"}) || events.Rejected != 1 {
		t.Errorf("%+v", events)
	}
	if strings.Contains(allBytes(t, out), "+353-PII") {
		t.Error("a document at another parent reached the output")
	}
	// With the parents fixed by --path, the reads name the collections: nothing of another parent or sibling is read.
	src = exampleSource()
	src.docs["teams/t1/ext/other/events/x"] = map[string]any{"title": "not read"}
	report = runWith(t, src, policy, Options{Output: t.TempDir(), Under: "teams/t1"})
	for _, q := range src.queries {
		if q.Group && (q.Collection != "notes" || q.Under != "teams/t1/ext/cal") {
			t.Errorf("a group query: %+v", q)
		}
	}
	if !slices.Equal(src.gets, []string{"teams/t1", "teams/t1/ext/cal"}) {
		t.Errorf("gets %v", src.gets)
	}
	if r := result(t, report, "teams/ext/events"); r.Records != 1 || r.OtherParents != 0 || r.Query != "collection teams/t1/ext/cal/events" {
		t.Errorf("%+v", r)
	}
	if len(report.Outside) != 0 {
		t.Errorf("%+v", report.Outside)
	}
	// A path no pattern allows selects nothing.
	if _, err := Run(context.Background(), exampleSource(), testDefinitions(t), policy, Options{DryRun: true, Under: "teams/t1/ext/other", Collections: []string{"teams/ext/**"}}); !errors.Is(err, ErrUsage) {
		t.Error(err)
	}
	// A pattern that --path rules out plans no read.
	policy.Paths["teams/ext/events"] = []string{"teams/*/ext/cal/events", "teams/t1/ext/*/events"}
	report = runWith(t, exampleSource(), policy, Options{Output: t.TempDir(), Under: "teams/t2"})
	if r := result(t, report, "teams/ext/events"); r.Query != "collection teams/t2/ext/cal/events" || r.Records != 1 {
		t.Errorf("%+v", r)
	}
	// Two patterns that need the same read share it.
	policy.Paths["teams/ext/events"] = []string{"teams/*/ext/cal/events", "teams/*/ext/gone/events"}
	report = runWith(t, exampleSource(), policy, Options{DryRun: true})
	if r := result(t, report, "teams/ext/events"); r.Query != "collection group events" || *r.Count != 6 {
		t.Errorf("%+v", r)
	}
	// Two patterns of one collection, with a read each, and a document read by both is counted once.
	policy.Paths["teams/ext/events"] = []string{"teams/*/ext/cal/events", "teams/t1/ext/*/events"}
	src = exampleSource()
	report = runWith(t, src, policy, Options{Output: t.TempDir()})
	if r := result(t, report, "teams/ext/events"); r.Records != 4 || r.Rejected != 1 || r.OtherParents != 0 || r.Query != "collection group events; collection group events under teams/t1" {
		t.Errorf("%+v", r)
	}
	// A dry run adds up the counts of the reads of a collection.
	report = runWith(t, exampleSource(), policy, Options{DryRun: true})
	if r := result(t, report, "teams/ext/events"); *r.Count != 6+2 {
		t.Errorf("%+v", r)
	}
}

func TestRunUnderOneDocument(t *testing.T) {
	src := exampleSource()
	report, out := runExample(t, src, Options{Under: "teams/t1"})
	if !slices.Equal(src.gets, []string{"teams/t1"}) {
		t.Errorf("gets %v", src.gets)
	}
	for _, q := range src.queries {
		if q.Group && q.Under != "teams/t1" || !q.Group && !strings.HasPrefix(q.Collection, "teams/t1/") {
			t.Errorf("query %+v", q)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "teams/t1x.json")); !os.IsNotExist(err) {
		t.Error("a sibling document was copied")
	}
	if r := result(t, report, "ext/customers"); r.Selected || r.Note != "outside teams/t1" {
		t.Errorf("%+v", r)
	}
	// The sibling t1x shares the id prefix: the range does not read it.
	if len(report.Outside) != 0 {
		t.Errorf("%+v", report.Outside)
	}
	if r := result(t, report, "teams/ext/events"); r.Records != 2 || r.Query != "collection group events under teams/t1" {
		t.Errorf("%+v", r)
	}
	if r := result(t, report, "teams/ext"); r.Records != 1 || r.Query != "collection teams/t1/ext" {
		t.Errorf("%+v", r)
	}
	// teams/logs is in scope and cannot be written; ext/bundles is outside it.
	if len(report.Incomplete) != 1 || !strings.Contains(report.Incomplete[0], "teams/logs") {
		t.Errorf("%v", report.Incomplete)
	}
}

func TestRunUnderACollection(t *testing.T) {
	src := exampleSource()
	report, _ := runExample(t, src, Options{Under: "teams/t1/ext"})
	if r := result(t, report, "teams"); r.Selected {
		t.Errorf("%+v", r)
	}
	if r := result(t, report, "teams/ext/events"); r.Records != 2 {
		t.Errorf("%+v", r)
	}
	src = exampleSource()
	report, _ = runExample(t, src, Options{Under: "teams", Collections: []string{"teams", "teams/ext/**"}})
	if r := result(t, report, "teams/ext/events"); r.Records != 4 || r.Query != "collection group events" {
		t.Errorf("%+v", r)
	}
	if r := result(t, report, "ext/customers"); r.Selected || r.Note != "not in the selected collections" {
		t.Errorf("%+v", r)
	}
	// A path below a collection's documents covers only the deeper collections.
	report, _ = runExample(t, exampleSource(), Options{Under: "teams/t1/ext/cal/events/e1"})
	if r := result(t, report, "teams/ext"); r.Selected {
		t.Errorf("%+v", r)
	}
	if r := result(t, report, "teams/ext/events/notes"); r.Records != 1 || r.Query != "collection teams/t1/ext/cal/events/e1/notes" {
		t.Errorf("%+v", r)
	}
	// A missing document is no record and no error.
	report, _ = runExample(t, exampleSource(), Options{Under: "teams/nope"})
	if r := result(t, report, "teams"); r.Records != 0 || !r.Complete {
		t.Errorf("%+v", r)
	}
}

func TestRunDryRunCountsAndWritesNothing(t *testing.T) {
	src := exampleSource()
	var log bytes.Buffer
	report, _ := runExample(t, src, Options{DryRun: true, Log: &log, Collections: []string{"teams", "teams/ext", "ext"}})
	if r := result(t, report, "teams"); r.Count == nil || *r.Count != 3 {
		t.Errorf("%+v", r)
	}
	if r := result(t, report, "teams/ext"); r.Count == nil || *r.Count != 1 || !strings.Contains(r.Note, "wherever its parents are") {
		t.Errorf("%+v", r)
	}
	if len(src.queries) != 0 || len(src.gets) != 0 {
		t.Error("a dry run read documents")
	}
	if report.Reads != 4 || report.BudgetReached || !strings.Contains(log.String(), "dry run: 4 read(s) spent counting; nothing was written") {
		t.Errorf("%d\n%s", report.Reads, log.String())
	}
	report, _ = runExample(t, exampleSource(), Options{DryRun: true, Under: "teams/t1", Log: &log})
	if r := result(t, report, "teams"); r.Count == nil || *r.Count != 1 || r.Query != "document teams/t1" {
		t.Errorf("%+v", r)
	}
	// The budget caps a count (a count is billed per 1000 documents), and stops the dry run.
	log.Reset()
	big := newMemSource()
	for i := range 2500 {
		big.docs[fmt.Sprintf("teams/t%05d", i)] = map[string]any{}
	}
	report, _ = runExample(t, big, Options{DryRun: true, MaxReads: 3, Log: &log, Collections: []string{"teams", "teams/ext"}})
	if r := result(t, report, "teams"); r.Count == nil || *r.Count != 2000 || !r.CountAtLeast {
		t.Errorf("%+v", r)
	}
	if !report.BudgetReached || report.Reads != 3 || !strings.Contains(log.String(), "≥2000") || !strings.Contains(log.String(), "incomplete: stopped at the read budget of 3 reads") {
		t.Errorf("%+v\n%s", report, log.String())
	}
	if r := result(t, report, "teams/ext"); r.Count != nil {
		t.Errorf("counted past the budget: %+v", r)
	}
}

func TestRunStopsAtTheReadBudgetAndResumes(t *testing.T) {
	src := exampleSource()
	full, _ := runExample(t, exampleSource(), Options{PageSize: 2})
	out := t.TempDir()
	var log bytes.Buffer
	first := runWith(t, src, testPolicy(t), Options{Output: out, MaxReads: 4, PageSize: 2, Log: &log})
	if first.Reads != 4 || first.Complete || !first.BudgetReached || !strings.Contains(first.Incomplete[0], "read budget of 4") {
		t.Fatalf("%+v", first)
	}
	if result(t, first, "teams/ext/events").Complete {
		t.Errorf("%+v", first.Collections)
	}
	if _, err := os.Stat(resumeFile(out)); err != nil {
		t.Fatal("no resume state", err)
	}
	// Each next run continues where the last stopped, never re-reading the first documents.
	runs := 1
	report := first
	for report.BudgetReached {
		src.queries = nil
		report = runWith(t, src, testPolicy(t), Options{Output: out, MaxReads: 4, PageSize: 2, Log: &log})
		if !report.Resumed {
			t.Fatal("not resumed")
		}
		for _, q := range src.queries {
			if q.Collection == "teams" && !q.Group && q.After == "" {
				t.Errorf("read the collection teams from its start again: %+v", q)
			}
		}
		if runs++; runs > 20 {
			t.Fatal("no progress")
		}
	}
	if report.Records != full.Records || len(report.Rejected) != len(full.Rejected) || report.ReadsTotal < full.Reads || !strings.Contains(log.String(), "resuming an earlier run") {
		t.Errorf("resumed total %d records, %d rejected, %d reads; a full run %d, %d, %d", report.Records, len(report.Rejected), report.ReadsTotal, full.Records, len(full.Rejected), full.Reads)
	}
	if r := result(t, report, "teams"); !r.Resumed || r.Records != 3 {
		t.Errorf("%+v", r)
	}
	if _, err := os.Stat(resumeFile(out)); !os.IsNotExist(err) {
		t.Error("a finished run left its resume state")
	}
	// --restart, and a state of another selection, start over.
	runWith(t, src, testPolicy(t), Options{Output: out, MaxReads: 4, PageSize: 2})
	restarted := runWith(t, src, testPolicy(t), Options{Output: out, MaxReads: 4, PageSize: 2, Restart: true})
	if restarted.Resumed {
		t.Error("--restart resumed")
	}
	log.Reset()
	other := runWith(t, src, testPolicy(t), Options{Output: out, MaxReads: 4, PageSize: 2, Collections: []string{"teams"}, Log: &log})
	if other.Resumed || !strings.Contains(log.String(), "the selection (--include, --space, --path) changed since the run that stopped at its read budget: starting over") {
		t.Errorf("%v\n%s", other.Resumed, log.String())
	}
	if err := os.WriteFile(resumeFile(out), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if runWith(t, src, testPolicy(t), Options{Output: out}).Resumed {
		t.Error("resumed from a broken state")
	}
	// The budget also stops the read of one document.
	report, _ = runExample(t, exampleSource(), Options{MaxReads: 1, Under: "teams/t1"})
	if report.Complete || !strings.Contains(report.Incomplete[0], "read budget of 1") {
		t.Errorf("%+v", report)
	}
}

// A state file with no counts or outside reads is resumed too.
func TestRunResumesAMinimalState(t *testing.T) {
	out := t.TempDir()
	runWith(t, exampleSource(), testPolicy(t), Options{Output: out, Source: "s", MaxReads: 1})
	var stopped resumeState
	if err := json.Unmarshal([]byte(readFile(t, resumeFile(out))), &stopped); err != nil {
		t.Fatal(err)
	}
	state := `{"format":"` + resumeFormat + `","selection":"` + stopped.Selection + `","definitions":"` + stopped.Definitions + `","policy":"` + stopped.Policy + `","items":{"collection teams":{"done":true}}}`
	if err := os.WriteFile(resumeFile(out), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	src := exampleSource()
	report := runWith(t, src, testPolicy(t), Options{Output: out, Source: "s"})
	if !report.Resumed || !result(t, report, "teams").Resumed || result(t, report, "teams").Records != 0 {
		t.Errorf("%+v", report)
	}
}

// snapshotMemSource records the time it is asked to read at.
type snapshotMemSource struct {
	*memSource
	at []time.Time
}

func (s *snapshotMemSource) ReadAt(t time.Time) { s.at = append(s.at, t) }

func TestRunSnapshot(t *testing.T) {
	start := time.Date(2026, 11, 14, 10, 0, 0, 500, time.UTC)
	withSeam(t, &now, func() time.Time { return start })
	src := &snapshotMemSource{memSource: exampleSource()}
	out := t.TempDir()
	report := runWith(t, src, testPolicy(t), Options{Output: out, Snapshot: true, MaxReads: 3})
	if report.ReadTime != "2026-11-14T10:00:00.000000Z" || len(src.at) != 1 || !src.at[0].Equal(start.Truncate(time.Microsecond)) {
		t.Fatalf("%v %v", report.ReadTime, src.at)
	}
	// A resume within the hour reads at the same time; a later one cannot, and says so.
	withSeam(t, &now, func() time.Time { return start.Add(30 * time.Minute) })
	report = runWith(t, src, testPolicy(t), Options{Output: out, Snapshot: true, MaxReads: 3})
	if report.ReadTime != "2026-11-14T10:00:00.000000Z" {
		t.Error(report.ReadTime)
	}
	withSeam(t, &now, func() time.Time { return start.Add(2 * time.Hour) })
	report = runWith(t, src, testPolicy(t), Options{Output: out, Snapshot: true, MaxReads: 3})
	if report.ReadTime != "2026-11-14T12:00:00.000000Z" || !strings.Contains(strings.Join(report.Incomplete, ";"), "read at another time") {
		t.Errorf("%+v", report)
	}
	// Without Snapshot, or with a source that cannot, no read time is set.
	if report := runWith(t, exampleSource(), testPolicy(t), Options{Output: t.TempDir(), Snapshot: true}); report.ReadTime != "" {
		t.Error(report.ReadTime)
	}
}

// M3/M4: ids that would collide or leave the folder are written safely or rejected; nothing aborts the run.
func TestRunWritesEveryIDSafely(t *testing.T) {
	src := newMemSource()
	long := strings.Repeat("x", 300)
	for _, id := range []string{"c", "C", `..\x`, "a:b", "CON", "x.", long} {
		src.docs["teams/"+id] = map[string]any{"title": "t", "createdAt": created}
	}
	// By default the run fails at the long id, and its report says so.
	out := t.TempDir()
	failed, err := Run(context.Background(), src, testDefinitions(t), testPolicy(t), Options{Output: out, Collections: []string{"teams"}})
	if !errors.Is(err, ErrIDTooLong) || !strings.Contains(err.Error(), "collection teams") || !strings.Contains(err.Error(), "305 bytes") ||
		failed == nil || failed.Complete || !strings.Contains(strings.Join(failed.Incomplete, ";"), "stopped: collection teams") {
		t.Fatalf("%v %+v", err, failed)
	}
	if saved := readFile(t, filepath.Join(out, stateDirName, "report.json")); !strings.Contains(saved, `"complete": false`) {
		t.Error(saved)
	}
	var log bytes.Buffer
	report, out := runExample(t, src, Options{Collections: []string{"teams"}, Log: &log, ContinueOnLongIDs: true})
	for _, file := range []string{"C.json", "~2E.~5Cx.json", "a~3Ab.json", "~43ON.json", "x~2E.json"} {
		if _, err := os.Stat(filepath.Join(out, "teams", file)); err != nil {
			t.Errorf("%s: %v", file, err)
		}
	}
	if len(report.Rejected) != 1 || report.Rejected[0].Path != "teams/c" || !strings.Contains(report.Rejected[0].Reasons[0], "differs only in case") {
		t.Fatalf("%+v", report.Rejected)
	}
	// An id too long for file per record is counted, not rejected and not lost.
	if r := result(t, report, "teams"); r.IDTooLong != 1 || r.Complete || !strings.Contains(strings.Join(report.Incomplete, ";"), "1 document(s) were not copied: their id is too long for file per record") {
		t.Errorf("%+v %v", r, report.Incomplete)
	}
	if strings.Contains(log.String(), long) || !strings.Contains(log.String(), strings.Repeat("x", 194)+"…") {
		t.Error("a long id is not shortened in the log")
	}
	// The re-run finds every record again (the names decode), so none is stale.
	if again, _ := runExample(t, src, Options{Output: out, Collections: []string{"teams"}, ContinueOnLongIDs: true}); len(again.Stale) != 0 || again.Written != 0 {
		t.Errorf("%+v", again)
	}
}

func TestRunPrunesOnlyCompletelyReadCollections(t *testing.T) {
	src := exampleSource()
	out := t.TempDir()
	runWith(t, src, testPolicy(t), Options{Output: out})
	delete(src.docs, "teams/t2")
	delete(src.docs, "teams/t1/ext/cal/events/e1")
	// A run stopped by its budget prunes nothing it did not read to the end.
	stopped := runWith(t, src, testPolicy(t), Options{Output: out, Prune: true, MaxReads: 5, Restart: true})
	if slices.Contains(stopped.Pruned, "teams/t1/ext/cal/events/e1.json") {
		t.Errorf("%v", stopped.Pruned)
	}
	report := runWith(t, src, testPolicy(t), Options{Output: out, Prune: true, Restart: true})
	if !slices.Equal(report.Pruned, []string{"teams/t1/ext/cal/events/e1.json", "teams/t2.json"}) || len(report.Stale) != 0 {
		t.Errorf("pruned %v, stale %v", report.Pruned, report.Stale)
	}
	if _, err := os.Stat(filepath.Join(out, "teams/t2.json")); !os.IsNotExist(err) {
		t.Error("not pruned")
	}
	// A file that cannot be deleted fails the run.
	src.docs["teams/t9"] = map[string]any{"title": "t", "createdAt": created}
	runWith(t, src, testPolicy(t), Options{Output: out})
	delete(src.docs, "teams/t9")
	withSeam(t, &removeFile, func(string) error { return errBoom })
	if _, err := Run(context.Background(), src, testDefinitions(t), testPolicy(t), Options{Output: out, Prune: true}); !errors.Is(err, errBoom) {
		t.Error(err)
	}
}

func TestRunRefusesBadOptions(t *testing.T) {
	for _, opts := range []Options{
		{Under: "teams//x"},
		{Collections: []string{"nope"}},
		{Collections: []string{"ext/secrets"}},
		{Collections: []string{"ext/loose"}},
		{Under: "spaces/s1"},
		{Under: "teams/t1", Collections: []string{"ext/customers"}},
	} {
		opts.DryRun = true
		if _, err := Run(context.Background(), exampleSource(), testDefinitions(t), testPolicy(t), opts); !errors.Is(err, ErrUsage) {
			t.Errorf("%+v: %v", opts, err)
		}
	}
	policy := testPolicy(t)
	policy.Copy = append(policy.Copy, "nope")
	if _, err := Run(context.Background(), exampleSource(), testDefinitions(t), policy, Options{DryRun: true}); !errors.Is(err, ErrUsage) {
		t.Error(err)
	}
}

func TestRunReportsReadAndWriteFailures(t *testing.T) {
	for _, method := range []string{"RootCollections", "Page", "Get", "Count"} {
		src := exampleSource()
		src.fail[method] = errBoom
		_, err := Run(context.Background(), src, testDefinitions(t), testPolicy(t), Options{Output: t.TempDir(), DryRun: method == "Count", Under: map[bool]string{true: "teams/t1"}[method == "Get"]})
		if !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", method, err)
		}
	}
	// The output cannot be written.
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), exampleSource(), testDefinitions(t), testPolicy(t), Options{Output: file}); err == nil {
		t.Error("wrote into a file")
	}
	// A record cannot be written.
	out := t.TempDir()
	claimed(t, out, SourceIdentity{})
	if err := os.MkdirAll(filepath.Join(out, "teams", "t1.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), exampleSource(), testDefinitions(t), testPolicy(t), Options{Output: out, Under: "teams/t1"}); err == nil || errors.Is(err, ErrUsage) {
		t.Error("no error for the record")
	}
	// A kept rejected record cannot be written.
	out = t.TempDir()
	claimed(t, out, SourceIdentity{})
	withSeam(t, &removeAll, func(path string) error {
		_ = os.RemoveAll(path)
		return os.WriteFile(path, nil, 0o600) // the rejected folder is a file
	})
	if _, err := Run(context.Background(), exampleSource(), testDefinitions(t), testPolicy(t), Options{Output: out, KeepRejected: true}); err == nil || errors.Is(err, ErrUsage) {
		t.Error("no error for the rejected record")
	}
	// The rejected folder of the last run cannot be cleared.
	withSeam(t, &removeAll, func(string) error { return errBoom })
	if _, err := Run(context.Background(), exampleSource(), testDefinitions(t), testPolicy(t), Options{Output: t.TempDir()}); !errors.Is(err, errBoom) {
		t.Error(err)
	}
	withSeam(t, &removeAll, os.RemoveAll)
	// The report, the resume state, or a stopped run's state cannot be written, or a finished run's state removed.
	for name, blocked := range map[string]string{"report": "report.json", "resume": "resume.json"} {
		out := t.TempDir()
		claimed(t, out, SourceIdentity{})
		if err := os.MkdirAll(filepath.Join(out, stateDirName, blocked, "x"), 0o700); err != nil {
			t.Fatal(err)
		}
		opts := Options{Output: out, PageSize: 1, Collections: []string{"teams"}}
		if name == "resume" {
			opts.Restart = true
		}
		if _, err := Run(context.Background(), exampleSource(), testDefinitions(t), testPolicy(t), opts); err == nil || errors.Is(err, ErrUsage) {
			t.Errorf("%s: %v", name, err)
		}
	}
	withSeam(t, &removeFile, func(string) error { return errBoom })
	if _, err := Run(context.Background(), exampleSource(), testDefinitions(t), testPolicy(t), Options{Output: t.TempDir()}); !errors.Is(err, errBoom) {
		t.Error(err)
	}
}

func TestRunReportsValidationFindings(t *testing.T) {
	out := t.TempDir()
	// A record already in the output breaks the collection's max_length on $ID.
	if err := os.MkdirAll(filepath.Join(out, "teams"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "teams", "much-too-long-id.json"), []byte("{\"title\":\"x\",\"createdAt\":\"2026-01-01T00:00:00Z\",\"kind\":\"SECRET-KIND\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	claimed(t, out, SourceIdentity{})
	src := newMemSource()
	report, _ := runExample(t, src, Options{Output: out, Collections: []string{"teams"}})
	if len(report.Validation) != 2 || !strings.HasPrefix(report.Validation[0], "teams/much-too-long-id.json: ") || report.Complete || strings.Contains(strings.Join(report.Validation, ";"), "SECRET-KIND") {
		t.Errorf("%v", report.Validation)
	}
	// Definitions that no longer load are a finding too.
	withSeam(t, &readDefinition, func(string, ...ingitdbReadOption) (*ingitdbDefinition, error) { return nil, errBoom })
	if got := validateOutput(context.Background(), out); len(got) != 1 || got[0] != "boom" {
		t.Errorf("%v", got)
	}
}

func TestReasonOf(t *testing.T) {
	if got := reasonOf(ingitdbValidationError{FieldName: "a", Message: "missing required field"}); got != `field "a": missing required field` {
		t.Error(got)
	}
	if got := reasonOf(ingitdbValidationError{FieldName: "a", Message: `wrong type for field "a"`}); got != `wrong type for field "a"` {
		t.Error(got)
	}
	if got := reasonOf(ingitdbValidationError{FieldName: "a", Message: `value SECRET for field "a" is not one of the permitted values`}); strings.Contains(got, "SECRET") || !strings.Contains(got, `field "a"`) {
		t.Error(got)
	}
	if got := reasonOf(ingitdbValidationError{Message: "SECRET"}); strings.Contains(got, "SECRET") {
		t.Error(got)
	}
}

func TestRunRejectsAValueItCannotKeep(t *testing.T) {
	src := newMemSource()
	src.docs["teams/t1"] = map[string]any{"title": map[string]any{"SECRET-KEY": struct{}{}}, "createdAt": created}
	src.docs["teams/t2"] = map[string]any{"title": "x", "createdAt": created, "SECRET-NAME": struct{}{}}
	report, out := runExample(t, src, Options{Collections: []string{"teams"}, KeepRejected: true})
	if len(report.Rejected) != 2 || report.Rejected[0].Reasons[0] != `field "title" holds a Firestore value of type struct {} (inside it) that the mirror cannot write` || !strings.HasPrefix(report.Rejected[1].Reasons[0], "1 field(s) the definition does not declare") {
		t.Errorf("%+v", report.Rejected)
	}
	if _, err := os.Stat(filepath.Join(out, stateDirName, "rejected", "teams", "t1.json")); !os.IsNotExist(err) {
		t.Error("a record that could not be converted was kept as a file")
	}
}

func TestValidateOutputReportsAValidatorError(t *testing.T) {
	out := t.TempDir()
	if _, err := testDefinitions(t).CopyTo(out); err != nil {
		t.Fatal(err)
	}
	withSeam(t, &validateDatabase, func(context.Context, string, *ingitdbDefinition) (*ingitdb.ValidationResult, error) {
		return nil, errBoom
	})
	if got := validateOutput(context.Background(), out); len(got) != 1 || got[0] != "boom" {
		t.Errorf("%v", got)
	}
}

func TestAppendOnceAndShown(t *testing.T) {
	if got := appendOnce(appendOnce(nil, "a"), "a"); !slices.Equal(got, []string{"a"}) {
		t.Error(got)
	}
	if shown("short") != "short" {
		t.Error("shown")
	}
}

func withSeam[T any](t *testing.T, seam *T, value T) {
	t.Helper()
	saved := *seam
	*seam = value
	t.Cleanup(func() { *seam = saved })
}

// Collections of one id at different paths share one group read.
func TestPlanSharesAGroupReadBetweenCollections(t *testing.T) {
	a := &Spec{ID: "a/x", Segments: []string{"a", "x"}}
	b := &Spec{ID: "b/x", Segments: []string{"b", "x"}}
	r := &run{defs: &Definitions{Specs: []*Spec{a, b}}, results: map[string]*CollectionResult{"a/x": {Selected: true}, "b/x": {Selected: true}},
		patterns: map[string][][]string{"a/x": specPatterns(a, nil), "b/x": specPatterns(b, nil)}}
	plan := r.plan()
	if len(plan) != 1 || len(plan[0].specs) != 2 || r.results["b/x"].Query != "collection group x" {
		t.Errorf("%+v", plan)
	}
}
