package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// These tests pin the release wiring around the Homebrew tap. Publishing to
// the tap is manual, by the owner's decision of 2026-09-23 ("never update
// downstream package managers as a side effect of merging code"): a merge to
// main publishes nothing to the tap and no release can stop because of
// Homebrew. That left the tap at v0.43.1 while 39 releases followed, and
// nothing said so. The tests fail the build if the publishing wiring comes back
// without a new decision, if the warning after a release goes away or could
// fail the release, or if the manual workflow stops checking its own result.

type workflowStep struct {
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	If   string            `yaml:"if"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
	// ContinueOnError is a string because YAML may hold a bool or an expression.
	ContinueOnError string `yaml:"continue-on-error"`
}

type workflowJob struct {
	Needs           yamlStrings       `yaml:"needs"`
	If              string            `yaml:"if"`
	Timeout         int               `yaml:"timeout-minutes"`
	ContinueOnError string            `yaml:"continue-on-error"`
	Permissions     map[string]string `yaml:"permissions"`
	With            map[string]any    `yaml:"with"`
	Secrets         map[string]string `yaml:"secrets"`
	Steps           []workflowStep    `yaml:"steps"`
}

// workflowFile decodes the parts of a workflow file the tests below read.
type workflowFile struct {
	Jobs map[string]workflowJob `yaml:"jobs"`
}

// yamlStrings accepts the YAML forms of `needs:` (a scalar or a list).
type yamlStrings []string

func (s *yamlStrings) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*s = yamlStrings{n.Value}
		return nil
	}
	var list []string
	if err := n.Decode(&list); err != nil {
		return err
	}
	*s = list
	return nil
}

func readWorkflow(t *testing.T, name string) workflowFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(".github", "workflows", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var wf workflowFile
	if err = yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return wf
}

func TestReleaseWorkflow_PublishesNothingToTheTap(t *testing.T) {
	t.Parallel()
	release, ok := readWorkflow(t, "release.yml").Jobs["release"]
	if !ok {
		t.Fatal("release.yml has no release job")
	}
	args, _ := release.With["goreleaser_extra_args"].(string)
	if !strings.Contains(args, "--skip=") || !strings.Contains(args, "homebrew") {
		t.Errorf("goreleaser_extra_args = %q: the release must skip the homebrew publisher (publishing is manual)", args)
	}
	for name, value := range release.Secrets {
		if strings.Contains(name, "GORELEASER_GITHUB_TOKEN") || strings.Contains(value, "HOMEBREW_TAP") {
			t.Errorf("release secret %s = %q: the release must carry no tap token", name, value)
		}
	}
}

func TestWorkflows_OnlyThePublishWorkflowNamesTheTapToken(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob(filepath.Join(".github", "workflows", "*.yml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no workflow files found: %v", err)
	}
	var namesIt []string
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "HOMEBREW_TAP_GITHUB_TOKEN") {
			namesIt = append(namesIt, filepath.Base(f))
		}
	}
	if len(namesIt) != 1 || namesIt[0] != "publish-packages.yml" {
		t.Errorf("workflows naming HOMEBREW_TAP_GITHUB_TOKEN = %v, want only publish-packages.yml", namesIt)
	}
}

func TestReleaseWorkflow_WarnsWhenTheTapIsBehind(t *testing.T) {
	t.Parallel()
	var found bool
	for name, job := range readWorkflow(t, "release.yml").Jobs {
		if name == "release" || !contains(job.Needs, "release") {
			continue
		}
		var script *workflowStep
		for i, step := range job.Steps {
			if strings.Contains(step.Run, "scripts/verify-homebrew-cask.sh") {
				script = &job.Steps[i]
			}
		}
		if script == nil {
			continue
		}
		found = true
		if !strings.Contains(script.Run, "--warn-only") {
			t.Errorf("job %q must run the script with --warn-only (a stale tap is a warning, not a failure), got %q", name, script.Run)
		}
		// The job may never turn the run red: a warning is the whole output.
		if job.ContinueOnError != "true" {
			t.Errorf("job %q must have continue-on-error: true so it can never fail the release run, got %q", name, job.ContinueOnError)
		}
		for _, step := range job.Steps {
			if step.ContinueOnError == "false" {
				t.Errorf("job %q step %q turns continue-on-error off", name, step.Name)
			}
		}
		if !strings.Contains(job.If, "needs.release.outputs.tag") {
			t.Errorf("job %q must run only when the release cut a tag (if: needs.release.outputs.tag != ''), got %q", name, job.If)
		}
		// Without !cancelled() the implicit success() skips the job when a smoke
		// test fails after the release went public.
		if !strings.Contains(job.If, "!cancelled()") {
			t.Errorf("job %q must also run when a later job of the release failed (if: !cancelled() && ...), got %q", name, job.If)
		}
		// The job runs inside the workflow's `release` concurrency group, so a stalled
		// gh call would hold every later release. gh has no request timeout.
		if job.Timeout < 1 || job.Timeout > 5 {
			t.Errorf("job %q needs timeout-minutes between 1 and 5 so it cannot hold later releases, got %d", name, job.Timeout)
		}
		if got := job.Permissions["contents"]; got != "read" || len(job.Permissions) != 1 {
			t.Errorf("job %q permissions = %v, want only contents: read", name, job.Permissions)
		}
		allowedEnv := map[string]bool{"GH_TOKEN": true, "TAG": true, "ATTEMPTS": true, "INTERVAL": true}
		for key, value := range script.Env {
			if !allowedEnv[key] || strings.Contains(value, "secrets.") {
				t.Errorf("job %q env %s = %q: the warning reads the public tap with the workflow's own token and needs no secret", name, key, value)
			}
		}
		if script.Env["GH_TOKEN"] != "${{ github.token }}" {
			t.Errorf("job %q GH_TOKEN = %q, want the workflow's own ${{ github.token }}", name, script.Env["GH_TOKEN"])
		}
	}
	if !found {
		t.Error("no job after `release` runs scripts/verify-homebrew-cask.sh")
	}
}

func TestPublishWorkflow_VerifiesTheTapAfterPushing(t *testing.T) {
	t.Parallel()
	job, ok := readWorkflow(t, "publish-packages.yml").Jobs["homebrew"]
	if !ok {
		t.Fatal("publish-packages.yml has no homebrew job")
	}
	steps := job.Steps
	if len(steps) == 0 {
		t.Fatal("homebrew job has no steps")
	}
	last := steps[len(steps)-1]
	if !strings.Contains(last.Run, "scripts/verify-homebrew-cask.sh") {
		t.Fatalf("the last step of the manual workflow must run scripts/verify-homebrew-cask.sh, got %q", last.Run)
	}
	if strings.Contains(last.Run, "--warn-only") {
		t.Errorf("the manual workflow's check must fail the run, not warn: %q", last.Run)
	}
	if last.ContinueOnError == "true" || last.If != "" {
		t.Errorf("the verify step must run after a successful push and be able to fail the run (continue-on-error %q, if %q)", last.ContinueOnError, last.If)
	}
	// It must come after the step that pushes to the tap.
	var pushAt = -1
	for i, step := range steps {
		if strings.Contains(step.Run, "git push") {
			pushAt = i
		}
	}
	if pushAt < 0 || pushAt >= len(steps)-1 {
		t.Errorf("the verify step must come after the step that pushes the cask (push at %d of %d steps)", pushAt, len(steps))
	}
	if last.Env["TAG"] == "" || last.Env["GH_TOKEN"] == "" {
		t.Errorf("the verify step needs TAG and GH_TOKEN, got %v", last.Env)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

const (
	sumDarwinArm64 = "1111111111111111111111111111111111111111111111111111111111111111"
	sumDarwinAmd64 = "2222222222222222222222222222222222222222222222222222222222222222"
	sumLinuxArm64  = "3333333333333333333333333333333333333333333333333333333333333333"
	sumLinuxAmd64  = "4444444444444444444444444444444444444444444444444444444444444444"
)

func caskFixture(version string, sums [4]string) string {
	return fmt.Sprintf(`cask "datatug" do
  version "%s"

  on_macos do
    on_arm do
      sha256 "%s"
    end
    on_intel do
      sha256 "%s"
    end
  end
  on_linux do
    on_arm do
      sha256 "%s"
    end
    on_intel do
      sha256 "%s"
    end
  end

  binary "datatug"
end
`, version, sums[0], sums[1], sums[2], sums[3])
}

func checksumsFixture(version string) string {
	return fmt.Sprintf("%s  datatug_%s_darwin_arm64.tar.gz\n%s  datatug_%s_darwin_amd64.tar.gz\n%s  datatug_%s_linux_arm64.tar.gz\n%s  datatug_%s_linux_amd64.tar.gz\n%s  datatug_%s_windows_amd64.zip\n",
		sumDarwinArm64, version, sumDarwinAmd64, version, sumLinuxArm64, version, sumLinuxAmd64, version,
		strings.Repeat("5", 64), version)
}

func TestCheckHomebrewCaskScript(t *testing.T) {
	t.Parallel()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not available")
	}
	script, err := filepath.Abs(filepath.Join("scripts", "check-homebrew-cask.sh"))
	if err != nil {
		t.Fatal(err)
	}
	current := [4]string{sumDarwinArm64, sumDarwinAmd64, sumLinuxArm64, sumLinuxAmd64}
	stale := [4]string{sumDarwinArm64, sumDarwinAmd64, sumLinuxArm64, strings.Repeat("9", 64)}

	tests := []struct {
		name       string
		tag        string
		cask       string // empty means: no cask file at all
		checksums  string // empty means: version-only check
		wantExit   int
		wantStderr string
	}{
		{name: "tap is current", tag: "v0.56.0", cask: caskFixture("0.56.0", current), checksums: checksumsFixture("0.56.0")},
		{name: "tap is current, version only", tag: "v0.56.0", cask: caskFixture("0.56.0", current)},
		{name: "tap is behind", tag: "v0.56.0", cask: caskFixture("0.43.1", current), wantExit: 1, wantStderr: "0.43.1"},
		{name: "tap is ahead", tag: "v0.56.0", cask: caskFixture("0.57.0", current), wantExit: 1, wantStderr: "0.57.0"},
		{name: "tap has a prefix of the version", tag: "v0.5.0", cask: caskFixture("0.55.0", current), wantExit: 1, wantStderr: "0.55.0"},
		{name: "version right, one checksum wrong", tag: "v0.56.0", cask: caskFixture("0.56.0", stale), checksums: checksumsFixture("0.56.0"), wantExit: 1, wantStderr: "linux_amd64"},
		{name: "checksums lack an archive", tag: "v0.56.0", cask: caskFixture("0.56.0", current), checksums: "garbage\n", wantExit: 3, wantStderr: "darwin_arm64"},
		{name: "cask has no version line", tag: "v0.56.0", cask: "cask \"datatug\" do\nend\n", wantExit: 1, wantStderr: "no version"},
		{name: "tag is not a stable release", tag: "v0.56.0-rc1", cask: caskFixture("0.56.0", current), wantExit: 2, wantStderr: "vX.Y.Z"},
		{name: "cask file is missing", tag: "v0.56.0", wantExit: 2, wantStderr: "cask file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			caskPath := filepath.Join(dir, "datatug.rb")
			if tt.cask != "" {
				if err := os.WriteFile(caskPath, []byte(tt.cask), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{script, tt.tag, caskPath}
			if tt.checksums != "" {
				sums := filepath.Join(dir, "checksums.txt")
				if err := os.WriteFile(sums, []byte(tt.checksums), 0o600); err != nil {
					t.Fatal(err)
				}
				args = append(args, sums)
			}
			cmd := exec.Command(bash, args...)
			var stderr strings.Builder
			cmd.Stderr = &stderr
			err := cmd.Run()
			exit := 0
			if ee, ok := err.(*exec.ExitError); ok {
				exit = ee.ExitCode()
			} else if err != nil {
				t.Fatalf("run script: %v", err)
			}
			if exit != tt.wantExit {
				t.Fatalf("exit code = %d, want %d; stderr: %s", exit, tt.wantExit, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

// fakeGH stands in for the gh CLI so the verify script's retry logic can be
// exercised without a network. State lives in the directory named by FAKE_DIR:
//
//   - the Nth `gh api` call prints cask.N and fails when cask.N does not exist;
//   - the Nth `gh release download` call fails when dl_fail.N exists, otherwise
//     writes the release's checksums file;
//   - each call appends to api.calls or dl.calls, which the tests count.
const fakeGH = `#!/usr/bin/env bash
set -euo pipefail
case "$1" in
  release)
    echo x >> "$FAKE_DIR/dl.calls"
    n=$(wc -l < "$FAKE_DIR/dl.calls" | tr -d ' ')
    [[ -e "$FAKE_DIR/dl_fail.$n" ]] && { echo "gh: HTTP 502" >&2; exit 1; }
    dir=""
    while [[ $# -gt 0 ]]; do
      [[ "$1" == "--dir" ]] && dir="$2"
      shift
    done
    cp "$FAKE_DIR/checksums.txt" "$dir/datatug_0.56.0_checksums.txt"
    ;;
  api)
    echo x >> "$FAKE_DIR/api.calls"
    n=$(wc -l < "$FAKE_DIR/api.calls" | tr -d ' ')
    [[ -e "$FAKE_DIR/cask.$n" ]] || { echo "gh: HTTP 502" >&2; exit 1; }
    cat "$FAKE_DIR/cask.$n"
    ;;
  *) echo "unexpected gh call: $*" >&2; exit 64 ;;
esac
`

func TestVerifyHomebrewCaskScript(t *testing.T) {
	t.Parallel()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not available")
	}
	script, err := filepath.Abs(filepath.Join("scripts", "verify-homebrew-cask.sh"))
	if err != nil {
		t.Fatal(err)
	}
	current := [4]string{sumDarwinArm64, sumDarwinAmd64, sumLinuxArm64, sumLinuxAmd64}
	fresh := caskFixture("0.56.0", current)
	stale := caskFixture("0.43.1", current)

	tests := []struct {
		name         string
		tag          string
		warn         bool     // run with --warn-only, as the release run does
		noRepo       bool     // GITHUB_REPOSITORY unset
		noArgs       bool     // no tag argument at all
		casks        []string // cask.N contents; "" means the Nth read fails
		dlFail       []int    // download call numbers that fail
		checksums    string   // the release's checksums file; empty means a complete one
		wantExit     int
		wantAnn      string // "error" or "warning": the one annotation line; "" for none
		wantStderr   string
		notStderr    string
		wantAPICalls int
		wantDLCalls  int
	}{
		{name: "current at once", tag: "v0.56.0", casks: []string{fresh}, wantAPICalls: 1, wantDLCalls: 1},
		{name: "current after two stale reads", tag: "v0.56.0", casks: []string{stale, stale, fresh}, wantAPICalls: 3, wantDLCalls: 3},
		{name: "current after a failed read", tag: "v0.56.0", casks: []string{"", fresh}, wantAPICalls: 2, wantDLCalls: 2},
		{name: "current after a failed download", tag: "v0.56.0", casks: []string{fresh, fresh}, dlFail: []int{1}, wantAPICalls: 1, wantDLCalls: 2},
		{name: "always stale says stale, not unreadable", tag: "v0.56.0", casks: []string{stale, stale, stale, stale}, wantExit: 1, wantAnn: "error", wantStderr: "does not carry the release v0.56.0", notStderr: "could not check", wantAPICalls: 4, wantDLCalls: 4},
		{name: "tap never readable says unreadable, not stale", tag: "v0.56.0", casks: []string{"", "", "", ""}, wantExit: 1, wantAnn: "error", wantStderr: "could not read datatug/homebrew-tap", notStderr: "does not carry", wantAPICalls: 4, wantDLCalls: 4},
		{name: "downloads never work says unreadable", tag: "v0.56.0", casks: []string{fresh, fresh, fresh, fresh}, dlFail: []int{1, 2, 3, 4}, wantExit: 1, wantAnn: "error", wantStderr: "could not read", notStderr: "does not carry", wantAPICalls: 0, wantDLCalls: 4},
		{name: "stale reads then a failed read still says stale", tag: "v0.56.0", casks: []string{stale, stale, stale, ""}, wantExit: 1, wantAnn: "error", wantStderr: "does not carry the release v0.56.0", notStderr: "could not read datatug/homebrew-tap", wantAPICalls: 4, wantDLCalls: 4},
		{name: "checksums lacking an archive is not retried and not called stale", tag: "v0.56.0", casks: []string{fresh}, checksums: "garbage\n", wantExit: 3, wantAnn: "error", wantStderr: "has no checksum for", notStderr: "does not carry", wantAPICalls: 1, wantDLCalls: 1},
		{name: "a bad tag is not retried", tag: "v0.56.0-rc1", casks: []string{fresh}, wantExit: 2, wantAnn: "error", wantStderr: "vX.Y.Z", wantAPICalls: 1, wantDLCalls: 1},
		{name: "no tag argument", noArgs: true, wantExit: 2, wantAnn: "error", wantStderr: "usage"},
		{name: "no repository", tag: "v0.56.0", noRepo: true, wantExit: 2, wantAnn: "error", wantStderr: "GITHUB_REPOSITORY"},

		// --warn-only: the release run. Every outcome exits 0; anything but
		// "current" is exactly one ::warning:: line.
		{name: "warn: current says nothing", tag: "v0.56.0", warn: true, casks: []string{fresh}, notStderr: "::warning::", wantAPICalls: 1, wantDLCalls: 1},
		{name: "warn: stale names both versions and the workflow", tag: "v0.56.0", warn: true, casks: []string{stale, stale, stale, stale}, wantAnn: "warning", wantStderr: "0.43.1, but the release is 0.56.0", notStderr: "::error::", wantAPICalls: 4, wantDLCalls: 4},
		{name: "warn: stale names the workflow to start", tag: "v0.56.0", warn: true, casks: []string{stale, stale, stale, stale}, wantAnn: "warning", wantStderr: `"Publish latest packages"`, wantAPICalls: 4, wantDLCalls: 4},
		{name: "warn: tap unreadable is a warning that it could not check", tag: "v0.56.0", warn: true, casks: []string{"", "", "", ""}, wantAnn: "warning", wantStderr: "::warning::could not check", notStderr: "::error::", wantAPICalls: 4, wantDLCalls: 4},
		{name: "warn: downloads failing is a warning that it could not check", tag: "v0.56.0", warn: true, dlFail: []int{1, 2, 3, 4}, casks: []string{fresh}, wantAnn: "warning", wantStderr: "::warning::could not check", wantDLCalls: 4},
		{name: "warn: checksums lacking an archive is a warning", tag: "v0.56.0", warn: true, casks: []string{fresh}, checksums: "garbage\n", wantAnn: "warning", wantStderr: "has no checksum for", notStderr: "::error::", wantAPICalls: 1, wantDLCalls: 1},
		{name: "warn: a bad tag is a warning", tag: "v0.56.0-rc1", warn: true, casks: []string{fresh}, wantAnn: "warning", wantStderr: "vX.Y.Z", notStderr: "::error::", wantAPICalls: 1, wantDLCalls: 1},
		{name: "warn: no tag argument is a warning", noArgs: true, warn: true, wantAnn: "warning", wantStderr: "usage", notStderr: "::error::"},
		{name: "warn: no repository is a warning", tag: "v0.56.0", warn: true, noRepo: true, wantAnn: "warning", wantStderr: "GITHUB_REPOSITORY", notStderr: "::error::"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			write := func(name, content string) {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ghPath := filepath.Join(dir, "gh")
			if err := os.WriteFile(ghPath, []byte(fakeGH), 0o700); err != nil {
				t.Fatal(err)
			}
			sums := tt.checksums
			if sums == "" {
				sums = checksumsFixture("0.56.0")
			}
			write("checksums.txt", sums)
			write("api.calls", "")
			write("dl.calls", "")
			for i, c := range tt.casks {
				if c != "" {
					write(fmt.Sprintf("cask.%d", i+1), c)
				}
			}
			for _, n := range tt.dlFail {
				write(fmt.Sprintf("dl_fail.%d", n), "")
			}
			var args []string
			if tt.warn {
				args = append(args, "--warn-only")
			}
			if !tt.noArgs {
				args = append(args, tt.tag)
			}
			cmd := exec.Command(bash, append([]string{script}, args...)...)
			cmd.Env = append(os.Environ(), "GH="+ghPath, "FAKE_DIR="+dir, "GITHUB_REPOSITORY=datatug/datatug-cli", "ATTEMPTS=4", "INTERVAL=0")
			if tt.noRepo {
				cmd.Env = append(cmd.Env, "GITHUB_REPOSITORY=")
			}
			// The final ::error:: line goes to stdout so GitHub shows it as an
			// annotation; read both streams.
			var stderr strings.Builder
			cmd.Stdout = &stderr
			cmd.Stderr = &stderr
			err := cmd.Run()
			exit := 0
			if ee, ok := err.(*exec.ExitError); ok {
				exit = ee.ExitCode()
			} else if err != nil {
				t.Fatalf("run script: %v", err)
			}
			if exit != tt.wantExit {
				t.Fatalf("exit code = %d, want %d; stderr: %s", exit, tt.wantExit, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantStderr)
			}
			if tt.notStderr != "" && strings.Contains(stderr.String(), tt.notStderr) {
				t.Errorf("stderr = %q, must not contain %q", stderr.String(), tt.notStderr)
			}
			// Every non-zero outcome says why in exactly one annotation line.
			var annotations []string
			for _, line := range strings.Split(stderr.String(), "\n") {
				if strings.HasPrefix(line, "::") {
					annotations = append(annotations, line)
				}
			}
			wantLines := 0
			if tt.wantAnn != "" {
				wantLines = 1
			}
			if len(annotations) != wantLines {
				t.Errorf("annotation lines = %q, want %d", annotations, wantLines)
			}
			if tt.wantAnn != "" && len(annotations) == 1 && !strings.HasPrefix(annotations[0], "::"+tt.wantAnn+"::") {
				t.Errorf("annotation = %q, want a ::%s:: line", annotations[0], tt.wantAnn)
			}
			for file, want := range map[string]int{"api.calls": tt.wantAPICalls, "dl.calls": tt.wantDLCalls} {
				raw, err := os.ReadFile(filepath.Join(dir, file))
				if err != nil {
					t.Fatal(err)
				}
				if got := strings.Count(string(raw), "x"); got != want {
					t.Errorf("%s: %d calls, want %d", file, got, want)
				}
			}
		})
	}
}
