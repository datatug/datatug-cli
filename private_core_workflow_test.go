package main

import (
	"fmt"
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
	if err := validatePrivateCoreLocalGoJobs(workflow); err != nil {
		t.Fatal(err)
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

func privateCoreAuthPrecedesSetupGo(job string) bool {
	auth := strings.Index(job, "uses: ./.github/actions/private-core")
	setup := strings.Index(job, "uses: actions/setup-go@")
	return auth >= 0 && setup >= 0 && auth < setup
}

func TestPrivateCoreAuthOrderingRejectsMissingAndLateAuth(t *testing.T) {
	for _, ref := range []string{"v7", "b7ad1dad31e06c5925ef5d2fc7ad053ef454303e"} {
		t.Run(ref, func(t *testing.T) {
			auth := "      - uses: ./.github/actions/private-core\n"
			setup := "      - uses: actions/setup-go@" + ref + "\n"
			for _, tc := range []struct {
				name string
				job  string
				want bool
			}{
				{"before", auth + setup, true},
				{"missing", setup, false},
				{"late", setup + auth, false},
				{"no-setup", auth, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if got := privateCoreAuthPrecedesSetupGo(tc.job); got != tc.want {
						t.Fatalf("authentication ordering got %t, want %t", got, tc.want)
					}
				})
			}
		})
	}
}

func validatePrivateCoreLocalGoJobs(workflow string) error {
	expected := map[string]bool{"go-mod-tidy": false, "hermetic-tests": false, "scan-without-cgo": false, "journey-postgres": false, "mcp-apps-browser": false, "watch-firestore": false}
	headers := regexp.MustCompile(`(?m)^  [a-zA-Z0-9_-]+:\n`).FindAllStringIndex(workflow, -1)
	count := 0
	for i, header := range headers {
		end := len(workflow)
		if i+1 < len(headers) {
			end = headers[i+1][0]
		}
		job := workflow[header[1]:end]
		if !strings.Contains(job, "uses: actions/setup-go@") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimSpace(workflow[header[0]:header[1]]), ":")
		if !privateCoreAuthPrecedesSetupGo(job) {
			return fmt.Errorf("Go job %s fetches dependencies before private Core authentication", name)
		}
		seen, known := expected[name]
		if !known || seen {
			return fmt.Errorf("unexpected or duplicate local Go job %s", name)
		}
		expected[name] = true
		count++
	}
	if count != len(expected) {
		return fmt.Errorf("expected six independently authenticated named Go jobs, got %d", count)
	}
	return nil
}

func TestPrivateCoreLocalGoJobsRequireAllSixIdentities(t *testing.T) {
	names := []string{"go-mod-tidy", "hermetic-tests", "scan-without-cgo", "journey-postgres", "mcp-apps-browser", "watch-firestore"}
	for _, ref := range []string{"v7", "b7ad1dad31e06c5925ef5d2fc7ad053ef454303e"} {
		t.Run(ref, func(t *testing.T) {
			var workflow string
			for _, name := range names {
				workflow += "  " + name + ":\n      - uses: ./.github/actions/private-core\n      - uses: actions/setup-go@" + ref + "\n"
			}
			if err := validatePrivateCoreLocalGoJobs(workflow); err != nil {
				t.Fatal(err)
			}
			for _, name := range names {
				start := strings.Index(workflow, "  "+name+":\n")
				end := start + strings.Index(workflow[start:], "actions/setup-go@"+ref) + len("actions/setup-go@"+ref+"\n")
				if err := validatePrivateCoreLocalGoJobs(workflow[:start] + workflow[end:]); err == nil {
					t.Fatalf("missing expected job %s accepted", name)
				}
			}
			auth := "      - uses: ./.github/actions/private-core\n"
			setup := "      - uses: actions/setup-go@" + ref + "\n"
			for _, replacement := range []string{setup, setup + auth} {
				bad := strings.Replace(workflow, auth+setup, replacement, 1)
				if err := validatePrivateCoreLocalGoJobs(bad); err == nil {
					t.Fatal("missing or late authentication accepted")
				}
			}
		})
	}
}
