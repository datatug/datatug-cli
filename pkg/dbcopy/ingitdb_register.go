// ingitdb_register.go — workaround for the inGitDB driver gap:
// ddl.CreateCollection writes <project>/<name>/.collection/definition.yaml
// but does not register the collection in <project>/.ingitdb/root-collections.yaml,
// which is what the validator-backed CollectionsReader uses to load the
// project Definition for transactions. Without this entry,
// RunReadwriteTransaction → InsertMulti fails with "collection X not found
// in definition".
//
// We append entries to root-collections.yaml as a flat YAML map (id: path)
// so the next loadDefinition sees them. When upstream adds a public API
// (or auto-registers inside CreateCollection), this file can be deleted.
//
// Tracked at docs/upstream-issues/ingitdb-cli-auto-register-collection.md
// (to be filed).

package dbcopy

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/ingitdb/ingitdb-cli/pkg/ingitdb/config"
)

// registerInGitDBCollections writes <projectPath>/.ingitdb/root-collections.yaml
// containing one entry per name (id: name, path: name — identity mapping
// matches what crud_test.go does). Idempotent: re-writes the file each call.
func registerInGitDBCollections(projectPath string, names []string) error {
	if projectPath == "" {
		return fmt.Errorf("registerInGitDBCollections: empty projectPath")
	}
	dir := filepath.Join(projectPath, config.IngitDBDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("registerInGitDBCollections: mkdir %s: %w", dir, err)
	}

	// Sort for deterministic output.
	sorted := make([]string, len(names))
	copy(sorted, names)
	sort.Strings(sorted)

	var buf []byte
	for _, n := range sorted {
		buf = append(buf, []byte(n+": "+n+"\n")...)
	}

	file := filepath.Join(dir, config.RootCollectionsFileName)
	if err := os.WriteFile(file, buf, 0o644); err != nil {
		return fmt.Errorf("registerInGitDBCollections: write %s: %w", file, err)
	}
	return nil
}
