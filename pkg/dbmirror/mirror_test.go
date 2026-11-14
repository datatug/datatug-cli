package dbmirror

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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

func TestRunWritesTheMirrorLayout(t *testing.T) {
	src := exampleSource()
	var log, progress bytes.Buffer
	report, out := runExample(t, src, Options{Source: "env:SOURCE", Log: &log, Progress: &progress, PageSize: 2})

	// Records sit exactly where the definitions put them, at any depth, also below a missing parent.
	for _, file := range []string{"teams/t1.json", "teams/t1x.json", "teams/t1/ext/cal.json", "teams/t1/ext/cal/events/e1.json",
		"teams/t1/ext/cal/events/e1/notes/n1.json", "teams/t1/ext/gone/events/e2.json", "teams/t1x/ext/cal/events/e3.json",
		"ext/crm/customers/$records/c1.json"} {
		if _, err := os.Stat(filepath.Join(out, file)); err != nil {
			t.Errorf("%s: %v", file, err)
		}
	}
	// Lossless and deterministic values.
	if got := readFile(t, filepath.Join(out, "teams/t1.json")); got != `{
  "createdAt": "2026-11-01T09:00:00Z",
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
	if got := readFile(t, filepath.Join(out, "ext/crm/customers/$records/c1.json")); !strings.Contains(got, `"email": "***"`) || !strings.Contains(got, `"phone": "***"`) ||
		strings.Contains(got, "someone@example.com") || strings.Contains(got, "+000") || !strings.Contains(got, `"not a map"`) {
		t.Errorf("masked customer:\n%s", got)
	}
	// The definitions are copied; the mirror's own example records are not.
	if _, err := os.Stat(filepath.Join(out, "teams/.collection/subcollections/ext/definition.yaml")); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(out, ".ingitdb/root-collections.yaml")); err != nil {
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
	if r := result(t, report, "teams/logs"); r.Action != ActionUnsupported || !strings.Contains(r.Note, "yaml") {
		t.Errorf("%+v", r)
	}
	if r := result(t, report, "ext/customers"); r.Action != ActionMask || !slices.Equal(r.Masked, []string{"contacts.*.phone", "email", "name"}) {
		t.Errorf("%+v", r)
	}
	events := result(t, report, "teams/ext/events")
	if events.Records != 3 || events.Rejected != 2 || events.BelowMissingParent != 2 || events.Complete {
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
	if len(report.Rejected) != 2 || report.Rejected[0].Path != "teams/t2/ext/cal/events/.hidden" || !strings.Contains(report.Rejected[1].Reasons[0], "wrong type") {
		t.Errorf("%+v", report.Rejected)
	}
	if !strings.Contains(readFile(t, filepath.Join(out, stateDirName, "rejected", "teams/t2/ext/cal/events/bad.json")), `"title": 5`) {
		t.Error("the rejected record is not kept")
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
	if !slices.Equal(again.Stale, []string{"teams/t2.json"}) {
		t.Errorf("stale: %v", again.Stale)
	}
	src.docs["teams/t2"] = map[string]any{"title": "Team two", "createdAt": created}
	third, _ := runExample(t, src, Options{Output: out, Source: "env:SOURCE", PageSize: 2})
	if third.Written != 0 || readFile(t, filepath.Join(out, stateDirName, "report.json")) != reportBytes {
		t.Error("the same source gave another report")
	}
}

func TestRunUnderOneDocument(t *testing.T) {
	src := exampleSource()
	report, out := runExample(t, src, Options{Under: "teams/t1"})
	if !slices.Equal(src.gets, []string{"teams/t1"}) {
		t.Errorf("gets %v", src.gets)
	}
	for _, q := range src.queries {
		if !q.Group || q.Under != "teams/t1" {
			t.Errorf("query %+v", q)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "teams/t1x.json")); !os.IsNotExist(err) {
		t.Error("a sibling document was copied")
	}
	if r := result(t, report, "ext/customers"); r.Selected || r.Note != "outside teams/t1" {
		t.Errorf("%+v", r)
	}
	// The sibling t1x matches the name range; it is read but not copied.
	if len(report.Outside) != 1 || report.Outside[0].Collection != "teams/ext/events" || !report.Outside[0].Defined {
		t.Errorf("%+v", report.Outside)
	}
	if r := result(t, report, "teams/ext/events"); r.Records != 2 || r.Query != "collection group events under teams/t1" {
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
	if r := result(t, report, "teams/ext/events"); r.Records != 3 || r.Query != "collection group events" {
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
	if r := result(t, report, "teams/ext/events/notes"); r.Records != 1 {
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
	if r := result(t, report, "teams/ext"); r.Count == nil || *r.Count != 1 || !strings.Contains(r.Note, "shared by ext, teams/ext") {
		t.Errorf("%+v", r)
	}
	if len(src.queries) != 0 || len(src.gets) != 0 {
		t.Error("a dry run read documents")
	}
	if report.Reads != 3 || !strings.Contains(log.String(), "dry run: 3 read(s) spent counting; nothing was written") {
		t.Errorf("%d\n%s", report.Reads, log.String())
	}
	report, _ = runExample(t, exampleSource(), Options{DryRun: true, Under: "teams/t1", Log: &log})
	if r := result(t, report, "teams"); r.Count == nil || *r.Count != 1 || r.Query != "document teams/t1" {
		t.Errorf("%+v", r)
	}
	// The budget stops a dry run too, and the count of a collection then stays unknown.
	report, _ = runExample(t, exampleSource(), Options{DryRun: true, MaxReads: 1, Log: &log})
	if report.Complete || !strings.Contains(log.String(), "?") {
		t.Errorf("%+v\n%s", report, log.String())
	}
}

func TestRunStopsAtTheReadBudget(t *testing.T) {
	src := exampleSource()
	report, _ := runExample(t, src, Options{MaxReads: 4, PageSize: 2})
	if report.Reads != 4 || report.Complete || !strings.Contains(report.Incomplete[0], "read budget of 4") {
		t.Errorf("%+v", report)
	}
	if !result(t, report, "ext/customers").Complete || result(t, report, "teams/ext/events").Complete || result(t, report, "teams").Complete {
		t.Errorf("%+v", report.Collections)
	}
	// The budget also stops the read of one document.
	report, _ = runExample(t, exampleSource(), Options{MaxReads: 1, Under: "teams/t1"})
	if report.Complete || !strings.Contains(report.Incomplete[0], "read budget of 1") {
		t.Errorf("%+v", report)
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
	// A record or a rejected record cannot be written.
	for _, blocked := range []string{"teams", filepath.Join(stateDirName, "rejected")} {
		out := t.TempDir()
		defs := testDefinitions(t)
		if err := defs.CopyTo(out); err != nil {
			t.Fatal(err)
		}
		src := exampleSource()
		if blocked == "teams" {
			src.docs = map[string]map[string]any{"teams/t1/ext/cal": {}}
			if err := os.WriteFile(filepath.Join(out, "teams", "t1"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		withSeam(t, &removeAll, func(path string) error {
			if blocked != "teams" {
				_ = os.RemoveAll(path)
				return os.WriteFile(path, nil, 0o600) // the state folder is a file: no rejected record can go in it
			}
			return nil
		})
		if _, err := Run(context.Background(), src, defs, testPolicy(t), Options{Output: out}); err == nil {
			t.Errorf("%s: no error", blocked)
		}
	}
	// The state folder of the last run cannot be cleared.
	withSeam(t, &removeAll, func(string) error { return errBoom })
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
	if err := os.WriteFile(filepath.Join(out, "teams", "much-too-long-id.json"), []byte("{\"title\":\"x\",\"createdAt\":\"2026-01-01T00:00:00Z\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := newMemSource()
	report, _ := runExample(t, src, Options{Output: out, Collections: []string{"teams"}})
	if len(report.Validation) != 1 || !strings.HasPrefix(report.Validation[0], "teams/much-too-long-id.json: ") || report.Complete {
		t.Errorf("%v", report.Validation)
	}
	// Definitions that no longer load are a finding too.
	withSeam(t, &readDefinition, func(string, ...ingitdbReadOption) (*ingitdbDefinition, error) { return nil, errBoom })
	if got := validateOutput(context.Background(), out); len(got) != 1 || got[0] != "boom" {
		t.Errorf("%v", got)
	}
}

func TestDescribe(t *testing.T) {
	if got := describe(ingitdbValidationError{FieldName: "a", Message: "missing required field"}); got != `field "a": missing required field` {
		t.Error(got)
	}
	if got := describe(ingitdbValidationError{FieldName: "a", Message: `wrong type for field "a"`}); got != `wrong type for field "a"` {
		t.Error(got)
	}
}

func withSeam[T any](t *testing.T, seam *T, value T) {
	t.Helper()
	saved := *seam
	*seam = value
	t.Cleanup(func() { *seam = saved })
}

func TestRunRejectsAValueItCannotKeep(t *testing.T) {
	src := newMemSource()
	src.docs["teams/t1"] = map[string]any{"title": "x", "createdAt": created, "odd": struct{}{}}
	report, out := runExample(t, src, Options{Collections: []string{"teams"}})
	if len(report.Rejected) != 1 || !strings.Contains(report.Rejected[0].Reasons[0], "struct {}") {
		t.Errorf("%+v", report.Rejected)
	}
	if _, err := os.Stat(filepath.Join(out, stateDirName, "rejected", "teams", "t1.json")); !os.IsNotExist(err) {
		t.Error("a record that could not be converted was kept as a file")
	}
}

func TestRunFailsWhenARecordOrTheReportCannotBeWritten(t *testing.T) {
	// The record of the one document asked for cannot be written.
	out := t.TempDir()
	if err := os.MkdirAll(filepath.Join(out, "teams", "t1.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), exampleSource(), testDefinitions(t), testPolicy(t), Options{Output: out, Under: "teams/t1"}); err == nil {
		t.Error("no error for the record")
	}
	// The report cannot be written.
	withSeam(t, &removeAll, func(path string) error {
		_ = os.RemoveAll(path)
		return os.MkdirAll(filepath.Join(path, "report.json"), 0o700)
	})
	if _, err := Run(context.Background(), exampleSource(), testDefinitions(t), testPolicy(t), Options{Output: t.TempDir()}); err == nil {
		t.Error("no error for the report")
	}
}

func TestValidateOutputReportsAValidatorError(t *testing.T) {
	out := t.TempDir()
	if err := testDefinitions(t).CopyTo(out); err != nil {
		t.Fatal(err)
	}
	withSeam(t, &validateDatabase, func(context.Context, string, *ingitdbDefinition) (*ingitdb.ValidationResult, error) {
		return nil, errBoom
	})
	if got := validateOutput(context.Background(), out); len(got) != 1 || got[0] != "boom" {
		t.Errorf("%v", got)
	}
}

func TestAppendOnce(t *testing.T) {
	if got := appendOnce(appendOnce(nil, "a"), "a"); !slices.Equal(got, []string{"a"}) {
		t.Error(got)
	}
}
