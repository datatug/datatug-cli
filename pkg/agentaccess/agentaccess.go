// Package agentaccess is the shared-token access model of a local DataTug agent (`datatug serve`,
// and the daemon that runs it): loopback plus a token. The agent creates a random secret when it
// starts and keeps it in the state directory (mode 0600); everything the browser and the CLI need is
// derived from it:
//
//   - the session token, which every route but the preflight and the exchange requires;
//   - a one-time code, which `datatug daemon url` and `datatug serve` mint locally (no HTTP request,
//     no daemon needed) and put in the fragment of a link, and which the web UI exchanges once for
//     the session token.
//
// A code is `v1.<expiry unix seconds>.<nonce>.<signature>`: signed with the secret, valid for
// CodeTTL, accepted once. The secret, the token and the codes are never logged.
package agentaccess

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/datatug/datatug-cli/pkg/dtroot"
)

const (
	// CodeTTL is how long a minted code can be exchanged.
	CodeTTL = 10 * time.Minute
	// DirEnv overrides the state directory (default ~/datatug/daemon); it is the daemon's own.
	DirEnv = "DATATUG_DAEMON_DIR"
	// ExchangePath is the route (under /datatug) that trades a code for the session token.
	ExchangePath = "/datatug/session/exchange"

	secretBytes = 32
	nonceBytes  = 16
	codeVersion = "v1"
)

// randRead is the source of randomness; tests replace it.
var randRead = rand.Read

// ErrBadCode is the one answer for a code that is malformed, tampered, expired or used.
var ErrBadCode = errors.New("the link code is not valid")

// StateDir is the directory holding an agent's access key (and the daemon's record).
func StateDir() string {
	if dir := os.Getenv(DirEnv); dir != "" {
		return dir
	}
	return filepath.Join(dtroot.Path(), "daemon")
}

// KeyPath is the access key file of the agent that listens on port.
func KeyPath(dir string, port int) string {
	return filepath.Join(dir, fmt.Sprintf("access-%d.key", port))
}

// NewSecret returns a fresh random secret.
func NewSecret() ([]byte, error) {
	secret := make([]byte, secretBytes)
	if _, err := randRead(secret); err != nil {
		return nil, err
	}
	return secret, nil
}

// WriteKey stores secret for the agent on port, readable by the current user only. The state
// directory must not be writable by anyone else (another user could plant a link or a file), and
// the file is created fresh, never through an existing file or link.
func WriteKey(dir string, port int, secret []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !privateMode(info) {
		if goos == "windows" {
			return errors.New("the state directory is not private to this user")
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
		if info, err = os.Stat(dir); err != nil || !privateMode(info) {
			return errors.New("the state directory is not private to this user")
		}
	}
	path := KeyPath(dir, port)
	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	// A short or failed write leaves a file that ReadKey refuses as not valid.
	_, _ = f.WriteString(hex.EncodeToString(secret) + "\n")
	_ = f.Close()
	return os.Rename(tmp, path)
}

// goos is the platform; tests replace it.
var goos = runtime.GOOS

// privateMode reports whether info has no group or other write bit for a directory, and no group
// or other bit at all for a file. Modes mean nothing on Windows, where the ACL of the user's
// directory applies.
func privateMode(info os.FileInfo) bool {
	if goos == "windows" {
		return true
	}
	if info.IsDir() {
		return info.Mode().Perm()&0o022 == 0
	}
	return info.Mode().Perm()&0o077 == 0
}

// RemoveKey deletes the key of the agent on port.
func RemoveKey(dir string, port int) { _ = os.Remove(KeyPath(dir, port)) }

// ReadKey reads the secret of the agent on port.
func ReadKey(dir string, port int) ([]byte, error) {
	info, err := os.Stat(KeyPath(dir, port))
	if err != nil {
		return nil, err
	}
	if !privateMode(info) {
		return nil, errors.New("the access key file is readable by others")
	}
	data, _ := os.ReadFile(KeyPath(dir, port)) // unreadable: empty, and not a valid key
	secret, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(secret) != secretBytes {
		return nil, errors.New("the access key file is not valid")
	}
	return secret, nil
}

func mac(secret []byte, parts ...string) string {
	h := hmac.New(sha256.New, secret)
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// SessionToken is the token that every route requires, derived from secret.
func SessionToken(secret []byte) string { return mac(secret, "datatug-session-v1") }

// MintCode returns a one-time code valid for CodeTTL from now. It needs the secret only.
func MintCode(secret []byte, now time.Time) (string, error) {
	nonce := make([]byte, nonceBytes)
	if _, err := randRead(nonce); err != nil {
		return "", err
	}
	exp := strconv.FormatInt(now.Add(CodeTTL).Unix(), 10)
	n := base64.RawURLEncoding.EncodeToString(nonce)
	return strings.Join([]string{codeVersion, exp, n, mac(secret, "code", exp, n)}, "."), nil
}

// Access holds the secret of a running agent and the codes it has accepted.
type Access struct {
	secret []byte
	now    func() time.Time

	mu   sync.Mutex
	used map[string]time.Time // nonce -> expiry
}

// New returns the Access of an agent with secret.
func New(secret []byte) *Access {
	return &Access{secret: secret, now: time.Now, used: map[string]time.Time{}}
}

// Authorized reports whether r carries the session token as `Authorization: Bearer <token>`.
func (a *Access) Authorized(r *http.Request) bool {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return false
	}
	token, ok := strings.CutPrefix(values[0], "Bearer ")
	want := SessionToken(a.secret)
	return ok && len(token) == len(want) && subtle.ConstantTimeCompare([]byte(token), []byte(want)) == 1
}

// Exchange accepts code once and returns the session token. A code that is malformed, tampered,
// expired or already used is ErrBadCode.
func (a *Access) Exchange(code string) (string, error) {
	parts := strings.Split(code, ".")
	if len(parts) != 4 || parts[0] != codeVersion {
		return "", ErrBadCode
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", ErrBadCode
	}
	if want := mac(a.secret, "code", parts[1], parts[2]); !hmac.Equal([]byte(want), []byte(parts[3])) {
		return "", ErrBadCode
	}
	expiry, now := time.Unix(exp, 0), a.now()
	if !now.Before(expiry) {
		return "", ErrBadCode
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for nonce, until := range a.used {
		if !now.Before(until) {
			delete(a.used, nonce)
		}
	}
	if _, seen := a.used[parts[2]]; seen {
		return "", ErrBadCode
	}
	a.used[parts[2]] = expiry
	return SessionToken(a.secret), nil
}

// Apply adds the session token of the local agent that req addresses, when req is for a loopback
// address and that agent has left its key in the state directory. It is how the CLI's own HTTP
// clients (and the daemon's probe) talk to an agent that requires the token; any other request is
// left as it is, so the token never goes to another host.
func Apply(req *http.Request) { ApplyIn(StateDir, req) }

// ApplyIn is Apply with the state directory given by dir.
func ApplyIn(dir func() string, req *http.Request) {
	if req == nil || req.URL == nil || req.Header.Get("Authorization") != "" || !isLoopback(req.URL) {
		return
	}
	port, err := strconv.Atoi(req.URL.Port())
	if err != nil {
		port = 80
		if req.URL.Scheme == "https" {
			port = 443
		}
	}
	if secret, err := ReadKey(dir(), port); err == nil {
		req.Header.Set("Authorization", "Bearer "+SessionToken(secret))
	}
}

func isLoopback(u *url.URL) bool {
	switch u.Hostname() {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}
