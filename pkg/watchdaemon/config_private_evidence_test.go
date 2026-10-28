package watchdaemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigPrivateExecutionEvidenceOptIn(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{{`{"runner":{}}`, false}, {`{"runner":{"privateExecutionEvidence":true}}`, true}, {`{"runner":{"privateExecutionEvidence":false}}`, false}} {
		path := filepath.Join(t.TempDir(), "watch.json")
		if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadConfig(path)
		if err != nil || cfg.Runner.PrivateExecutionEvidence != tc.want {
			t.Fatalf("private execution option=%v err=%v", cfg.Runner.PrivateExecutionEvidence, err)
		}
	}
}
