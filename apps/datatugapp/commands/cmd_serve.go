package commands

import (
	"errors"
	"fmt"
	"strings"

	"github.com/datatug/datatug-cli/pkg/datatug-core/dtconfig"
	"github.com/datatug/datatug-cli/pkg/datatug-core/storage/filestore"
	"github.com/datatug/datatug-cli/pkg/server"
	"github.com/pkg/browser"
	"github.com/spf13/cobra"
)

// ServeCommand executes serve consoleCommand
//var ServeCommand *flags.Command

func serveCommandAction(_ *cobra.Command, _ []string) error {
	v := &serveCommand{}
	var config dtconfig.Settings
	config, err := dtconfig.GetSettings()
	if err != nil {
		return err
	}

	pathsByID := make(map[string]string)
	if v.ProjectDir != "" {
		if strings.Contains(v.ProjectDir, ";") {
			return errors.New("serving multiple specified throw a consoleCommand line argument is not supported yet")
		}
		projectFile, err := filestore.LoadProjectFile(v.ProjectDir)
		if err != nil {
			return fmt.Errorf("failed to load project file: %w", err)
		}
		pathsByID[projectFile.ID] = v.ProjectDir
	} else {
		pathsByID = getProjPathsByID(config)
	}

	serverConfig := config.Server

	if v.Host == "" {
		v.Host = serverConfig.Host
	}
	if v.Port == 0 {
		v.Port = serverConfig.Port
	}
	if v.ClientURL == "" {
		if v.Local {
			//goland:noinspection HttpUrlsUsage
			v.ClientURL = fmt.Sprintf("http://%s:%d", v.Host, v.Port) // consider choosing some unique default port
		} else {
			v.ClientURL = fmt.Sprintf("https://datatug.app/pwa/repo/%s:%d", v.Host, v.Port)
		}
	}

	var agent string
	if v.Port == 0 || v.Port == 80 {
		agent = v.Host
	} else {
		agent = fmt.Sprintf("%v:%v", v.Host, v.Port)
	}

	url := v.ClientURL + "/agent/" + agent

	if err := browser.OpenURL(url); err != nil {
		_, _ = fmt.Printf("failed to open browser with URl=%v: %v", url, err)
	}
	httpServer := server.NewHttpServer()
	// TODO: implement graceful shutdown
	return httpServer.ServeHTTP(pathsByID, v.Host, v.Port)
}

func serveCommandArgs() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Serves HTTP server to provide API for UI",
		Long:  "Serves HTTP server to provide API for UI. Default port is 8989",
		RunE:  serveCommandAction,
	}
}

// serveCommand defines parameters for serve consoleCommand. No flags are
// registered on the `serve` cobra.Command (matching the pre-migration
// urfave/cli/v3 wiring, which likewise never attached these fields to any
// cli.Flag), so every field below always holds its zero value at runtime;
// serveCommandAction falls back to config-file/default values instead.
type serveCommand struct {
	projectBaseCommand
	Host      string
	Port      int
	Local     bool
	ClientURL string
}
