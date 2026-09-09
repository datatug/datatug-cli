package commands

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"

	"github.com/datatug/datatug-cli/pkg/datatug-core/dtconfig"
	"github.com/datatug/datatug-cli/pkg/datatug-core/storage/filestore"
	"github.com/datatug/datatug-cli/pkg/server"
	"github.com/pkg/browser"
	"github.com/spf13/cobra"
)

const (
	serveHostFlag    = "host"
	servePortFlag    = "port"
	serveProjectFlag = "project"
	serveConfigFlag  = "config"
	serveAsFlag      = "as"
	serveRoleFlag    = "role"
	serveGroupFlag   = "group"
)

// ServeCommand executes serve consoleCommand
//var ServeCommand *flags.Command

// serveFlags holds the values parsed off the `serve` cobra.Command's flags.
// --as/--role/--group are reserved: parsed and logged here, but not yet
// enforced. Task 6 (plan: 2026-09-09-phase-1-core-investigation-loop.md)
// wires them into access policies.
type serveFlags struct {
	host       string
	port       int
	projectDir string
	configFile string
	as         string
	roles      []string
	groups     []string
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
	if f.projectDir, err = flags.GetString(serveProjectFlag); err != nil {
		return f, err
	}
	if f.configFile, err = flags.GetString(serveConfigFlag); err != nil {
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

func serveCommandAction(cmd *cobra.Command, _ []string) error {
	flags, err := readServeFlags(cmd)
	if err != nil {
		return err
	}
	if flags.as != "" || len(flags.roles) > 0 || len(flags.groups) > 0 {
		log.Printf("serve: --as=%q --role=%v --group=%v parsed but not yet enforced (plan task 6 wires access policies)", flags.as, flags.roles, flags.groups)
	}

	var config dtconfig.Settings
	if flags.configFile != "" {
		config, err = dtconfig.GetSettingsFromFile(flags.configFile)
	} else {
		config, err = dtconfig.GetSettings()
	}
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
	ovdbTargets, err := config.ResolveOpenVaultDBTargets()
	if err != nil {
		return err
	}
	var agentSessionToken string
	if len(ovdbTargets) != 0 {
		agentSessionToken, err = server.NewAgentSessionToken()
		if err != nil {
			return err
		}
	}

	// The pre-migration `--local`/`--client-url` flags were never wired to
	// cobra (same dead-flag issue as --host/--port before this fix), so this
	// always resolved to the datatug.app URL; preserved as-is, out of scope
	// for this fix.
	clientURL := fmt.Sprintf("%s/pwa/repo/%s:%d", config.WebUIOrigin(), host, port)
	var agent string
	if port == 0 || port == 80 {
		agent = host
	} else {
		agent = fmt.Sprintf("%v:%v", host, port)
	}

	launchURL := clientURL + "/agent/" + agent
	if agentSessionToken != "" {
		// A fragment is available to the launched web app but is not sent to
		// the hosted web server, proxy logs, or the OpenVaultDB upstream.
		launchURL += "#agentToken=" + url.QueryEscape(agentSessionToken)
	}

	if err := browser.OpenURL(launchURL); err != nil {
		if agentSessionToken != "" {
			_, _ = fmt.Println("failed to open browser for the protected DataTug session")
		} else {
			_, _ = fmt.Printf("failed to open browser with URL: %v", err)
		}
	}
	httpServer := server.NewHttpServer()
	if len(ovdbTargets) != 0 {
		httpServer = server.NewHttpServerWithOpenVaultDB(server.OpenVaultDBProxyOptions{
			Origin: config.WebUIOrigin(), SessionToken: agentSessionToken, Targets: ovdbTargets,
		})
	}
	// TODO: implement graceful shutdown
	return httpServer.ServeHTTP(pathsByID, host, port)
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
	flags.String(serveConfigFlag, "", "Path to a DataTug settings file for this server")
	flags.String(serveAsFlag, "", "Principal user ID to serve as (reserved: parsed but not yet enforced)")
	flags.StringArray(serveRoleFlag, nil, "Principal role, repeatable (reserved: parsed but not yet enforced)")
	flags.StringArray(serveGroupFlag, nil, "Principal group, repeatable (reserved: parsed but not yet enforced)")
	return cmd
}
