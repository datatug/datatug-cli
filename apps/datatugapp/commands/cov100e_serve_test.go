package commands

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/server"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/spf13/cobra"
)

// cov100eHome points HOME at a fresh dir (optionally seeding ~/.datatug.yaml)
// and neutralises the policies fallback dir.
func cov100eHome(t *testing.T, config string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(accesspolicies.DirEnv, t.TempDir())
	if config != "" {
		if err := os.WriteFile(filepath.Join(home, dtconfig.ConfigFileName), []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func cov100eProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "datatug-project.json"), []byte(`{"id":"p1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

type cov100eServeCall struct {
	paths map[string]string
	host  string
	port  int
	caps  api.Capabilities
}

func cov100eSeams(t *testing.T, serveErr error) (*cov100eServeCall, *[]string) {
	t.Helper()
	origStart, origOpen := serveStartHTTP, serveOpenURL
	t.Cleanup(func() { serveStartHTTP, serveOpenURL = origStart, origOpen })
	call := &cov100eServeCall{}
	opened := &[]string{}
	serveStartHTTP = func(_ *server.HttpServer, paths map[string]string, host string, port int, _ secureread.Session, caps api.Capabilities) error {
		*call = cov100eServeCall{paths: paths, host: host, port: port, caps: caps}
		return serveErr
	}
	serveOpenURL = func(u string) error {
		*opened = append(*opened, u)
		return errors.New("no browser")
	}
	return call, opened
}

func TestCov100eReadServeFlags_EachFlagMissing(t *testing.T) {
	type def struct {
		name string
		add  func(*cobra.Command)
	}
	defs := []def{
		{serveHostFlag, func(c *cobra.Command) { c.Flags().String(serveHostFlag, "", "") }},
		{servePortFlag, func(c *cobra.Command) { c.Flags().Int(servePortFlag, 0, "") }},
		{serveOpenBrowserFlag, func(c *cobra.Command) { c.Flags().Bool(serveOpenBrowserFlag, false, "") }},
		{serveProjectFlag, func(c *cobra.Command) { c.Flags().String(serveProjectFlag, "", "") }},
		{serveAsFlag, func(c *cobra.Command) { c.Flags().String(serveAsFlag, "", "") }},
		{serveRoleFlag, func(c *cobra.Command) { c.Flags().StringArray(serveRoleFlag, nil, "") }},
		{serveGroupFlag, func(c *cobra.Command) { c.Flags().StringArray(serveGroupFlag, nil, "") }},
		{serveAllowWritesFlag, func(c *cobra.Command) { c.Flags().Bool(serveAllowWritesFlag, false, "") }},
		{serveAllowOpaqueSQLFlag, func(c *cobra.Command) { c.Flags().Bool(serveAllowOpaqueSQLFlag, false, "") }},
		{serveAllowLiveConnsFlag, func(c *cobra.Command) { c.Flags().Bool(serveAllowLiveConnsFlag, false, "") }},
		{serveHTTPOfflineFlag, func(c *cobra.Command) { c.Flags().Bool(serveHTTPOfflineFlag, false, "") }},
		{serveExecTimeoutFlag, func(c *cobra.Command) { c.Flags().Duration(serveExecTimeoutFlag, 0, "") }},
		{serveEvidenceDirFlag, func(c *cobra.Command) { c.Flags().String(serveEvidenceDirFlag, "", "") }},
		{serveSnapshotByteCapFlag, func(c *cobra.Command) { c.Flags().Int(serveSnapshotByteCapFlag, 0, "") }},
		{serveSnapshotRetentionFlag, func(c *cobra.Command) { c.Flags().Duration(serveSnapshotRetentionFlag, 0, "") }},
	}
	for k := range defs {
		cmd := &cobra.Command{Use: "x"}
		for _, d := range defs[:k] {
			d.add(cmd)
		}
		if _, err := readServeFlags(cmd); err == nil {
			t.Errorf("flags before %q defined: want error", defs[k].name)
		}
	}
}

func TestCov100eServe_FlagsError(t *testing.T) {
	if err := serveCommandAction(&cobra.Command{Use: "x"}, nil); err == nil {
		t.Fatal("want flag error")
	}
}

func TestCov100eServe_PolicyWithoutPrincipal(t *testing.T) {
	cov100eHome(t, "")
	cmd := serveCommandArgs()
	_ = cmd.Flags().Set(serveProjectFlag, projectWithPolicies(t))
	err := serveCommandAction(cmd, nil)
	var ec ExitCoder
	if !errors.As(err, &ec) {
		t.Fatalf("err = %v, want ExitCoder", err)
	}
}

func TestCov100eServe_SettingsReadError(t *testing.T) {
	// An empty settings file decodes to io.EOF, which is not os.ErrNotExist.
	cov100eHome(t, "\n")
	cov100eSeams(t, nil)
	if err := serveCommandAction(serveCommandArgs(), nil); err == nil {
		t.Fatal("want settings error")
	}
}

func TestCov100eServe_RuntimeSettingsError(t *testing.T) {
	cov100eHome(t, "server:\n  snapshotRetention: notaduration\n")
	cov100eSeams(t, nil)
	if err := serveCommandAction(serveCommandArgs(), nil); err == nil {
		t.Fatal("want runtime settings error")
	}
}

func TestCov100eServe_MultipleProjects(t *testing.T) {
	cov100eHome(t, "")
	cov100eSeams(t, nil)
	cmd := serveCommandArgs()
	_ = cmd.Flags().Set(serveProjectFlag, "a;b")
	if err := serveCommandAction(cmd, nil); err == nil {
		t.Fatal("want multiple-projects error")
	}
}

func TestCov100eServe_ProjectFileMissing(t *testing.T) {
	cov100eHome(t, "")
	cov100eSeams(t, nil)
	cmd := serveCommandArgs()
	_ = cmd.Flags().Set(serveProjectFlag, t.TempDir())
	if err := serveCommandAction(cmd, nil); err == nil {
		t.Fatal("want project load error")
	}
}

func TestCov100eServe_NoSettingsFile_ConfiguredProjects(t *testing.T) {
	cov100eHome(t, "")
	call, opened := cov100eSeams(t, nil)
	if err := serveCommandAction(serveCommandArgs(), nil); err != nil {
		t.Fatal(err)
	}
	if call.host != "localhost" || call.port != 8989 || len(call.paths) != 0 || len(*opened) != 0 {
		t.Fatalf("call = %+v opened=%v", call, *opened)
	}
}

func TestCov100eServe_AllOptions(t *testing.T) {
	cov100eHome(t, "projects:\n  - id: cfg\n    path: /x\nserver:\n  host: 0.0.0.0\n  port: 9000\n  evidenceDir: /ev\n")
	call, opened := cov100eSeams(t, errors.New("serve stopped"))
	cmd := serveCommandArgs()
	for k, v := range map[string]string{
		serveOpenBrowserFlag: "true", serveAllowWritesFlag: "true", serveAllowOpaqueSQLFlag: "true",
		serveHTTPOfflineFlag: "true", serveExecTimeoutFlag: "5s", serveAsFlag: "alice", serveAllowLiveConnsFlag: "true",
	} {
		if err := cmd.Flags().Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
	err := serveCommandAction(cmd, nil)
	if err == nil || err.Error() != "serve stopped" {
		t.Fatalf("err = %v", err)
	}
	if call.host != "0.0.0.0" || call.port != 9000 || call.paths["cfg"] != "/x" {
		t.Fatalf("call = %+v", call)
	}
	if !call.caps.AllowWrites || !call.caps.AllowOpaqueSQL || !call.caps.AllowLiveConnections || !call.caps.HTTPOffline ||
		call.caps.ExecTimeout != 5*time.Second || call.caps.EvidencePrivateDir != "/ev" {
		t.Fatalf("caps = %+v", call.caps)
	}
	if len(*opened) != 1 {
		t.Fatalf("opened = %v", *opened)
	}
}

func TestCov100eServe_PolicySetWithProject(t *testing.T) {
	cov100eHome(t, "")
	call, _ := cov100eSeams(t, nil)
	dir := projectWithPolicies(t)
	if err := os.WriteFile(filepath.Join(dir, "datatug-project.json"), []byte(`{"id":"p1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := serveCommandArgs()
	_ = cmd.Flags().Set(serveProjectFlag, dir)
	_ = cmd.Flags().Set(serveAsFlag, "alice")
	if err := serveCommandAction(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if call.paths["p1"] != dir {
		t.Fatalf("paths = %v", call.paths)
	}
}

func TestCov100eServe_SingleProjectUnrestricted(t *testing.T) {
	cov100eHome(t, "")
	call, _ := cov100eSeams(t, nil)
	dir := cov100eProject(t)
	cmd := serveCommandArgs()
	_ = cmd.Flags().Set(serveProjectFlag, dir)
	_ = cmd.Flags().Set(serveHostFlag, "localhost")
	if err := serveCommandAction(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if call.paths["p1"] != dir {
		t.Fatalf("paths = %v", call.paths)
	}
}

func TestCov100eRuntimeSettings(t *testing.T) {
	home := cov100eHome(t, "")
	s, err := loadServeRuntimeSettings()
	if err != nil || s.Server.EvidenceDir != "" {
		t.Fatalf("missing file: %+v %v", s, err)
	}

	cfg := filepath.Join(home, dtconfig.ConfigFileName)
	if err := os.WriteFile(cfg, []byte("server:\n  evidenceDir: /e\n  snapshotByteCap: 7\n  snapshotRetention: 1h\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err = loadServeRuntimeSettings()
	if err != nil || s.Server.EvidenceDir != "/e" || s.Server.SnapshotByteCap != 7 || s.Server.SnapshotRetention != time.Hour {
		t.Fatalf("parsed: %+v %v", s, err)
	}

	if err := os.WriteFile(cfg, []byte("server: [unclosed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = loadServeRuntimeSettings(); err == nil {
		t.Fatal("want decode error")
	}

	// A directory in place of the file: ReadFile fails with a non-NotExist error.
	if err := os.Remove(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err = loadServeRuntimeSettings(); err == nil {
		t.Fatal("want read error")
	}
}

func TestCov100eFirstNonZeroHelpers(t *testing.T) {
	if firstNonEmpty("", "a", "b") != "a" || firstNonEmpty("", "") != "" || firstNonEmpty("x") != "x" {
		t.Fatal("firstNonEmpty")
	}
	if firstNonZero(0, 2, 3) != 2 || firstNonZero(0, 0) != 0 || firstNonZero(1) != 1 {
		t.Fatal("firstNonZero")
	}
	if firstNonZeroDuration(0, time.Second) != time.Second || firstNonZeroDuration(0) != 0 || firstNonZeroDuration(time.Minute, time.Second) != time.Minute {
		t.Fatal("firstNonZeroDuration")
	}
}

// The live connection of the databases route is a capability that is off unless the flag is
// given, and the flag says in its help what it allows.
func TestServe_AllowLiveConnectionsIsOffByDefault(t *testing.T) {
	cov100eHome(t, "")
	call, _ := cov100eSeams(t, nil)
	if err := serveCommandAction(serveCommandArgs(), nil); err != nil {
		t.Fatal(err)
	}
	if call.caps.AllowLiveConnections {
		t.Fatal("live connections are allowed with no flag")
	}

	flag := serveCommandArgs().Flags().Lookup(serveAllowLiveConnsFlag)
	if flag == nil || flag.DefValue != "false" {
		t.Fatalf("flag = %+v, want one that defaults to false", flag)
	}
	for _, word := range []string{"connect", "db server", "dbserver-databases", "403"} {
		if !strings.Contains(flag.Usage, word) {
			t.Errorf("the help of --%s does not say %q: %s", serveAllowLiveConnsFlag, word, flag.Usage)
		}
	}
}
