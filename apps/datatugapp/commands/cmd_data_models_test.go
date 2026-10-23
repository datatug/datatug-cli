package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/datatug/datatug-core/pkg/meaninggraph"
	"github.com/spf13/cobra"
)

const dataModelTestAddress = "modelspec://github.com/demo-db/chinook/chinook"

type dataModelResolverStub struct {
	reference datatug.DataModelReference
	result    *meaninggraph.Resolution
	err       error
	calls     int
}

func (s *dataModelResolverStub) Resolve(_ context.Context, ref datatug.DataModelReference) (*meaninggraph.Resolution, error) {
	s.calls++
	s.reference = ref
	if s.result == nil {
		return nil, s.err
	}
	result := *s.result
	result.Reference = ref
	result.Root.DataModelReference = ref
	return &result, s.err
}

func executeDataModels(t *testing.T, factory dataModelResolverFactory, arguments ...string) (string, error) {
	t.Helper()
	root := &cobra.Command{Use: "test", SilenceErrors: true, SilenceUsage: true}
	root.AddCommand(dataModelsCommandWithResolver(factory))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"data-models", "resolve"}, arguments...))
	err := root.Execute()
	return out.String(), err
}

func dataModelsDirectArguments() []string {
	return []string{"--address", dataModelTestAddress, "--source-commit", strings.Repeat("a", 40),
		"--registry-commit", strings.Repeat("b", 40), "--catalog-sha256", strings.Repeat("c", 64), "--fixture-root", "/explicit-fixture"}
}

func TestDataModelsResolveKeepsRawSourceAndExactPins(t *testing.T) {
	definition := []byte("{\n\t\"number\":10000000000000000001,\"decimal\":1.234567890123456789\n}\n")
	source := []byte("{ \"modelspec\": \"1.0-draft\", \"number\": 10000000000000000001 }\n")
	hcl := []byte("// exact source bytes\nrecord \"Thing\" {}\n")
	for _, format := range []string{"json", "definition", "modelspec-json", "modelspec-hcl"} {
		t.Run(format, func(t *testing.T) {
			stub := &dataModelResolverStub{result: &meaninggraph.Resolution{Transport: meaninggraph.TransportFixture,
				Root: meaninggraph.ModelRevision{Definition: definition, SourceJSON: source, SourceHCL: hcl}}}
			factory := func(config meaninggraph.Config) (dataModelResolver, error) {
				if config.RegistryCommit != strings.Repeat("b", 40) || config.CatalogSHA256 != strings.Repeat("c", 64) || config.FixtureRoot != "/explicit-fixture" {
					t.Fatalf("snapshot identity changed: %#v", config)
				}
				return stub, nil
			}
			out, err := executeDataModels(t, factory, append(dataModelsDirectArguments(), "--format", format)...)
			if err != nil || stub.calls != 1 || stub.reference.Address != dataModelTestAddress || stub.reference.Commit != strings.Repeat("a", 40) {
				t.Fatalf("reference resolution: %v, %#v", err, stub)
			}
			switch format {
			case "definition":
				if out != string(definition) {
					t.Fatal("definition bytes changed")
				}
			case "modelspec-json":
				if out != string(source) {
					t.Fatal("original JSON bytes changed")
				}
			case "modelspec-hcl":
				if out != string(hcl) {
					t.Fatal("original HCL bytes changed")
				}
			default:
				var document struct {
					Schema, Transport string
					Reference         datatug.DataModelReference
				}
				if err := json.Unmarshal([]byte(out), &document); err != nil || document.Schema != "datatug-data-model-resolution/v1" || document.Transport != meaninggraph.TransportFixture || document.Reference != stub.reference {
					t.Fatalf("incorrect result identity: %s / %v", out, err)
				}
				if !strings.Contains(out, "10000000000000000001") {
					t.Fatal("raw numeric value rounded")
				}
			}
		})
	}
}

func TestDataModelsResolveProjectAliasesAndCatalogVersionsAreReadOnly(t *testing.T) {
	dir := t.TempDir()
	metadata := `{"id":"test","title":"Test","access":"private","created":{"at":"2026-10-23T00:00:00Z"},"dataModels":{"version":1,"references":[{"id":"first","address":"` + dataModelTestAddress + `","commit":"` + strings.Repeat("a", 40) + `"},{"id":"second","address":"` + dataModelTestAddress + `","commit":"` + strings.Repeat("b", 40) + `"}],"catalogBindings":[{"environment":"local","catalog":"chinook-local","reference":"first"},{"environment":"prod","catalog":"chinook-prod","reference":"second"}]}}`
	path := filepath.Join(dir, "datatug-project.json")
	if err := os.WriteFile(path, []byte(metadata), 0600); err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		name, commit string
		selectors    []string
	}{
		{"alias", "b", []string{"--reference", "second"}},
		{"local", "a", []string{"--environment", "local", "--catalog", "chinook-local"}},
		{"prod", "b", []string{"--environment", "prod", "--catalog", "chinook-prod"}},
	} {
		t.Run(sample.name, func(t *testing.T) {
			stub := &dataModelResolverStub{result: &meaninggraph.Resolution{}}
			arguments := append([]string{"--directory", dir}, sample.selectors...)
			_, err := executeDataModels(t, func(meaninggraph.Config) (dataModelResolver, error) { return stub, nil }, arguments...)
			if err != nil || stub.reference.Commit != strings.Repeat(sample.commit, 40) {
				t.Fatalf("%v / %#v", err, stub.reference)
			}
		})
	}
	t.Chdir(dir)
	stub := &dataModelResolverStub{result: &meaninggraph.Resolution{}}
	if _, err := executeDataModels(t, func(meaninggraph.Config) (dataModelResolver, error) { return stub, nil }, "--reference", "first"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil || string(before) != metadata {
		t.Fatal("project metadata changed")
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatal("resolution wrote project/meaning/query files")
	}
}

func TestDataModelsResolveRefusesAmbiguousOrUnpinnedInputBeforeResolver(t *testing.T) {
	for _, arguments := range [][]string{
		{}, {"--address", dataModelTestAddress}, {"--source-commit", strings.Repeat("a", 40)},
		{"--address", dataModelTestAddress, "--source-commit", "main"},
		{"--address", dataModelTestAddress + "?ref=" + strings.Repeat("a", 40), "--source-commit", strings.Repeat("a", 40)},
		append(dataModelsDirectArguments(), "--reference", "first"),
		append(dataModelsDirectArguments(), "--directory", t.TempDir()),
		{"--reference", "first", "--environment", "local", "--catalog", "chinook-local"},
		{"--environment", "local"}, {"--catalog", "chinook-local"},
		{"--reference", "first", "--project", "test", "--directory", t.TempDir()},
		append(dataModelsDirectArguments(), "--format", "sql"),
	} {
		_, err := executeDataModels(t, func(meaninggraph.Config) (dataModelResolver, error) {
			t.Fatal("resolver reached for invalid input")
			return nil, nil
		}, arguments...)
		if err == nil {
			t.Fatalf("accepted invalid input: %v", arguments)
		}
	}
}

func TestDataModelsResolveMissingOrInvalidProjectMetadata(t *testing.T) {
	for _, data := range []string{
		`{"id":"test","title":"Test","access":"private"}`,
		`{"id":"test","title":"Test","access":"private","dataModels":null}`,
		`{"id":"test","title":"Test","access":"private","dataModels":{"version":2,"references":[]}}`,
		`{"id":"test","title":"Test","access":"private","dataModels":{"version":1,"references":[]}}`,
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "datatug-project.json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := executeDataModels(t, func(meaninggraph.Config) (dataModelResolver, error) {
			t.Fatal("resolver reached for missing/invalid project reference")
			return nil, nil
		}, "--directory", dir, "--reference", "missing")
		if err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
}

func TestDataModelsResolveSnapshotAndResolutionFailures(t *testing.T) {
	for _, scenario := range []string{"snapshot", "lookup", "empty"} {
		t.Run(scenario, func(t *testing.T) {
			_, err := executeDataModels(t, func(meaninggraph.Config) (dataModelResolver, error) {
				if scenario == "snapshot" {
					return nil, errors.New("bad catalog hash")
				}
				if scenario == "lookup" {
					return &dataModelResolverStub{err: errors.New("exact version missing")}, nil
				}
				return &dataModelResolverStub{}, nil
			}, dataModelsDirectArguments()...)
			if err == nil {
				t.Fatal("resolution failure lost")
			}
		})
	}
	command, _, err := DatatugCommand().Find([]string{"data-models", "resolve"})
	if err != nil || command.Name() != "resolve" {
		t.Fatalf("command is not registered: %v", err)
	}
}

func TestDataModelsRealResolverRequiresExactRegistryPins(t *testing.T) {
	for _, sample := range []struct {
		name, registry, digest string
	}{
		{name: "both omitted"},
		{name: "registry omitted", digest: strings.Repeat("c", 64)},
		{name: "digest omitted", registry: strings.Repeat("b", 40)},
		{name: "registry branch", registry: "main", digest: strings.Repeat("c", 64)},
		{name: "invalid digest", registry: strings.Repeat("b", 40), digest: strings.Repeat("z", 64)},
	} {
		t.Run(sample.name, func(t *testing.T) {
			root := &cobra.Command{Use: "test", SilenceErrors: true, SilenceUsage: true}
			root.AddCommand(dataModelsCommand())
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs([]string{"data-models", "resolve", "--address", dataModelTestAddress,
				"--source-commit", strings.Repeat("a", 40), "--registry-commit", sample.registry,
				"--catalog-sha256", sample.digest})
			err := root.Execute()
			if showExitCodeOf(t, err) != exitCodeUsage || !strings.Contains(err.Error(), "invalid registry snapshot") || out.Len() != 0 {
				t.Fatalf("invalid snapshot: output=%q error=%v", out.String(), err)
			}
		})
	}
}

func TestDataModelsRegisteredProjectHonorsExplicitConfig(t *testing.T) {
	previousConfig := dtconfig.GetConfigFilePath()
	t.Cleanup(func() { dtconfig.SetConfigFilePath(previousConfig) })
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	dir := t.TempDir()
	metadata := `{"id":"logical-only","title":"Logical only","access":"private","created":{"at":"2026-10-23T00:00:00Z"},"dataModels":{"version":1,"references":[{"id":"first","address":"` + dataModelTestAddress + `","commit":"` + strings.Repeat("a", 40) + `"}]}}`
	projectPath := filepath.Join(dir, "datatug-project.json")
	if err := os.WriteFile(projectPath, []byte(metadata), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runIsolatedCommand(t, settings, "projects", "add", "--project", "logical-only", "--directory", dir); err != nil {
		t.Fatal(err)
	}
	beforeSettings, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	// Select another config first: execution must restore the explicit --config before lookup.
	dtconfig.SetConfigFilePath(filepath.Join(t.TempDir(), "other-settings.yaml"))
	root := DatatugCommand()
	root.SilenceErrors, root.SilenceUsage = true, true
	for _, command := range root.Commands() {
		if command.Name() == "data-models" {
			root.RemoveCommand(command)
		}
	}
	stub := &dataModelResolverStub{result: &meaninggraph.Resolution{}}
	root.AddCommand(dataModelsCommandWithResolver(func(meaninggraph.Config) (dataModelResolver, error) { return stub, nil }))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--config", settings, "data-models", "resolve", "--project", "logical-only", "--reference", "first"})
	if err := root.Execute(); err != nil || stub.calls != 1 || stub.reference.Commit != strings.Repeat("a", 40) {
		t.Fatalf("registered reference: %v / %#v", err, stub)
	}
	for path, before := range map[string][]byte{settings: beforeSettings, projectPath: []byte(metadata)} {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("read changed %s: %v", path, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("metadata-only resolution wrote project files: %v / %v", entries, err)
	}
}

func TestDataModelsResolvePropagatesOutputFailures(t *testing.T) {
	for _, format := range []string{"json", "definition", "modelspec-json", "modelspec-hcl"} {
		t.Run(format, func(t *testing.T) {
			stub := &dataModelResolverStub{result: &meaninggraph.Resolution{Root: meaninggraph.ModelRevision{
				Definition: []byte(`{"type":"record"}`), SourceJSON: []byte(`{"modelspec":"1.0-draft"}`), SourceHCL: []byte("record {}\n"),
			}}}
			command := dataModelsCommandWithResolver(func(meaninggraph.Config) (dataModelResolver, error) { return stub, nil })
			command.SilenceErrors, command.SilenceUsage = true, true
			command.SetOut(failingWriter{})
			command.SetErr(&bytes.Buffer{})
			command.SetArgs(append([]string{"resolve"}, append(dataModelsDirectArguments(), "--format", format)...))
			if err := command.Execute(); !errors.Is(err, errWriteFailed) || stub.calls != 1 {
				t.Fatalf("output failure lost: %v (resolver calls=%d)", err, stub.calls)
			}
		})
	}
}

func TestDataModelsResolveReportsProjectReadFailures(t *testing.T) {
	for _, sample := range []struct {
		name, metadata, want string
		code                 int
	}{
		{name: "missing project", want: "not a DataTug project", code: exitCodeNotFound},
		{name: "malformed project", metadata: "{", want: "read project metadata", code: 1},
		{name: "missing catalog binding", metadata: `{"id":"test","access":"private","dataModels":{"version":1,"references":[{"id":"first","address":"` + dataModelTestAddress + `","commit":"` + strings.Repeat("a", 40) + `"}]}}`, want: "no data model reference assigned to environment/catalog", code: exitCodeNotFound},
	} {
		t.Run(sample.name, func(t *testing.T) {
			dir := t.TempDir()
			if sample.metadata != "" {
				if err := os.WriteFile(filepath.Join(dir, "datatug-project.json"), []byte(sample.metadata), 0600); err != nil {
					t.Fatal(err)
				}
			}
			out, err := executeDataModels(t, func(meaninggraph.Config) (dataModelResolver, error) {
				t.Fatal("resolver reached without a readable project reference")
				return nil, nil
			}, "--directory", dir, "--environment", "local", "--catalog", "chinook.sqlite")
			if showExitCodeOf(t, err) != sample.code || !strings.Contains(err.Error(), sample.want) || out != "" {
				t.Fatalf("project failure: output=%q error=%v", out, err)
			}
		})
	}
}

func TestDataModelsResolveReportsUnavailableWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	out, err := executeDataModels(t, func(meaninggraph.Config) (dataModelResolver, error) {
		t.Fatal("resolver reached without a working directory")
		return nil, nil
	}, "--reference", "first")
	if !errors.Is(err, os.ErrNotExist) || out != "" {
		t.Fatalf("unavailable cwd: output=%q error=%v", out, err)
	}
}

func TestDataModelsRealResolverRejectsMismatchedCatalog(t *testing.T) {
	// The real resolver deliberately rejects symlinks in fixture ancestors.
	// Darwin's temporary directory can be rooted through /var -> /private/var.
	fixture, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture, "catalog.json"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command := dataModelsCommand()
	command.SilenceErrors, command.SilenceUsage = true, true
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"resolve", "--address", dataModelTestAddress, "--source-commit", strings.Repeat("a", 40),
		"--registry-commit", strings.Repeat("b", 40), "--catalog-sha256", strings.Repeat("c", 64), "--fixture-root", fixture})
	err = command.Execute()
	if showExitCodeOf(t, err) != 1 || !strings.Contains(err.Error(), "retained digest mismatch: catalog.json") || out.Len() != 0 {
		t.Fatalf("catalog digest mismatch: output=%q error=%v", out.String(), err)
	}
}

func TestDataModelsResolvePreservesHostWorkingDirectoryFailure(t *testing.T) {
	original := dataModelWorkingDirectory
	t.Cleanup(func() { dataModelWorkingDirectory = original })
	dataModelWorkingDirectory = func() (string, error) { return "", os.ErrNotExist }
	out, err := executeDataModels(t, func(meaninggraph.Config) (dataModelResolver, error) {
		t.Fatal("resolver reached after working-directory failure")
		return nil, nil
	}, "--reference", "first")
	if !errors.Is(err, os.ErrNotExist) || out != "" {
		t.Fatal("host cwd failure lost", out, err)
	}
}
