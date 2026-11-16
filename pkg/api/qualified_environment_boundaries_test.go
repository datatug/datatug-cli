package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQualifiedLegacyAndCurrentEnvironmentReadersRejectBrokenFiles(t *testing.T) {
	for _, mode := range []string{"current escape", "legacy escape", "current type", "legacy identity", "legacy type", "invalid directory", "nested entity escape", "flat entity escape"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "environments/dev")
			_ = os.MkdirAll(dir, 0700)
			outside := filepath.Join(t.TempDir(), "source")
			_ = os.WriteFile(outside, []byte(`{}`), 0600)
			switch mode {
			case "current escape":
				_ = os.Symlink(outside, filepath.Join(dir, "dev.env.json"))
			case "legacy escape":
				_ = os.Symlink(outside, filepath.Join(dir, "environment-summary.json"))
			case "current type":
				_ = os.WriteFile(filepath.Join(dir, "dev.env.json"), []byte(`{"id":"dev","editionConnections":42}`), 0600)
			case "legacy identity":
				_ = os.WriteFile(filepath.Join(dir, "environment-summary.json"), []byte(`{"id":"foreign"}`), 0600)
			case "legacy type":
				_ = os.WriteFile(filepath.Join(dir, "environment-summary.json"), []byte(`{"id":"dev","dbServers":42}`), 0600)
			case "invalid directory":
				_ = os.MkdirAll(filepath.Join(root, "environments", strings.Repeat("x", 129)), 0700)
			case "nested entity escape":
				_ = os.MkdirAll(filepath.Join(root, "entities/customer"), 0700)
				_ = os.Symlink(outside, filepath.Join(root, "entities/customer/customer.entity.json"))
			case "flat entity escape":
				_ = os.MkdirAll(filepath.Join(root, "entities"), 0700)
				_ = os.Symlink(outside, filepath.Join(root, "entities/customer.entity.json"))
			}
			reader, err := openQualifiedProjectReader(root)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.close()
			if strings.Contains(mode, "entity") {
				if out, err := readQualifiedEntityDocument(reader, "customer"); err == nil || out != nil {
					t.Fatal("escaped entity read accepted", mode, err)
				}
				return
			}
			if mode == "invalid directory" {
				if out, err := readQualifiedEnvironments(reader); err == nil || out != nil {
					t.Fatal("invalid directory identity accepted")
				}
				return
			}
			if out, _, err := readQualifiedEnvironment(reader, "dev"); err == nil || out != nil {
				t.Fatal("corrupt environment read accepted", mode, err)
			}
		})
	}
	reader, err := openQualifiedProjectReader(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.close()
	if out, err := reader.readQualifiedBoard("bad/id"); err == nil || out != nil {
		t.Fatal("invalid board identifier accepted")
	}
}
