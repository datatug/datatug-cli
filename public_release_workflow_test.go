package main

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPublicReleasePublisherUsesExactRepositoryAppToken(t *testing.T) {
	t.Parallel()
	var workflow struct {
		Jobs map[string]struct {
			Needs       yamlStrings       `yaml:"needs"`
			If          string            `yaml:"if"`
			Permissions map[string]string `yaml:"permissions"`
			Steps       []struct {
				ID   string            `yaml:"id"`
				Uses string            `yaml:"uses"`
				With map[string]string `yaml:"with"`
				Env  map[string]string `yaml:"env"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	source, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err = yaml.Unmarshal(source, &workflow); err != nil {
		t.Fatal(err)
	}
	job, ok := workflow.Jobs["publish-datatug-release"]
	if !ok {
		t.Fatal("public publisher absent")
	}
	if !reflect.DeepEqual(job.Needs, yamlStrings{"release"}) || !strings.Contains(job.If, "needs.release.outputs.tag != ''") {
		t.Fatal("publisher must wait for the verified release tag")
	}
	if !reflect.DeepEqual(job.Permissions, map[string]string{"contents": "read"}) {
		t.Fatalf("source token permissions widened: %v", job.Permissions)
	}
	tokenFound, publishFound := false, false
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, "actions/checkout@") {
			if step.Uses != "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1" {
				t.Fatal("publisher checkout action must stay pinned")
			}
			if step.With["persist-credentials"] != "false" {
				t.Fatal("publisher checkout must not persist a reusable credential")
			}
		}
		if step.ID == "public-release-token" {
			tokenFound = true
			if step.Uses != "actions/create-github-app-token@64ab729a6294000f5aa4a4bc0b73d1f32bc45510" {
				t.Fatalf("unpinned App action: %s", step.Uses)
			}
			want := map[string]string{"client-id": "Iv23li26w131Kv6u3IdM", "private-key": "${{ secrets.SNEAT_RELEASER_PRIVATE_KEY }}", "owner": "datatug", "repositories": "datatug", "permission-contents": "write"}
			if !reflect.DeepEqual(step.With, want) {
				t.Fatalf("public token must be restricted to one repository and contents:write: %v", step.With)
			}
		}
		if strings.Contains(step.Run, "scripts/publish-datatug-release.sh") {
			publishFound = true
			if !tokenFound {
				t.Fatal("publisher runs before its scoped token is minted")
			}
			if step.Env["GH_SOURCE_TOKEN"] != "${{ github.token }}" || step.Env["GH_TARGET_TOKEN"] != "${{ steps.public-release-token.outputs.token }}" {
				t.Fatalf("publisher must separate source and target credentials: %v", step.Env)
			}
			if step.Env["TAG"] != "${{ needs.release.outputs.tag }}" {
				t.Fatal("publisher must mirror the exact verified tag")
			}
			if !strings.Contains(step.Run, `bash scripts/publish-datatug-release.sh "$TAG" "datatug/datatug-cli" "datatug/datatug"`) {
				t.Fatal("publisher source, destination or provenance-preserving script changed")
			}
		}
	}
	if !tokenFound || !publishFound {
		t.Fatal("scoped token or publisher step absent")
	}
}
