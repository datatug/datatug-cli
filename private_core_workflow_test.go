package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestPrivateCoreCredentialMatchesOnlyExactRepository(t *testing.T) {
	data, err := os.ReadFile(".github/actions/private-core/action.yml")
	if err != nil {
		t.Fatal(err)
	}
	action := string(data)
	for _, value := range []string{"uses: actions/create-github-app-token@64ab729a6294000f5aa4a4bc0b73d1f32bc45510", "client-id: Iv23li26w131Kv6u3IdM", "owner: datatug", "repositories: datatug-core", "permission-contents: read"} {
		if !strings.Contains(action, value) {
			t.Fatalf("missing scoped App input %s", value)
		}
	}
	start := strings.Index(action, "      run: |\n")
	if start < 0 {
		t.Fatal("missing credential script")
	}
	script := strings.ReplaceAll(action[start+len("      run: |\n"):], "        ", "")
	temp := t.TempDir()
	run := exec.Command("bash", "-c", script)
	run.Env = []string{"PATH=" + os.Getenv("PATH"), "RUNNER_TEMP=" + temp, "GITHUB_JOB=test", "GITHUB_ENV=" + filepath.Join(temp, "env"), "CORE_READ_TOKEN=TEST_READ_TOKEN"}
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("credential setup failed: %v %s", err, out)
	}
	for _, repo := range []string{"datatug/datatug-core", "datatug/datatug-core.git", "datatug/datatug-core-tools", "other/datatug-core", "datatug/datatug"} {
		command := exec.Command("git", "credential", "fill")
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_GLOBAL=" + filepath.Join(temp, "core-test.gitconfig"), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0"}
		command.Stdin = strings.NewReader("protocol=https\nhost=github.com\npath=" + repo + "\n\n")
		out, err := command.CombinedOutput()
		exact := repo == "datatug/datatug-core" || repo == "datatug/datatug-core.git"
		if exact && (err != nil || !strings.Contains(string(out), "password=TEST_READ_TOKEN")) {
			t.Fatalf("exact Core path failed: %s %v", repo, err)
		}
		if !exact && strings.Contains(string(out), "TEST_READ_TOKEN") {
			t.Fatalf("Core credential exposed to %s", repo)
		}
	}
}

func TestPrivateCoreAuthPrecedesEveryLocalGoJob(t *testing.T) {
	data, err := os.ReadFile(".github/workflows/golangci.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(data)
	jobs := regexp.MustCompile(`(?m)^  [a-zA-Z0-9_-]+:\n`).Split(workflow, -1)
	count := 0
	for _, job := range jobs {
		if !strings.Contains(job, "uses: actions/setup-go@v7") {
			continue
		}
		count++
		auth := strings.Index(job, "uses: ./.github/actions/private-core")
		setup := strings.Index(job, "uses: actions/setup-go@v7")
		if auth < 0 || auth > setup {
			t.Fatal("Go job fetches dependencies before private Core authentication")
		}
	}
	if count != 4 {
		t.Fatalf("expected four independently authenticated Go jobs, got %d", count)
	}
	if !strings.Contains(workflow, "GOPRIVATE: github.com/datatug/datatug-core") {
		t.Fatal("private Core could reach public module proxy")
	}
	release, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"goprivate_github_app_repositories: datatug-core", "GOPRIVATE_GITHUB_APP_PRIVATE_KEY: ${{ secrets.SNEAT_RELEASER_PRIVATE_KEY }}", `bash scripts/publish-datatug-release.sh "$TAG" "datatug/datatug-cli" "datatug/datatug"`, "require_notarized_macos: true", "require_workflow_success: 'Go CI, Everywhere candidate acceptance'"} {
		if !strings.Contains(string(release), required) {
			t.Fatalf("release invariant missing: %s", required)
		}
	}
}
