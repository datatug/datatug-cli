// Package sessionresultcache retains bounded display results on one device.
// Entries are a private client cache; callers must freshly authorize their
// server run and input scope before either writing or reading them.
package sessionresultcache

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/datatug/datatug-cli/pkg/sessionstore"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
)

const DirEnv = "DATATUG_SESSION_RESULTS_DIR"
const MaxBytes = 4 << 20

var ErrUnavailable = errors.New("local dashboard display cache unavailable")

type Binding struct {
	Authority        string               `json:"authority"`
	Actor            string               `json:"actor"`
	DatabaseID       string               `json:"databaseId"`
	ProjectID        string               `json:"projectId"`
	SessionID        string               `json:"sessionId"`
	RunID            string               `json:"runId"`
	InputSnapshotID  string               `json:"inputSnapshotId"`
	Scope            sessioncontext.Scope `json:"scope"`
	DefinitionDigest string               `json:"definitionDigest"`
}
type entry struct {
	SchemaVersion int             `json:"schemaVersion"`
	Binding       Binding         `json:"binding"`
	PayloadDigest string          `json:"payloadDigest"`
	Payload       json.RawMessage `json:"payload"`
}
type Cache struct{ root string }

func New(root string) (*Cache, error) {
	resolved, err := sessionstore.ResolvePrivateDirectory(root)
	if err != nil {
		return nil, ErrUnavailable
	}
	return &Cache{root: resolved}, nil
}
func NewDefault() (*Cache, error) {
	root := os.Getenv(DirEnv)
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, ErrUnavailable
		}
		root = filepath.Join(home, ".datatug", "session-results")
	}
	return New(root)
}
func payloadDigest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
func (c *Cache) path(binding Binding) (string, error) {
	if binding.Authority == "" || binding.Actor == "" || binding.DatabaseID == "" || binding.ProjectID == "" || binding.SessionID == "" || binding.RunID == "" || binding.InputSnapshotID == "" || binding.Scope.EnvironmentID == "" || binding.Scope.IdentityDomain == "" || len(binding.DefinitionDigest) != 64 {
		return "", ErrUnavailable
	}
	encoded, _ := json.Marshal(binding)
	return filepath.Join(c.root, payloadDigest(encoded)+".json"), nil
}
func syncDirectory(root string) error {
	dir, err := os.Open(root)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
func (c *Cache) Put(binding Binding, payload json.RawMessage) error {
	var compact bytes.Buffer
	path, err := c.path(binding)
	if err != nil || !json.Valid(payload) || json.Compact(&compact, payload) != nil {
		return ErrUnavailable
	}
	payload = compact.Bytes()
	encoded, _ := json.Marshal(entry{SchemaVersion: 1, Binding: binding, PayloadDigest: payloadDigest(payload), Payload: payload})
	if len(encoded) > MaxBytes {
		return ErrUnavailable
	}
	resolved, err := sessionstore.ResolvePrivateDirectory(c.root)
	if err != nil || resolved != c.root {
		return ErrUnavailable
	}
	var f *os.File
	if err = os.MkdirAll(c.root, 0700); err == nil {
		if err = os.Chmod(c.root, 0700); err == nil {
			f, err = os.CreateTemp(c.root, ".dashboard-*.tmp")
		}
	}
	if err != nil {
		return ErrUnavailable
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, wErr := f.Write(encoded)
	sErr := f.Sync()
	cErr := f.Close()
	if errors.Join(wErr, sErr, cErr) != nil || os.Rename(f.Name(), path) != nil || syncDirectory(c.root) != nil {
		return ErrUnavailable
	}
	return nil
}
func (c *Cache) Get(binding Binding) (json.RawMessage, error) {
	path, err := c.path(binding)
	if err != nil {
		return nil, ErrUnavailable
	}
	resolved, err := sessionstore.ResolvePrivateDirectory(c.root)
	if err != nil || resolved != c.root {
		return nil, ErrUnavailable
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return nil, ErrUnavailable
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil || len(data) > MaxBytes {
		return nil, ErrUnavailable
	}
	var stored entry
	if apicontract.DecodeStrict(data, &stored) != nil || stored.SchemaVersion != 1 || stored.Binding != binding || payloadDigest(stored.Payload) != stored.PayloadDigest {
		return nil, ErrUnavailable
	}
	return append(json.RawMessage(nil), stored.Payload...), nil
}
