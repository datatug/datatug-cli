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

// These tests pin the release wiring that keeps the Homebrew tap current.
// From 2026-09-23 the automatic release ran GoReleaser with
// --skip=homebrew and dropped the tap token, so every release after v0.43.1
// left the tap's cask unchanged and nothing noticed. The tests fail the build
// if either half comes back: the release must publish the cask, and a job
// after publication must compare the tap with the release.

// releaseWorkflow decodes only the parts of .github/workflows/release.yml the
// tests below read.
type releaseWorkflow struct {
	Jobs map[string]struct {
		Needs   yamlStrings       `yaml:"needs"`
		If      string            `yaml:"if"`
		Timeout int               `yaml:"timeout-minutes"`
		With    map[string]any    `yaml:"with"`
		Secrets map[string]string `yaml:"secrets"`
		Steps   []struct {
			Run string `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
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

func readReleaseWorkflow(t *testing.T) releaseWorkflow {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatalf("read release.yml: %v", err)
	}
	var wf releaseWorkflow
	if err = yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatalf("parse release.yml: %v", err)
	}
	return wf
}

func TestReleaseWorkflow_PublishesTheCaskWithEveryRelease(t *testing.T) {
	t.Parallel()
	release, ok := readReleaseWorkflow(t).Jobs["release"]
	if !ok {
		t.Fatal("release.yml has no release job")
	}
	if args, _ := release.With["goreleaser_extra_args"].(string); strings.Contains(args, "homebrew") {
		t.Errorf("goreleaser_extra_args = %q: it must not skip the homebrew publisher", args)
	}
	if got, want := release.Secrets["GORELEASER_GITHUB_TOKEN"], "${{ secrets.HOMEBREW_TAP_GITHUB_TOKEN }}"; got != want {
		t.Errorf("release secret GORELEASER_GITHUB_TOKEN = %q, want %q (the tap token .goreleaser.yaml reads)", got, want)
	}
}

func TestGoReleaser_CaskPushesToTheTapWithTheForwardedToken(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(".goreleaser.yaml")
	if err != nil {
		t.Fatalf("read .goreleaser.yaml: %v", err)
	}
	var cfg struct {
		Casks []struct {
			Repository struct {
				Owner string `yaml:"owner"`
				Name  string `yaml:"name"`
				Token string `yaml:"token"`
			} `yaml:"repository"`
		} `yaml:"homebrew_casks"`
	}
	if err = yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse .goreleaser.yaml: %v", err)
	}
	if len(cfg.Casks) != 1 {
		t.Fatalf("homebrew_casks has %d entries, want 1", len(cfg.Casks))
	}
	repo := cfg.Casks[0].Repository
	if repo.Owner != "datatug" || repo.Name != "homebrew-tap" {
		t.Errorf("cask repository = %s/%s, want datatug/homebrew-tap", repo.Owner, repo.Name)
	}
	if repo.Token != "{{ .Env.GORELEASER_GITHUB_TOKEN }}" {
		t.Errorf("cask repository token = %q, want the forwarded GORELEASER_GITHUB_TOKEN", repo.Token)
	}
}

func TestReleaseWorkflow_ChecksTheTapAfterPublishing(t *testing.T) {
	t.Parallel()
	var found bool
	for name, job := range readReleaseWorkflow(t).Jobs {
		if name == "release" || !contains(job.Needs, "release") {
			continue
		}
		var runsScript bool
		for _, step := range job.Steps {
			if strings.Contains(step.Run, "scripts/verify-homebrew-cask.sh") {
				runsScript = true
			}
		}
		if !runsScript {
			continue
		}
		found = true
		if !strings.Contains(job.If, "needs.release.outputs.tag") {
			t.Errorf("job %q must run only when the release cut a tag (if: needs.release.outputs.tag != ''), got %q", name, job.If)
		}
		// Without !cancelled() the implicit success() skips the job when a smoke
		// test fails after the release went public, which is when the question
		// "did the tap move" is still open.
		if !strings.Contains(job.If, "!cancelled()") {
			t.Errorf("job %q must also run when a later job of the release failed (if: !cancelled() && ...), got %q", name, job.If)
		}
		// The job runs inside the workflow's `release` concurrency group and gh
		// has no request timeout of its own: without a limit a stalled call holds
		// every later release for GitHub's six-hour default. The script's worst
		// case is about 2.5 minutes.
		if job.Timeout < 1 || job.Timeout > 10 {
			t.Errorf("job %q needs timeout-minutes between 1 and 10 so a stalled gh call cannot hold later releases, got %d", name, job.Timeout)
		}
	}
	if !found {
		t.Error("no job after `release` runs scripts/verify-homebrew-cask.sh")
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
		casks        []string // cask.N contents; "" means the Nth read fails
		dlFail       []int    // download call numbers that fail
		checksums    string   // the release's checksums file; empty means a complete one
		wantExit     int
		wantStderr   string
		notStderr    string
		wantAPICalls int
		wantDLCalls  int
	}{
		{name: "current at once", tag: "v0.56.0", casks: []string{fresh}, wantAPICalls: 1, wantDLCalls: 1},
		{name: "current after two stale reads", tag: "v0.56.0", casks: []string{stale, stale, fresh}, wantAPICalls: 3, wantDLCalls: 3},
		{name: "current after a failed read", tag: "v0.56.0", casks: []string{"", fresh}, wantAPICalls: 2, wantDLCalls: 2},
		{name: "current after a failed download", tag: "v0.56.0", casks: []string{fresh, fresh}, dlFail: []int{1}, wantAPICalls: 1, wantDLCalls: 2},
		{name: "always stale says stale, not unreadable", tag: "v0.56.0", casks: []string{stale, stale, stale, stale}, wantExit: 1, wantStderr: "is not at v0.56.0", notStderr: "could not read", wantAPICalls: 4, wantDLCalls: 4},
		{name: "tap never readable says unreadable, not stale", tag: "v0.56.0", casks: []string{"", "", "", ""}, wantExit: 1, wantStderr: "could not read datatug/homebrew-tap", notStderr: "is not at", wantAPICalls: 4, wantDLCalls: 4},
		{name: "downloads never work says unreadable", tag: "v0.56.0", casks: []string{fresh, fresh, fresh, fresh}, dlFail: []int{1, 2, 3, 4}, wantExit: 1, wantStderr: "could not read", notStderr: "is not at", wantAPICalls: 0, wantDLCalls: 4},
		{name: "stale reads then a failed read still says stale", tag: "v0.56.0", casks: []string{stale, stale, stale, ""}, wantExit: 1, wantStderr: "is not at v0.56.0", notStderr: "could not read datatug/homebrew-tap", wantAPICalls: 4, wantDLCalls: 4},
		{name: "checksums lacking an archive is not retried and not called stale", tag: "v0.56.0", casks: []string{fresh}, checksums: "garbage\n", wantExit: 3, wantStderr: "has no checksum for", notStderr: "is not at", wantAPICalls: 1, wantDLCalls: 1},
		{name: "a bad tag is not retried", tag: "v0.56.0-rc1", casks: []string{fresh}, wantExit: 2, wantStderr: "vX.Y.Z", wantAPICalls: 1, wantDLCalls: 1},
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
			cmd := exec.Command(bash, script, tt.tag)
			cmd.Env = append(os.Environ(), "GH="+ghPath, "FAKE_DIR="+dir, "GITHUB_REPOSITORY=datatug/datatug-cli", "ATTEMPTS=4", "INTERVAL=0")
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
