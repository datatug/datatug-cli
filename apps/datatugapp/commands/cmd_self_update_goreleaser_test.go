package commands

import (
	"os"
	"sort"
	"testing"

	"github.com/strongo/cli-helpers/cliinstall"
	"github.com/strongo/cli-helpers/selfupdate"
	"gopkg.in/yaml.v3"
)

// goreleaserConfig decodes only the .goreleaser.yaml fields this consumer
// test needs to compare against the compiled-in catalog entry
// (cli-install#req:catalog-identity-single-source): archive/checksums
// naming, the release repository, the supported platform matrix, and the
// Homebrew cask coordinates. It is deliberately not a full GoReleaser
// schema.
type goreleaserConfig struct {
	ProjectName string `yaml:"project_name"`
	Builds      []struct {
		GOOS   []string `yaml:"goos"`
		GOARCH []string `yaml:"goarch"`
		Ignore []struct {
			GOOS   string `yaml:"goos"`
			GOARCH string `yaml:"goarch"`
		} `yaml:"ignore"`
	} `yaml:"builds"`
	Archives []struct {
		NameTemplate string `yaml:"name_template"`
	} `yaml:"archives"`
	Checksum struct {
		NameTemplate string `yaml:"name_template"`
	} `yaml:"checksum"`
	Release struct {
		GitHub struct {
			Owner string `yaml:"owner"`
			Name  string `yaml:"name"`
		} `yaml:"github"`
	} `yaml:"release"`
	HomebrewCasks []struct {
		Name       string `yaml:"name"`
		Repository struct {
			Owner string `yaml:"owner"`
			Name  string `yaml:"name"`
		} `yaml:"repository"`
	} `yaml:"homebrew_casks"`
}

// AC: cli-install#ac:catalog-matrix-is-valid — a consumer-side offline test
// asserting that datatug's own .goreleaser.yaml archive name template,
// checksum name template, release repository, platform matrix, and cask
// token match its cli-helpers catalog entry, so drift fails datatug's own
// CI rather than surfacing as a 404 (or a stale cask token) in someone
// else's `install datatug` (cli-install#req:catalog-identity-single-source).
func TestDatatugCatalogEntry_MatchesGoReleaserConfig(t *testing.T) {
	t.Parallel()

	entry, ok := cliinstall.ByID("datatug")
	if !ok {
		t.Fatal(`no catalog entry for "datatug"`)
	}

	raw, err := os.ReadFile("../../../.goreleaser.yaml")
	if err != nil {
		t.Fatalf("read .goreleaser.yaml: %v", err)
	}
	var gr goreleaserConfig
	if err := yaml.Unmarshal(raw, &gr); err != nil {
		t.Fatalf("parse .goreleaser.yaml: %v", err)
	}

	if gr.ProjectName != entry.ID {
		t.Errorf("goreleaser project_name = %q, want catalog id %q", gr.ProjectName, entry.ID)
	}
	if got, want := gr.Release.GitHub.Owner+"/"+gr.Release.GitHub.Name, entry.Repository; got != want {
		t.Errorf("goreleaser release.github = %q, want catalog Repository %q", got, want)
	}
	if entry.TagPrefix != "" {
		t.Errorf("catalog TagPrefix = %q, want empty: datatug publishes one product per repository", entry.TagPrefix)
	}

	// checksum: the catalog entry leaves ChecksumsName nil, which means "use
	// the library's own GoReleaser-shaped default"
	// ("<binary>_<version>_checksums.txt") — verify .goreleaser.yaml's own
	// template is exactly that shape, so the two can never silently diverge.
	const wantChecksumTemplate = "{{ .ProjectName }}_{{ .Version }}_checksums.txt"
	if gr.Checksum.NameTemplate != wantChecksumTemplate {
		t.Errorf(".goreleaser.yaml checksum.name_template = %q, want %q", gr.Checksum.NameTemplate, wantChecksumTemplate)
	}
	if entry.ChecksumsName != nil {
		t.Error("catalog entry.ChecksumsName is overridden; want nil (the shared GoReleaser default matches .goreleaser.yaml)")
	}

	// archive naming: same reasoning as checksums, for every declared
	// archive block (datatug publishes a tarball id and a windows zip id,
	// both sharing the same name_template).
	if len(gr.Archives) == 0 {
		t.Fatal(".goreleaser.yaml declares no archives")
	}
	const wantArchiveTemplate = "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"
	for i, a := range gr.Archives {
		if a.NameTemplate != wantArchiveTemplate {
			t.Errorf(".goreleaser.yaml archives[%d].name_template = %q, want %q (the library's default asset-naming shape)",
				i, a.NameTemplate, wantArchiveTemplate)
		}
	}
	if entry.AssetName != nil {
		t.Error("catalog entry.AssetName is overridden; want nil (the shared GoReleaser default matches .goreleaser.yaml)")
	}

	// platform matrix: union of every builds[].goos x goarch (minus each
	// build's own ignore list) across BOTH build ids (datatug-unix,
	// datatug-windows) must equal entry.SupportedPlatforms exactly
	// (order-independent).
	if len(gr.Builds) == 0 {
		t.Fatal(".goreleaser.yaml declares no builds")
	}
	var goreleaserPlatforms []selfupdate.Platform
	for _, build := range gr.Builds {
		ignored := make(map[[2]string]bool, len(build.Ignore))
		for _, ig := range build.Ignore {
			ignored[[2]string{ig.GOOS, ig.GOARCH}] = true
		}
		for _, goos := range build.GOOS {
			for _, goarch := range build.GOARCH {
				if ignored[[2]string{goos, goarch}] {
					continue
				}
				goreleaserPlatforms = append(goreleaserPlatforms, selfupdate.Platform{GOOS: goos, GOARCH: goarch})
			}
		}
	}

	if got, want := sortedPlatforms(goreleaserPlatforms), sortedPlatforms(entry.SupportedPlatforms); !platformsEqual(got, want) {
		t.Errorf("goreleaser platform matrix = %v, want catalog entry.SupportedPlatforms %v", got, want)
	}

	// cask token: "<homebrew_casks[].repository.owner>/tap/<name>", matching
	// the convention every other catalog entry's CaskToken follows.
	if len(gr.HomebrewCasks) == 0 {
		t.Fatal(".goreleaser.yaml declares no homebrew_casks")
	}
	cask := gr.HomebrewCasks[0]
	wantCaskToken := cask.Repository.Owner + "/tap/" + cask.Name
	if entry.CaskToken != wantCaskToken {
		t.Errorf("catalog entry.CaskToken = %q, want %q (from .goreleaser.yaml homebrew_casks[0])", entry.CaskToken, wantCaskToken)
	}
}

func sortedPlatforms(in []selfupdate.Platform) []selfupdate.Platform {
	out := append([]selfupdate.Platform(nil), in...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].GOOS != out[j].GOOS {
			return out[i].GOOS < out[j].GOOS
		}
		return out[i].GOARCH < out[j].GOARCH
	})
	return out
}

func platformsEqual(a, b []selfupdate.Platform) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
