package webui

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// SignatureFormat is the first field of manifest.json.sig:
//
//	datatug-web-ui-signature/1 ed25519 <key id> <base64 signature>
//
// The signature is Ed25519 over the exact bytes of manifest.json. The manifest lists the size and SHA-256 of every
// file, so a valid signature vouches for the whole archive.
const SignatureFormat = "datatug-web-ui-signature/1"

// trustedKeyBase64 are the raw 32-byte Ed25519 public keys of DataTug web UI releases. The private key is held as
// the DATATUG_WEB_UI_SIGNING_KEY secret of datatug/datatug-apps (spec local-frontend-distribution REQ:trust-model);
// datatug-apps tools/package-web-ui/generate-signing-key.mjs generates a pair. To rotate, add the new key here, ship
// a CLI, then switch the secret; remove the old key once no supported release is signed with it.
var trustedKeyBase64 = []string{
	"zo9/5hNhla+dcZtCg9aggVtTh4AOllQSmXTAwVHWBds=", // 1c581e307a880eeb, 2026-11-07
}

// releaseKey is a trusted public key and its id: the first 16 hex characters of its SHA-256.
type releaseKey struct {
	id  string
	key ed25519.PublicKey
}

var trustedKeys = mustReleaseKeys(trustedKeyBase64)

func mustReleaseKeys(encoded []string) []releaseKey {
	keys, err := parseReleaseKeys(encoded)
	if err != nil {
		panic(err)
	}
	return keys
}

func parseReleaseKeys(encoded []string) ([]releaseKey, error) {
	keys := make([]releaseKey, 0, len(encoded))
	for _, e := range encoded {
		raw, err := base64.StdEncoding.DecodeString(e)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("release key %q is not a base64 Ed25519 public key", e)
		}
		keys = append(keys, releaseKey{id: KeyID(raw), key: ed25519.PublicKey(raw)})
	}
	return keys, nil
}

// KeyID names a public key: the first 16 hex characters of its SHA-256.
func KeyID(pub []byte) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])[:16]
}

// Signature is what an install found out about who signed an archive.
type Signature struct {
	// KeyID is the trusted key that signed the manifest; "" when the archive was installed unsigned.
	KeyID string
}

// ErrUnsigned is returned for an archive with no valid signature from a trusted key, when one is required.
var ErrUnsigned = errors.New("the archive is not signed by a DataTug release key")

// verifySignature checks manifest.json.sig against the trusted keys. A signature that does not verify is always
// refused: it means tampering. A missing signature, or one from a key this CLI does not know, is refused while this
// CLI has trusted keys, unless allowUnsigned (a local or test build).
func verifySignature(manifest, sig []byte, present, allowUnsigned bool, keys []releaseKey) (Signature, error) {
	if !present {
		if len(keys) > 0 && !allowUnsigned {
			return Signature{}, fmt.Errorf("%w (no %s); pass --allow-unsigned to install a local build anyway", ErrUnsigned, SignatureFile)
		}
		return Signature{}, nil
	}
	fields := strings.Fields(string(sig))
	if len(fields) != 4 || fields[0] != SignatureFormat || fields[1] != "ed25519" {
		return Signature{}, fmt.Errorf("%s is not a %s signature", SignatureFile, SignatureFormat)
	}
	raw, err := base64.StdEncoding.DecodeString(fields[3])
	if err != nil || len(raw) != ed25519.SignatureSize {
		return Signature{}, fmt.Errorf("%s holds no valid Ed25519 signature", SignatureFile)
	}
	for _, k := range keys {
		if k.id != fields[2] {
			continue
		}
		if !ed25519.Verify(k.key, manifest, raw) {
			return Signature{}, fmt.Errorf("%s does not verify with release key %s: the manifest was changed after signing", SignatureFile, k.id)
		}
		return Signature{KeyID: k.id}, nil
	}
	if allowUnsigned {
		return Signature{}, nil
	}
	return Signature{}, fmt.Errorf("%w: it is signed by key %s, which this CLI does not trust", ErrUnsigned, fields[2])
}

// UseTrustedKeys replaces the trusted release keys (raw Ed25519 public keys, base64) and returns a function that
// restores the previous ones. With no keys, unsigned archives install. It exists for tests of code that installs
// archives; it is compiled-in API only, never configuration a user or environment can set.
func UseTrustedKeys(encoded ...string) (restore func(), err error) {
	keys, err := parseReleaseKeys(encoded)
	if err != nil {
		return nil, err
	}
	previous := trustedKeys
	trustedKeys = keys
	return func() { trustedKeys = previous }, nil
}
