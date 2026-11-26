package sshdemo

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	gossh "golang.org/x/crypto/ssh"
)

// LoadOrGenerateHostKey loads an SSH host key from path, refusing unreadable,
// invalid, or group/world-accessible files without ever overwriting them.
// When path does not exist, it generates an ed25519 key once and writes it
// atomically with mode 0600.
func LoadOrGenerateHostKey(path string) (gossh.Signer, error) {
	if path == "" {
		return nil, errors.New("sshdemo: host key path must not be empty")
	}
	info, err := os.Stat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("sshdemo: host key %s is not a regular file", path)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			return nil, fmt.Errorf("sshdemo: host key %s has insecure permissions %04o (must not be group/world-readable)", path, perm)
		}
		pemBytes, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, fmt.Errorf("sshdemo: read host key %s: %w", path, readErr)
		}
		signer, parseErr := gossh.ParsePrivateKey(pemBytes)
		if parseErr != nil {
			return nil, fmt.Errorf("sshdemo: parse host key %s: %w", path, parseErr)
		}
		return signer, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("sshdemo: stat host key %s: %w", path, err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("sshdemo: create host key dir %s: %w", dir, err)
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("sshdemo: generate ed25519 host key: %w", err)
	}
	block, err := gossh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, fmt.Errorf("sshdemo: marshal ed25519 host key: %w", err)
	}
	pemBytes := pem.EncodeToMemory(block)

	if err := writeFileExclusive0600(path, pemBytes); err != nil {
		return nil, err
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		return nil, fmt.Errorf("sshdemo: build host key signer: %w", err)
	}
	return signer, nil
}

// writeFileExclusive0600 writes data to a temporary file in the same directory
// with mode 0600, fsyncs it, and links it into path without overwriting any
// existing file.
func writeFileExclusive0600(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".ssh_host_key_*")
	if err != nil {
		return fmt.Errorf("sshdemo: create temp host key file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sshdemo: chmod temp host key: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sshdemo: write temp host key: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sshdemo: sync temp host key: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("sshdemo: close temp host key: %w", err)
	}
	if err := os.Link(tmpName, path); err != nil {
		return fmt.Errorf("sshdemo: install host key %s: %w", path, err)
	}
	return nil
}

// FormatHostKey formats the public key line (`ssh-ed25519 AAAA...`) and its
// SHA256 fingerprint (`SHA256:...`) on two lines.
func FormatHostKey(signer gossh.Signer) string {
	pub := signer.PublicKey()
	authLine := strings.TrimSpace(string(gossh.MarshalAuthorizedKey(pub)))
	fp := gossh.FingerprintSHA256(pub)
	return authLine + "\n" + fp + "\n"
}

// PrintHostKey loads (or generates once when absent) the host key at path and
// writes its public key and SHA256 fingerprint to w.
func PrintHostKey(path string, w io.Writer) error {
	signer, err := LoadOrGenerateHostKey(path)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, FormatHostKey(signer))
	return err
}
