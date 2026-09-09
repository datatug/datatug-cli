package commands

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/datatug-core/dtconfig"
	"github.com/datatug/datatug-cli/pkg/datatug-core/storage/filestore"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/server"
	"github.com/pkg/browser"
	"github.com/spf13/cobra"
)

const (
	serveHostFlag    = "host"
	servePortFlag    = "port"
	serveProjectFlag = "project"
	serveAsFlag      = "as"
	serveRoleFlag    = "role"
	serveGroupFlag   = "group"
)

// ServeCommand executes serve consoleCommand
//var ServeCommand *flags.Command

const serveOpenBrowserFlag = "open-browser"

// serveFlags holds the values parsed off the `serve` cobra.Command's flags.
// --as/--role/--group fix the principal every policy-enforced read in this
// process runs as (REQ:principal-selection) — see resolveServeSession.
type serveFlags struct {
	openBrowser bool
	host        string
	port        int
	projectDir  string
	as          string
	roles       []string
	groups      []string
}

func readServeFlags(cmd *cobra.Command) (serveFlags, error) {
	flags := cmd.Flags()
	var f serveFlags
	var err error
	if f.host, err = flags.GetString(serveHostFlag); err != nil {
		return f, err
	}
	if f.port, err = flags.GetInt(servePortFlag); err != nil {
		return f, err
	}
	if f.openBrowser, err = flags.GetBool(serveOpenBrowserFlag); err != nil {
		return f, err
	}
	if f.projectDir, err = flags.GetString(serveProjectFlag); err != nil {
		return f, err
	}
	if f.as, err = flags.GetString(serveAsFlag); err != nil {
		return f, err
	}
	if f.roles, err = flags.GetStringArray(serveRoleFlag); err != nil {
		return f, err
	}
	if f.groups, err = flags.GetStringArray(serveGroupFlag); err != nil {
		return f, err
	}
	return f, nil
}

// resolveServeAddr picks the host/port `datatug serve` binds to: explicit
// flags win, then the optional `server:` section of ~/.datatug.yaml, then
// the localhost:8989 default. config.Server is nil whenever there is no
// config file (a fresh `datatug serve` with no prior `datatug init`/settings)
// or the file has no `server:` section - both must be handled without a nil
// pointer dereference.
func resolveServeAddr(host string, port int, config dtconfig.Settings) (string, int) {
	if config.Server != nil {
		if host == "" {
			host = config.Server.Host
		}
		if port == 0 {
			port = config.Server.Port
		}
	}
	if host == "" {
		host = "localhost"
	}
	if port == 0 {
		port = 8989
	}
	return host, port
}

// resolveServeSession builds the fixed secureread.Session `datatug serve`
// runs its whole process life under (REQ:principal-selection,
// REQ:server-acl-all-reads). Policies come from "<project>/policies/" when
// projectDir is set and that folder exists, else the usual accesspolicies
// default (--policies-dir/$DATATUG_POLICIES_DIR/~/.datatug/policies — same
// resolution `datatug query run` uses).
//
// Per the hub Feature's resolved Open Question ("Should the serving
// principal default to admin when --as is omitted, or should serve refuse
// to start without a principal when a policy set exists? Recommendation:
// default admin only when no policy set exists; otherwise require --as."):
// a policy set with no --as/--role/--group refuses to start; no policy set
// at all runs Unrestricted, defaulting the reported principal to "admin"
// unless the caller named one explicitly.
func resolveServeSession(projectDir string, flags serveFlags) (secureread.Session, error) {
	policiesDir := ""
	if projectDir != "" {
		candidate := filepath.Join(projectDir, "policies")
		if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
			policiesDir = candidate
		}
	}
	hasPrincipal := flags.as != "" || len(flags.roles) > 0 || len(flags.groups) > 0

	_, err := accesspolicies.Load(accesspolicies.LoadOptions{Dir: policiesDir})
	switch {
	case err == nil:
		if !hasPrincipal {
			return secureread.Session{}, errors.New("serve: a policy set was found but no principal was named; pass --as, --role or --group (or remove the policies to run unrestricted)")
		}
		return secureread.NewSession(secureread.SessionOptions{
			As: flags.as, Roles: flags.roles, Groups: flags.groups, PoliciesDir: policiesDir,
		})
	case errors.Is(err, accesspolicies.ErrNoPolicies):
		as := flags.as
		if !hasPrincipal {
			as = "admin"
		}
		return secureread.NewSession(secureread.SessionOptions{
			As: as, Roles: flags.roles, Groups: flags.groups, NoPolicies: true,
		})
	default:
		return secureread.Session{}, fmt.Errorf("serve: load access policies: %w", err)
	}
}

func serveCommandAction(cmd *cobra.Command, _ []string) error {
	flags, err := readServeFlags(cmd)
	if err != nil {
		return err
	}

	session, err := resolveServeSession(flags.projectDir, flags)
	if err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}
	if session.Unrestricted {
		log.Printf("serve: running without access policy enforcement (principal=%v)", flags.as)
	} else {
		log.Printf("serve: %d access policy document(s) loaded; serving as principal %+v", len(session.Policies), session.Principal)
	}

	config, err := dtconfig.GetSettings()
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to get DataTug settings: %w", err)
		}
		config = dtconfig.Settings{}
	}

	pathsByID := make(map[string]string)
	if flags.projectDir != "" {
		if strings.Contains(flags.projectDir, ";") {
			return errors.New("serving multiple specified throw a consoleCommand line argument is not supported yet")
		}
		projectFile, err := filestore.LoadProjectFile(flags.projectDir)
		if err != nil {
			return fmt.Errorf("failed to load project file: %w", err)
		}
		pathsByID[projectFile.ID] = flags.projectDir
	} else {
		pathsByID = getProjPathsByID(config)
	}

	host, port := resolveServeAddr(flags.host, flags.port, config)

	// The web UI addresses a local agent as a store id of the form host:port
	// under /store/<id> (datatug-apps routes); the old /pwa/repo/... route no
	// longer exists and crashed the hosted app with NG04002.
	var agent string
	if port == 0 || port == 80 {
		agent = host
	} else {
		agent = fmt.Sprintf("%v:%v", host, port)
	}
	url := "https://datatug.app/store/" + agent

	// Founder ruling 2026-09-09: serve never opens a browser by default; it
	// prints the link and opens a browser only with --open-browser.
	fmt.Printf("DataTug web UI for this agent: %s\n", url)
	if flags.openBrowser {
		if err := browser.OpenURL(url); err != nil {
			_, _ = fmt.Printf("failed to open browser with URL=%v: %v\n", url, err)
		}
	}
	httpServer := server.NewHttpServer()
	// TODO: implement graceful shutdown
	return httpServer.ServeHTTP(pathsByID, host, port, session)
}

func serveCommandArgs() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serves HTTP server to provide API for UI",
		Long:  "Serves HTTP server to provide API for UI. Default port is 8989",
		RunE:  serveCommandAction,
	}
	flags := cmd.Flags()
	flags.String(serveHostFlag, "", "Host to bind the agent HTTP server to (default: localhost, or the server.host setting)")
	flags.Int(servePortFlag, 0, "Port to bind the agent HTTP server to (default: 8989, or the server.port setting)")
	flags.String(serveProjectFlag, "", "Path to a single DataTug project directory to serve")
	flags.Bool(serveOpenBrowserFlag, false, "Open the web UI in a browser (off by default; the URL is always printed)")
	flags.String(serveAsFlag, "", "Principal user ID to serve as; required when the project has a policies/ set (or $HOME/"+accesspolicies.DefaultDir+")")
	flags.StringArray(serveRoleFlag, nil, "Principal role, repeatable")
	flags.StringArray(serveGroupFlag, nil, "Principal group, repeatable")
	return cmd
}
