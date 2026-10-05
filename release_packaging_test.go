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
			if strings.Contains(step.Run, "scripts/check-homebrew-cask.sh") {
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
	}
	if !found {
		t.Error("no job after `release` runs scripts/check-homebrew-cask.sh")
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
		{name: "checksums lack an archive", tag: "v0.56.0", cask: caskFixture("0.56.0", current), checksums: "garbage\n", wantExit: 1, wantStderr: "darwin_arm64"},
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
