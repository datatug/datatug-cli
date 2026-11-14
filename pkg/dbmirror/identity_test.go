package dbmirror

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var (
	production = SourceIdentity{Project: "p1", Database: "(default)"}
	emulator   = SourceIdentity{Project: "p1", Database: "(default)", Emulator: "127.0.0.1:8080"}
)

// M6: an output knows which database it is a mirror of, and no run, resume
// or prune of another database touches it.
func TestRunKeepsAnOutputToOneSource(t *testing.T) {
	out := t.TempDir()
	src := exampleSource()
	first := runWith(t, src, testPolicy(t), Options{Output: out, Identity: production, MaxReads: 4, PageSize: 2})
	if !first.BudgetReached || *first.SourceIdentity != production {
		t.Fatalf("%+v", first)
	}
	if got := readFile(t, outputFile(out)); !strings.Contains(got, `"project": "p1"`) || !strings.Contains(got, `"id_encoding": "tilde-hex/v1"`) || strings.Contains(got, "emulator") {
		t.Error(got)
	}
	before := allBytes(t, out)
	for name, opts := range map[string]Options{
		"resume from the emulator":       {Identity: emulator},
		"prune from the emulator":        {Identity: emulator, Prune: true},
		"restart from another project":   {Identity: SourceIdentity{Project: "p2", Database: "(default)"}, Restart: true},
		"resume from another database":   {Identity: SourceIdentity{Project: "p1", Database: "other"}},
		"prune from another emulator":    {Identity: SourceIdentity{Project: "p1", Database: "(default)", Emulator: "127.0.0.1:9090"}, Prune: true},
		"an env variable that now reads": {Identity: SourceIdentity{Project: "p3", Database: "(default)"}},
	} {
		opts.Output = out
		_, err := Run(context.Background(), src, testDefinitions(t), testPolicy(t), opts)
		if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "is a mirror of Firestore project p1, database (default), not of") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if allBytes(t, out) != before {
		t.Error("a refused run changed the output")
	}
	// The same source resumes, and prunes once read completely.
	resumed := runWith(t, src, testPolicy(t), Options{Output: out, Identity: production})
	if !resumed.Resumed {
		t.Errorf("%+v", resumed)
	}
	if pruned := runWith(t, src, testPolicy(t), Options{Output: out, Identity: production, Prune: true}); pruned.Resumed {
		t.Errorf("%+v", pruned)
	}
	// An output created from an emulator is pruned only from that emulator.
	emulated := t.TempDir()
	runWith(t, src, testPolicy(t), Options{Output: emulated, Identity: emulator})
	if _, err := Run(context.Background(), src, testDefinitions(t), testPolicy(t), Options{Output: emulated, Identity: production, Prune: true}); !errors.Is(err, ErrUsage) {
		t.Error(err)
	}
	runWith(t, src, testPolicy(t), Options{Output: emulated, Identity: emulator, Prune: true})
}

func TestClaimOutputRefuses(t *testing.T) {
	run := func(out string, opts Options) error {
		opts.Output = out
		_, err := Run(context.Background(), exampleSource(), testDefinitions(t), testPolicy(t), opts)
		return err
	}
	// A folder that is not empty and says nothing of its source.
	full := t.TempDir()
	if err := os.WriteFile(filepath.Join(full, "x.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, restart := range []bool{false, true} {
		if err := run(full, Options{Restart: restart}); !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "is not empty and does not say which database") {
			t.Errorf("restart %v: %v", restart, err)
		}
	}
	// A state that is not one, or of another key encoding.
	for content, want := range map[string]string{
		"not json":           "is not a db mirror output state",
		`{"format":"other"}`: "is not a db mirror output state",
		`{"format":"` + outputFormat + `","source":{"project":"","database":""},"id_encoding":"percent/v1"}`: "by the key encoding percent/v1, and this version uses tilde-hex/v1",
	} {
		out := t.TempDir()
		claimed(t, out, SourceIdentity{})
		if err := os.WriteFile(outputFile(out), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := run(out, Options{}); !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", content, err)
		}
	}
	// Read failures.
	dir := t.TempDir()
	if err := os.MkdirAll(outputFile(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := run(dir, Options{}); err == nil || errors.Is(err, ErrUsage) {
		t.Error(err)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(file, Options{}); err == nil {
		t.Error("claimed a file")
	}
	// A missing folder is claimed.
	if err := run(filepath.Join(t.TempDir(), "new"), Options{}); err != nil {
		t.Error(err)
	}
}

func TestSourceIdentityString(t *testing.T) {
	if got := emulator.String(); got != "Firestore project p1, database (default), on the emulator at 127.0.0.1:8080" {
		t.Error(got)
	}
}

func TestClaimOutputCannotListTheFolder(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any folder")
	}
	out := t.TempDir()
	if err := os.Chmod(out, 0o300); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(out, 0o700) }()
	if _, err := Run(context.Background(), exampleSource(), testDefinitions(t), testPolicy(t), Options{Output: out}); err == nil || errors.Is(err, ErrUsage) {
		t.Error(err)
	}
}
