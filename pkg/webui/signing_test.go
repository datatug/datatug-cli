package webui

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return priv
}

func pubBase64(key ed25519.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
}

// signLine is what the release packager writes to manifest.json.sig.
func signLine(key ed25519.PrivateKey, manifest string) string {
	id := KeyID(key.Public().(ed25519.PublicKey))
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(key, []byte(manifest)))
	return SignatureFormat + " ed25519 " + id + " " + sig + "\n"
}

func useKeys(t *testing.T, keys ...ed25519.PrivateKey) {
	t.Helper()
	encoded := make([]string, len(keys))
	for i, k := range keys {
		encoded[i] = pubBase64(k)
	}
	restore, err := UseTrustedKeys(encoded...)
	require.NoError(t, err)
	t.Cleanup(restore)
}

func TestTrustedKeysCompiledIn(t *testing.T) {
	keys, err := parseReleaseKeys(trustedKeyBase64)
	require.NoError(t, err)
	require.NotEmpty(t, keys, "a release CLI trusts at least one key")
	assert.Equal(t, "1c581e307a880eeb", keys[0].id)
	assert.Equal(t, keys, mustReleaseKeys(trustedKeyBase64))
}

func TestParseReleaseKeys_Bad(t *testing.T) {
	for _, bad := range []string{"not base64!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		_, err := parseReleaseKeys([]string{bad})
		assert.Error(t, err, bad)
		_, err = UseTrustedKeys(bad)
		assert.Error(t, err, bad)
		assert.Panics(t, func() { mustReleaseKeys([]string{bad}) })
	}
}

func TestUseTrustedKeys_Restores(t *testing.T) {
	before := trustedKeys
	key := testKey(t)
	restore, err := UseTrustedKeys(pubBase64(key))
	require.NoError(t, err)
	require.Len(t, trustedKeys, 1)
	assert.Equal(t, KeyID(key.Public().(ed25519.PublicKey)), trustedKeys[0].id)
	restore()
	assert.Equal(t, before, trustedKeys)
}

func TestVerifySignature(t *testing.T) {
	const manifest = `{"format":"x"}`
	key, other := testKey(t), testKey(t)
	keys, err := parseReleaseKeys([]string{pubBase64(key)})
	require.NoError(t, err)
	id := keys[0].id
	good := signLine(key, manifest)
	forged := SignatureFormat + " ed25519 " + id + " " + base64.StdEncoding.EncodeToString(ed25519.Sign(other, []byte(manifest)))

	for name, tc := range map[string]struct {
		sig           string
		present       bool
		allowUnsigned bool
		keys          []releaseKey
		wantKey       string
		wantErr       string
		unsigned      bool
	}{
		"valid":                       {sig: good, present: true, keys: keys, wantKey: id},
		"valid, allow-unsigned too":   {sig: good, present: true, allowUnsigned: true, keys: keys, wantKey: id},
		"missing":                     {keys: keys, wantErr: "--allow-unsigned", unsigned: true},
		"missing, allow-unsigned":     {allowUnsigned: true, keys: keys},
		"missing, no trusted keys":    {},
		"tampered manifest":           {sig: signLine(key, manifest+" "), present: true, keys: keys, wantErr: "changed after signing"},
		"forged with another key":     {sig: forged, present: true, allowUnsigned: true, keys: keys, wantErr: "changed after signing"},
		"unknown key":                 {sig: signLine(other, manifest), present: true, keys: keys, wantErr: "does not trust", unsigned: true},
		"unknown key, allow-unsigned": {sig: signLine(other, manifest), present: true, allowUnsigned: true, keys: keys},
		"wrong format":                {sig: "datatug-web-ui-signature/2 ed25519 " + id + " AAAA", present: true, allowUnsigned: true, keys: keys, wantErr: "is not a"},
		"wrong algorithm":             {sig: SignatureFormat + " rsa " + id + " AAAA", present: true, keys: keys, wantErr: "is not a"},
		"too few fields":              {sig: "sig", present: true, allowUnsigned: true, keys: keys, wantErr: "is not a"},
		"bad base64":                  {sig: SignatureFormat + " ed25519 " + id + " !!!", present: true, allowUnsigned: true, keys: keys, wantErr: "no valid Ed25519"},
		"short signature":             {sig: SignatureFormat + " ed25519 " + id + " AAAA", present: true, keys: keys, wantErr: "no valid Ed25519"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := verifySignature([]byte(manifest), []byte(tc.sig), tc.present, tc.allowUnsigned, tc.keys)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				assert.Equal(t, tc.unsigned, errors.Is(err, ErrUnsigned))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantKey, got.KeyID)
		})
	}
}

func TestInstallArchive_SignatureRequired(t *testing.T) {
	key := testKey(t)
	useKeys(t, key)
	s := newStore(t)

	archive, _ := goodArchive(t, "1.0.0", ">=1")
	_, err := s.InstallArchive(archive, InstallOptions{})
	require.ErrorIs(t, err, ErrUnsigned)
	assert.Empty(t, versionDirs(t, s), "a refused archive leaves nothing")

	in, err := s.InstallArchive(archive, InstallOptions{AllowUnsigned: true})
	require.NoError(t, err)
	assert.Empty(t, in.Signature.KeyID)

	// A signature over a different manifest is refused even with AllowUnsigned.
	content := archiveEntries(t, "1.0.1", ">=1", appFiles())
	tampered := append([]tarEntry{file(SignatureFile, signLine(key, content[0].body+" "))}, content...)
	_, err = s.InstallArchive(writeFile(t, t.TempDir(), "b.tar.gz", tarGz(t, tampered...)), InstallOptions{AllowUnsigned: true})
	require.ErrorContains(t, err, "changed after signing")
	assert.Equal(t, []string{"1.0.0"}, versionDirs(t, s))
}
