package dbmirror

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// m4: names that differ only in case are found across a resume, and between
// parent folders.
func TestRunFindsCaseCollisionsAcrossResumesAndParents(t *testing.T) {
	src := newMemSource()
	src.docs["teams/Ab"] = map[string]any{"title": "x", "createdAt": created}
	src.docs["teams/ab"] = map[string]any{"title": "y", "createdAt": created}
	out := t.TempDir()
	// Ab is read before the budget stops the run, ab by the run that resumes it.
	first := runWith(t, src, testPolicy(t), Options{Output: out, Collections: []string{"teams"}, MaxReads: 2, PageSize: 1})
	if !first.BudgetReached || first.Records != 1 {
		t.Fatalf("%+v", first)
	}
	second := runWith(t, src, testPolicy(t), Options{Output: out, Collections: []string{"teams"}})
	if !second.Resumed || len(second.Rejected) != 1 || second.Rejected[0].Path != "teams/ab" || !strings.Contains(second.Rejected[0].Reasons[0], "differs only in case from the one of teams/Ab") {
		t.Errorf("%+v", second.Rejected)
	}
	// Two parents that differ only in case would share one folder.
	src = newMemSource()
	src.docs["teams/t1/ext/Cal"] = map[string]any{"data": map[string]any{}}
	src.docs["teams/t1/ext/Cal/events/e1"] = map[string]any{"title": "one"}
	src.docs["teams/t1/ext/cal/events/e2"] = map[string]any{"title": "two"}
	report := runWith(t, src, testPolicy(t), Options{Output: t.TempDir(), Collections: []string{"teams/ext", "teams/ext/events"}})
	if len(report.Rejected) != 1 || report.Rejected[0].Path != "teams/t1/ext/cal/events/e2" || !strings.Contains(report.Rejected[0].Reasons[0], "its folder teams/t1/ext/cal differs only in case from the folder teams/t1/ext/Cal") {
		t.Errorf("%+v", report.Rejected)
	}
}

// m5: a resume never continues under changed definitions or a changed policy.
func TestRunStartsOverWhenDefinitionsOrPolicyChange(t *testing.T) {
	out := t.TempDir()
	src := exampleSource()
	runWith(t, src, testPolicy(t), Options{Output: out, MaxReads: 4, PageSize: 2})
	var log bytes.Buffer
	policy := testPolicy(t)
	policy.Mask["ext/customers"] = []string{"email"}
	if report := runWith(t, src, policy, Options{Output: out, MaxReads: 4, PageSize: 2, Log: &log}); report.Resumed || !strings.Contains(log.String(), "the policy changed since the run that stopped at its read budget: starting over, not resuming") {
		t.Errorf("%s", log.String())
	}
	// Changed definitions (a copy with one more column).
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "mirror"))); err != nil {
		t.Fatal(err)
	}
	def := filepath.Join(dir, "teams/.collection/definition.yaml")
	content := readFile(t, def) + "  extra:\n    type: string\n"
	if err := os.WriteFile(def, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := LoadDefinitions(dir)
	if err != nil {
		t.Fatal(err)
	}
	log.Reset()
	report, err := Run(context.Background(), src, changed, policy, Options{Output: out, Log: &log})
	if err != nil || report.Resumed || !strings.Contains(log.String(), "the definitions changed since") {
		t.Errorf("%v %s", err, log.String())
	}
}

// m6: a path longer than the system writes fails the run like a long id
// (or, when asked, is counted), and a rejected record too long to keep is
// noted, not written.
func TestRunChecksThePathLength(t *testing.T) {
	src := newMemSource()
	src.docs["teams/"+strings.Repeat("a", 40)] = map[string]any{"title": "x", "createdAt": created}
	src.docs["teams/bad"] = map[string]any{"title": int64(1), "createdAt": created}
	out := t.TempDir()
	withSeam(t, &maxPathChars, len(filepath.Join(out, "teams", "bad.json"))+len(stateDirName)+4)
	_, err := Run(context.Background(), src, testDefinitions(t), testPolicy(t), Options{Output: out, Collections: []string{"teams"}})
	if !errors.Is(err, ErrIDTooLong) || !strings.Contains(err.Error(), "its file path would be") {
		t.Fatal(err)
	}
	report := runWith(t, src, testPolicy(t), Options{Output: t.TempDir(), Collections: []string{"teams"}, ContinueOnLongIDs: true, KeepRejected: true})
	if result(t, report, "teams").IDTooLong != 1 || len(report.Rejected) != 1 || !strings.Contains(strings.Join(report.Rejected[0].Reasons, ";"), "its rejected record was not kept: its file path would be") {
		t.Errorf("%+v", report)
	}
}

// A dry run and a run fail before reading on an id they know is too long: one
// named by --path or by the policy's paths.
func TestRunFailsUpFrontOnKnownLongIDs(t *testing.T) {
	long := strings.Repeat("x", 300)
	for name, opts := range map[string]Options{
		"dry run under it": {DryRun: true, Under: "teams/" + long},
		"run under it":     {Output: t.TempDir(), Under: "teams/" + long},
	} {
		src := exampleSource()
		_, err := Run(context.Background(), src, testDefinitions(t), testPolicy(t), opts)
		if !errors.Is(err, ErrIDTooLong) || !strings.Contains(err.Error(), "collection teams: the id") || len(src.queries)+len(src.gets)+len(src.counts) != 0 {
			t.Errorf("%s: %v", name, err)
		}
	}
	policy := testPolicy(t)
	policy.Paths = map[string][]string{"teams/ext": {"teams/" + long + "/ext"}}
	if _, err := Run(context.Background(), exampleSource(), testDefinitions(t), policy, Options{DryRun: true}); !errors.Is(err, ErrIDTooLong) || !strings.Contains(err.Error(), "collection teams/ext: the id") {
		t.Error(err)
	}
}

// m1: undeclared field names and map keys are data: never shown.
func TestRunShowsNoUndeclaredNameOrMapKey(t *testing.T) {
	src := newMemSource()
	src.docs["teams/t1"] = map[string]any{"title": "x", "createdAt": created, "someone@example.com": "v", "other@example.com": "w"}
	src.docs["teams/t2"] = map[string]any{"title": "x", "createdAt": created, "other@example.com": "w", "someone@example.com": "v"}
	src.docs["teams/t3"] = map[string]any{"title": "x", "createdAt": created, "settings": map[string]any{"key@example.com": struct{}{}}}
	var log bytes.Buffer
	report, out := runExample(t, src, Options{Collections: []string{"teams"}, Log: &log})
	if len(report.Rejected) != 3 {
		t.Fatalf("%+v", report.Rejected)
	}
	// Equal sets of names have equal fingerprints within a run.
	if report.Rejected[0].Reasons[0] != report.Rejected[1].Reasons[0] || !strings.HasPrefix(report.Rejected[0].Reasons[0], "2 field(s) the definition does not declare") {
		t.Errorf("%+v", report.Rejected)
	}
	if got := report.Rejected[2].Reasons[0]; got != `field "settings" holds a Firestore value of type struct {} (inside it) that the mirror cannot write` {
		t.Error(got)
	}
	if text := allBytes(t, out) + log.String(); strings.Contains(text, "example.com") {
		t.Error("a field name that is data was shown")
	}
	// Another run salts the fingerprint differently.
	again, _ := runExample(t, src, Options{Collections: []string{"teams"}})
	if again.Rejected[0].Reasons[0] == report.Rejected[0].Reasons[0] {
		t.Error("the fingerprint is not salted per run")
	}
	if got := reasonOf(ingitdbValidationError{FieldName: "x@example.com", Message: `undeclared field "x@example.com"`}); strings.Contains(got, "example") {
		t.Error(got)
	}
}
