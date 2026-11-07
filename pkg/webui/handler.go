package webui

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
	"syscall"
	"time"

	nethtml "golang.org/x/net/html"
)

// RuntimeConfig is injected into index.html as <meta name="datatug-runtime">.
// It tells the frontend it runs in local mode and where the agent is; it
// grants nothing — the agent decides every permission per request.
type RuntimeConfig struct {
	Mode       string `json:"mode"`
	AgentPath  string `json:"agentPath"`
	LocalAPI   string `json:"localApi"`
	CLIVersion string `json:"cliVersion"`
	Version    string `json:"webUiVersion"`
	Telemetry  bool   `json:"telemetry"`
}

// HandlerOptions configure the static web UI handler.
type HandlerOptions struct {
	// Dir is an installed version's directory (holding browser/).
	Dir     string
	Runtime RuntimeConfig
	// ConnectSrc are extra origins the page may reach (CSP connect-src), from --ui-connect-src.
	ConnectSrc []string
}

// Handler serves one installed web UI.
type Handler struct {
	root    *os.Root
	index   []byte
	csp     string
	modTime time.Time
	// InlineHandlers counts inline event-handler attributes the CSP blocks.
	InlineHandlers int
}

// AgentPathPrefix is where the agent API lives; the handler never answers under it.
const AgentPathPrefix = "/datatug/"

// cssMediaSwap is Angular's critical-CSS loader, which a strict CSP blocks:
// it becomes a plain stylesheet link.
var cssMediaSwap = regexp.MustCompile(`media="print" onload="this\.media='all'"`)

// hashedName matches Angular's content-hashed output names, e.g. main-ABCD1234.js.
var hashedName = regexp.MustCompile(`-[A-Z0-9]{8}\.[a-z0-9]+$`)

// NewHandler reads the version's index.html, injects the runtime config and builds the CSP.
func NewHandler(opts HandlerOptions) (*Handler, error) {
	root, err := os.OpenRoot(path.Join(opts.Dir, BrowserDir))
	if err != nil {
		return nil, err
	}
	raw, err := root.ReadFile("index.html")
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	index, err := injectRuntime(raw, opts.Runtime)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	index = cssMediaSwap.ReplaceAll(index, []byte(`media="all"`))
	hashes, handlers := inlineScriptHashes(index)
	for _, origin := range opts.ConnectSrc {
		if err := checkConnectOrigin(origin); err != nil {
			_ = root.Close()
			return nil, err
		}
	}
	return &Handler{
		root:           root,
		index:          index,
		csp:            contentSecurityPolicy(hashes, opts.ConnectSrc),
		modTime:        time.Now(),
		InlineHandlers: handlers,
	}, nil
}

// Close releases the directory handle.
func (h *Handler) Close() error { return h.root.Close() }

// CSP is the Content-Security-Policy every response carries.
func (h *Handler) CSP() string { return h.csp }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	h.securityHeaders(w.Header())
	p := path.Clean("/" + r.URL.Path)
	if strings.HasPrefix(p+"/", AgentPathPrefix) {
		http.NotFound(w, r)
		return
	}
	if p == "/" || p == "/index.html" {
		h.serveIndex(w, r)
		return
	}
	rel := strings.TrimPrefix(p, "/")
	f, err := h.root.Open(rel)
	if err == nil {
		defer func() { _ = f.Close() }()
		info, statErr := f.Stat()
		if statErr == nil && info.Mode().IsRegular() {
			if hashedName.MatchString(rel) {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			// Not the *os.File itself: net/http would send it with sendfile(2), which sandboxed
			// hosts refuse (the body arrives cut at 512 bytes); local serving does not need it.
			http.ServeContent(w, r, info.Name(), info.ModTime(), struct{ io.ReadSeeker }{f})
			return
		}
	} else if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) && !isRootEscape(err) {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if isPageLoad(r) {
		h.serveIndex(w, r) // a client-side route; app routes contain dots, so no extension rule
		return
	}
	http.NotFound(w, r)
}

// isPageLoad reports whether the browser is loading a document, not fetching an asset.
func isPageLoad(r *http.Request) bool {
	if dest := r.Header.Get("Sec-Fetch-Dest"); dest != "" {
		return dest == "document"
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

func isRootEscape(err error) bool {
	return err != nil && strings.Contains(err.Error(), "path escapes from parent")
}

func (h *Handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(w, r, "index.html", h.modTime, bytes.NewReader(h.index))
}

func (h *Handler) securityHeaders(hd http.Header) {
	hd.Set("Content-Security-Policy", h.csp)
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Referrer-Policy", "no-referrer")
	hd.Set("Cross-Origin-Opener-Policy", "same-origin")
	hd.Set("X-Frame-Options", "DENY")
}

func contentSecurityPolicy(scriptHashes, connect []string) string {
	// Ajv compiles project-file schemas with new Function; follow-up F9 precompiles them (ajv
	// standalone) so this can go. Inline scripts still need their hash.
	script := append([]string{"'self'", "'wasm-unsafe-eval'", "'unsafe-eval'"}, scriptHashes...)
	connectSrc := append([]string{"'self'"}, connect...)
	return strings.Join([]string{
		"default-src 'self'",
		"script-src " + strings.Join(script, " "),
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob:",
		"font-src 'self' data:",
		"connect-src " + strings.Join(connectSrc, " "),
		"worker-src 'self' blob:",
		"frame-src 'self'",
		"frame-ancestors 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"object-src 'none'",
	}, "; ")
}

var connectOrigin = regexp.MustCompile(`^https://[a-z0-9.-]+(:[0-9]{1,5})?$`)

// checkConnectOrigin accepts only an exact https origin: no wildcards, paths or other schemes.
func checkConnectOrigin(origin string) error {
	if !connectOrigin.MatchString(origin) {
		return fmt.Errorf("--ui-connect-src %q is not an exact https origin like https://api.example.com", origin)
	}
	return nil
}

// injectRuntime puts the runtime config as the first element of <head>.
func injectRuntime(index []byte, rc RuntimeConfig) ([]byte, error) {
	data, _ := json.Marshal(rc) // a RuntimeConfig of strings and a bool always marshals
	meta := []byte(`<meta name="datatug-runtime" content="` + html.EscapeString(string(data)) + `">`)
	// The <head> start tag as the HTML tokenizer reads it: never <header>, a comment or script text.
	z := nethtml.NewTokenizer(bytes.NewReader(index))
	cut := -1
	for offset := 0; cut < 0; {
		tt := z.Next()
		if tt == nethtml.ErrorToken {
			return nil, errors.New("index.html has no <head>")
		}
		offset += len(z.Raw())
		if name, _ := z.TagName(); tt == nethtml.StartTagToken && string(name) == "head" {
			cut = offset
		}
	}
	out := make([]byte, 0, len(index)+len(meta))
	out = append(out, index[:cut]...)
	out = append(out, meta...)
	return append(out, index[cut:]...), nil
}

// inlineScriptHashes returns the CSP hash source of every inline <script> and
// how many inline event-handler attributes the page has (the CSP blocks those).
func inlineScriptHashes(index []byte) (hashes []string, handlers int) {
	z := nethtml.NewTokenizer(bytes.NewReader(index))
	inScript := false
	for {
		tt := z.Next()
		switch tt {
		case nethtml.ErrorToken: // io.EOF, or a malformed tail the browser will not run either
			return hashes, handlers
		case nethtml.StartTagToken, nethtml.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			hasSrc := false
			for hasAttr {
				var key []byte
				key, _, hasAttr = z.TagAttr()
				k := strings.ToLower(string(key))
				if k == "src" {
					hasSrc = true
				}
				if strings.HasPrefix(k, "on") {
					handlers++
				}
			}
			inScript = string(name) == "script" && !hasSrc && tt == nethtml.StartTagToken
		case nethtml.TextToken:
			if inScript {
				sum := sha256.Sum256(z.Raw())
				hashes = append(hashes, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
			}
			inScript = false
		default:
			inScript = false
		}
	}
}
