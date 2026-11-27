package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ciWorkflow is Go CI (.github/workflows/golangci.yml), the one CI workflow
// of this repository (backstage rule one-ci-workflow-smart-jobs).
const ciWorkflow = "golangci.yml"

// The gate is the only check branch protection requires, so a job it does
// not wait for could fail while a pull request still merges.
func TestCIGateWaitsForEveryJob(t *testing.T) {
	t.Parallel()
	wf := readWorkflow(t, ciWorkflow)
	gate, ok := wf.Jobs["ci-ok"]
	if !ok {
		t.Fatal("Go CI has no ci-ok job")
	}
	if strings.TrimSpace(gate.If) != "always()" {
		// !cancelled() would let a cancelled run report the gate as skipped,
		// which GitHub counts as a passed required check.
		t.Fatalf("ci-ok must run with if: always(), got %q", gate.If)
	}
	needs := map[string]bool{}
	for _, n := range gate.Needs {
		needs[n] = true
	}
	var missing []string
	for name := range wf.Jobs {
		if name != "ci-ok" && !needs[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("ci-ok does not wait for: %s", strings.Join(missing, ", "))
	}
	if len(gate.Steps) != 1 {
		t.Fatalf("ci-ok must have one gate step, has %d", len(gate.Steps))
	}
	// A job without an if: of its own must have run: the gate lists it as
	// one that may never be skipped.
	always := strings.Fields(gate.Steps[0].Env["ALWAYS"])
	listed := map[string]bool{}
	for _, name := range always {
		listed[name] = true
	}
	for name, job := range wf.Jobs {
		if name != "ci-ok" && job.If == "" && !listed[name] {
			t.Errorf("job %s has no scope condition, so ci-ok must list it in ALWAYS", name)
		}
		if job.If != "" && listed[name] {
			t.Errorf("job %s is scoped, so ci-ok must accept it as skipped", name)
		}
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed; the gate script needs it (GitHub-hosted runners have it)")
	}
	const narrow = `"outputs":{"code":"true","redis":"false"}`
	const all = `"outputs":{"code":"true","redis":"true"}`
	for _, c := range []struct {
		name  string
		needs string
		want  int
	}{
		{"all green", `{"changes":{"result":"success",` + narrow + `},"pure-go":{"result":"success"},"native":{"result":"success"}}`, 0},
		{"scoped job out of scope", `{"changes":{"result":"success",` + narrow + `},"pure-go":{"result":"success"},"native":{"result":"skipped"}}`, 0},
		{"scoped job skipped although every scope is selected", `{"changes":{"result":"success",` + all + `},"pure-go":{"result":"success"},"native":{"result":"skipped"}}`, 1},
		{"every scope selected and green", `{"changes":{"result":"success",` + all + `},"pure-go":{"result":"success"},"native":{"result":"success"}}`, 0},
		{"always job skipped", `{"changes":{"result":"success",` + narrow + `},"pure-go":{"result":"skipped"},"native":{"result":"success"}}`, 1},
		{"scope job failed", `{"changes":{"result":"failure","outputs":{}},"pure-go":{"result":"success"},"native":{"result":"skipped"}}`, 1},
		{"scoped job cancelled", `{"changes":{"result":"success",` + narrow + `},"pure-go":{"result":"success"},"native":{"result":"cancelled"}}`, 1},
	} {
		cmd := exec.Command("bash", "-c", gate.Steps[0].Run)
		cmd.Env = append(os.Environ(), "NEEDS="+c.needs, "ALWAYS="+gate.Steps[0].Env["ALWAYS"])
		err := cmd.Run()
		got := 0
		if err != nil {
			exitErr, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("%s: run gate: %v", c.name, err)
			}
			got = exitErr.ExitCode()
		}
		if got != c.want {
			t.Errorf("%s: gate exited %d, want %d", c.name, got, c.want)
		}
	}
}

// Every scoped job reads a scope that scripts/ci-scope.sh prints, through the
// outputs of the changes job; a typo would silently skip a job forever.
func TestCIScopesAreDeclared(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(".github", "workflows", ciWorkflow))
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Jobs map[string]struct {
			Outputs map[string]string `yaml:"outputs"`
		} `yaml:"jobs"`
	}
	if err = yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatal(err)
	}
	declared := wf.Jobs["changes"].Outputs
	for name, value := range declared {
		if want := "${{ steps.scope.outputs." + name + " }}"; value != want {
			t.Errorf("changes output %s = %q, want %q", name, value, want)
		}
	}
	script, err := os.ReadFile(filepath.Join("scripts", "ci-scope.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for name := range declared {
		if !strings.Contains(string(script), "scopes["+name+"]=") {
			t.Errorf("changes output %q is not a scope of scripts/ci-scope.sh", name)
		}
	}
	for _, use := range strings.Split(string(raw), "needs.changes.outputs.")[1:] {
		name := use[:strings.IndexFunc(use, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r == '-' || r == '_') })]
		if _, ok := declared[name]; !ok {
			t.Errorf("a job reads needs.changes.outputs.%s, which the changes job does not declare", name)
		}
	}
}

// The native targets run inside Go CI by calling the same file the release
// dispatches at its exact tag, so the two can never drift apart.
func TestNativeBuildsRunInsideGoCI(t *testing.T) {
	t.Parallel()
	native := readWorkflow(t, ciWorkflow).Jobs["native"]
	if native.Uses != "./.github/workflows/native-release.yml" {
		t.Fatalf("Go CI native job uses %q", native.Uses)
	}
	if native.Secrets != "inherit" {
		// The native build authenticates to private Core with an inherited secret.
		t.Fatalf("Go CI native job must pass secrets: inherit, got %v", native.Secrets)
	}
	triggers := workflowTriggers(t, "native-release.yml")
	for _, want := range []string{"workflow_call", "workflow_dispatch"} {
		if !triggers[want] {
			t.Errorf("native-release.yml must be triggered by %s", want)
		}
	}
	for _, unwanted := range []string{"pull_request", "push"} {
		if triggers[unwanted] {
			t.Errorf("native-release.yml must not run on %s by itself; Go CI calls it", unwanted)
		}
	}
}

// Only Go CI runs on pull requests and pushes. Release runs on the push too,
// but gates on Go CI's verdict for the exact commit; everything else is
// privileged (workflow_run), dispatched, called or generated.
func TestOnlyGoCIRunsOnPullRequestsAndPushes(t *testing.T) {
	t.Parallel()
	allowed := map[string]bool{ciWorkflow: true, "release.yml": true}
	files, err := filepath.Glob(filepath.Join(".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	yamlFiles, err := filepath.Glob(filepath.Join(".github", "workflows", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, yamlFiles...)
	for _, file := range files {
		name := filepath.Base(file)
		if allowed[name] || strings.HasSuffix(name, ".lock.yml") {
			continue
		}
		triggers := workflowTriggers(t, name)
		if triggers["pull_request"] || triggers["push"] {
			t.Errorf("%s runs on pull_request or push; make it a job of Go CI (%s)", name, ciWorkflow)
		}
	}
	if triggers := workflowTriggers(t, "release.yml"); triggers["pull_request"] {
		t.Error("release.yml must not run on pull requests")
	}
}

func workflowTriggers(t *testing.T, name string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(".github", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]yaml.Node
	if err = yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	on, ok := doc["on"]
	if !ok {
		t.Fatalf("%s has no on:", name)
	}
	triggers := map[string]bool{}
	switch on.Kind {
	case yaml.ScalarNode:
		triggers[on.Value] = true
	case yaml.SequenceNode:
		for _, n := range on.Content {
			triggers[n.Value] = true
		}
	case yaml.MappingNode:
		for i := 0; i < len(on.Content); i += 2 {
			triggers[on.Content[i].Value] = true
		}
	}
	return triggers
}
