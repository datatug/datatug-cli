package commands

import (
	"reflect"
	"testing"

	"github.com/datatug/datatug-cli/pkg/datatug-core/dtconfig"
)

// TestResolveServeAddr covers the bug fixed in cmd_serve.go: serveCommandAction
// used to dereference config.Server unconditionally (`serverConfig.Host` /
// `serverConfig.Port`), which panicked with a nil pointer whenever
// `datatug serve` ran without a `server:` section in ~/.datatug.yaml -
// including when there is no config file at all.
func TestResolveServeAddr(t *testing.T) {
	t.Run("nil server config defaults to localhost:8989", func(t *testing.T) {
		host, port := resolveServeAddr("", 0, dtconfig.Settings{})
		if host != "localhost" || port != 8989 {
			t.Fatalf("got %s:%d, want localhost:8989", host, port)
		}
	})

	t.Run("flags win over config", func(t *testing.T) {
		config := dtconfig.Settings{Server: &dtconfig.ServerConfig{
			UrlConfig: dtconfig.UrlConfig{Host: "example.com", Port: 1234},
		}}
		host, port := resolveServeAddr("0.0.0.0", 9000, config)
		if host != "0.0.0.0" || port != 9000 {
			t.Fatalf("got %s:%d, want 0.0.0.0:9000", host, port)
		}
	})

	t.Run("config fills the gap left by unset flags", func(t *testing.T) {
		config := dtconfig.Settings{Server: &dtconfig.ServerConfig{
			UrlConfig: dtconfig.UrlConfig{Host: "example.com", Port: 1234},
		}}
		host, port := resolveServeAddr("", 0, config)
		if host != "example.com" || port != 1234 {
			t.Fatalf("got %s:%d, want example.com:1234", host, port)
		}
	})

	t.Run("config present but empty still falls back to defaults", func(t *testing.T) {
		host, port := resolveServeAddr("", 0, dtconfig.Settings{Server: &dtconfig.ServerConfig{}})
		if host != "localhost" || port != 8989 {
			t.Fatalf("got %s:%d, want localhost:8989", host, port)
		}
	})
}

func TestReadServeFlags(t *testing.T) {
	cmd := serveCommandArgs()
	args := []string{
		"--host=0.0.0.0",
		"--port=9000",
		"--project=/tmp/demo-project",
		"--config=/tmp/datatug-e2e.yaml",
		"--as=admin",
		"--role=support",
		"--role=readonly",
		"--group=team-a",
	}
	if err := cmd.Flags().Parse(args); err != nil {
		t.Fatalf("Parse(%v): %v", args, err)
	}

	flags, err := readServeFlags(cmd)
	if err != nil {
		t.Fatalf("readServeFlags: %v", err)
	}

	want := serveFlags{
		host:       "0.0.0.0",
		port:       9000,
		projectDir: "/tmp/demo-project",
		configFile: "/tmp/datatug-e2e.yaml",
		as:         "admin",
		roles:      []string{"support", "readonly"},
		groups:     []string{"team-a"},
	}
	if !reflect.DeepEqual(flags, want) {
		t.Fatalf("got %+v, want %+v", flags, want)
	}
}

func TestReadServeFlagsDefaults(t *testing.T) {
	cmd := serveCommandArgs()
	if err := cmd.Flags().Parse(nil); err != nil {
		t.Fatalf("Parse(nil): %v", err)
	}

	flags, err := readServeFlags(cmd)
	if err != nil {
		t.Fatalf("readServeFlags: %v", err)
	}
	if flags.host != "" || flags.port != 0 || flags.projectDir != "" || flags.configFile != "" {
		t.Fatalf("expected zero-value flags, got %+v", flags)
	}
}
