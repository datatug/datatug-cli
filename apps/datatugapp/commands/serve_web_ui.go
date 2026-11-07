package commands

// specscore: feature/cli/serve

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"

	"github.com/datatug/datatug-cli/pkg/webui"
	"github.com/spf13/cobra"
	"github.com/strongo/buildinfo"
)

const (
	serveWebUIFlag        = "web-ui"
	serveUIHostFlag       = "ui-host"
	serveUIConnectSrcFlag = "ui-connect-src"
	// defaultUIHost resolves to loopback in Chromium, Firefox and on macOS; the
	// 127.0.0.1 link is printed beside it for everything else.
	defaultUIHost = "datatug-app.localhost"
	webUIAuto     = "auto"
)

func addServeWebUIFlags(cmd *cobra.Command) {
	flags := cmd.Flags()
	flags.String(serveWebUIFlag, "", "Also serve the installed DataTug web UI from this server (same origin, works offline); "+
		"--web-ui=<version> serves that version. Install one with `datatug web-ui install`")
	flags.Lookup(serveWebUIFlag).NoOptDefVal = webUIAuto
	flags.String(serveUIHostFlag, "", "Host name of the served web UI: localhost, 127.0.0.1 or a *.localhost name (default "+defaultUIHost+", or the server.uiHost setting)")
	flags.StringArray(serveUIConnectSrcFlag, nil, "Exact https origin the served web UI may also reach (CSP connect-src), repeatable; nothing else is reachable from the page")
}

// servedWebUI is what `serve --web-ui` serves.
type servedWebUI struct {
	handler *webui.Handler
	host    string
	entry   webui.Entry
	connect []string
}

var uiHostLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// validUIHost accepts only names that resolve to this machine: localhost,
// 127.0.0.1, or a *.localhost name. A real DNS name would let a rebinding page
// pass the Host check of the token-free static routes.
func validUIHost(host string) bool {
	if host == "localhost" || host == "127.0.0.1" {
		return true
	}
	name, ok := strings.CutSuffix(host, ".localhost")
	if !ok || name == "" {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if !uiHostLabel.MatchString(label) {
			return false
		}
	}
	return true
}

// loopbackBind reports whether the server binds only this machine.
func loopbackBind(host string) bool {
	if host == "localhost" {
		return true
	}
	addr, err := netip.ParseAddr(strings.Trim(host, "[]"))
	return err == nil && addr.IsLoopback()
}

// prepareServeWebUI selects, verifies and opens the web UI to serve, or returns nil without --web-ui.
func prepareServeWebUI(cmd *cobra.Command, bindHost, settingsUIHost string) (*servedWebUI, error) {
	flags := cmd.Flags()
	if !flags.Changed(serveWebUIFlag) {
		return nil, nil
	}
	requested, _ := flags.GetString(serveWebUIFlag)
	if requested == webUIAuto {
		requested = ""
	}
	uiHost, _ := flags.GetString(serveUIHostFlag)
	uiHost = strings.ToLower(firstNonEmpty(uiHost, settingsUIHost, defaultUIHost))
	if !validUIHost(uiHost) {
		return nil, Exit(fmt.Sprintf("--%s %q: use localhost, 127.0.0.1 or a name ending in .localhost (serving the web UI beyond this machine is not supported)", serveUIHostFlag, uiHost), exitCodeUsage)
	}
	if !loopbackBind(bindHost) {
		return nil, Exit(fmt.Sprintf("serve --web-ui binds only this machine; --host %q is not a loopback address (shared hosting is a separately licensed deployment)", bindHost), exitCodeUsage)
	}
	connect, _ := flags.GetStringArray(serveUIConnectSrcFlag)
	entry, err := webUIStore().Select(requested)
	if err != nil {
		return nil, errWebUINotInstalled(err)
	}
	handler, err := webui.NewHandler(webui.HandlerOptions{
		Dir: entry.Dir,
		Runtime: webui.RuntimeConfig{
			Mode:       "local",
			AgentPath:  "/datatug",
			LocalAPI:   webui.LocalAPIVersion,
			CLIVersion: buildinfo.Get("datatug").Version,
			Version:    entry.Version,
		},
		ConnectSrc: connect,
	})
	if err != nil {
		return nil, Exit("serve --web-ui: "+err.Error(), exitCodeUsage)
	}
	if err := webUIStore().MarkActive(entry.Version); err != nil {
		_ = handler.Close()
		return nil, err
	}
	return &servedWebUI{handler: handler, host: uiHost, entry: entry, connect: connect}, nil
}

// links returns the web UI link on the UI host and the 127.0.0.1 fallback link.
func (w *servedWebUI) links(port int) (primary, fallback string) {
	link := func(host string) string {
		hostPort := fmt.Sprintf("%s:%d", host, port)
		return "http://" + hostPort + "/store/http-" + hostPort
	}
	return link(w.host), link("127.0.0.1")
}

func (w *servedWebUI) banner() string {
	b := fmt.Sprintf("Serving web UI %s from %s (local mode: no telemetry; the page can reach only this agent", w.entry.Version, w.entry.Dir)
	if len(w.connect) > 0 {
		b += " and " + strings.Join(w.connect, ", ")
	}
	b += ")"
	if w.handler.InlineHandlers > 0 {
		b += fmt.Sprintf("\nwarning: the web UI has %d inline event handler(s) that its Content-Security-Policy blocks", w.handler.InlineHandlers)
	}
	return b
}
