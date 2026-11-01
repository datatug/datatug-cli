//go:build !windows

package crossverify

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Run the actual version probe with a producer larger than a pipe buffer.
// Exiting its consumer early used to abort the Oracle journey under pipefail.
func TestOracleJourneyVersionProbeConsumesCompleteOutput(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "scripts", "check-journey-oracle.sh"))
	if err != nil {
		t.Fatal(err)
	}
	var probe string
	for _, line := range strings.Split(string(source), "\n") {
		if strings.HasPrefix(line, "sql_root <<<") && strings.Contains(line, "banner_full") {
			if probe != "" {
				t.Fatal("multiple version probes")
			}
			probe = line
		}
	}
	if probe == "" {
		t.Fatal("the actual Oracle version probe was not found")
	}
	banner := filepath.Join(t.TempDir(), "banner.txt")
	if err = os.WriteFile(banner, []byte("Oracle test version\nVersion test build\n"+strings.Repeat("remaining diagnostic output\n", 100000)), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", "set -euo pipefail\nsql_root() { cat \"$ORACLE_TEST_BANNER\"; }\n"+probe+"\n")
	cmd.Env = append(os.Environ(), "ORACLE_TEST_BANNER="+banner)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("actual version probe failed: %v\n%s", err, output)
	}
	if string(output) != "server version Oracle test version\nserver version Version test build\n" {
		t.Fatalf("unexpected version output: %q", output)
	}
}
