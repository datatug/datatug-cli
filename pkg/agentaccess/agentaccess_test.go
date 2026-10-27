package agentaccess

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func secretOf(t *testing.T) []byte {
	t.Helper()
	s, err := NewSecret()
	if err != nil || len(s) != secretBytes {
		t.Fatal(err)
	}
	return s
}

func TestKeyRoundTripAndMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	secret := secretOf(t)
	if err := WriteKey(dir, 8989, secret); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(KeyPath(dir, 8989))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", info, err)
	}
	got, err := ReadKey(dir, 8989)
	if err != nil || string(got) != string(secret) {
		t.Fatal(err)
	}
	RemoveKey(dir, 8989)
	if _, err = ReadKey(dir, 8989); err == nil {
		t.Fatal("removed key must not read")
	}
	_ = os.WriteFile(KeyPath(dir, 1), []byte("zz"), 0o600)
	if _, err = ReadKey(dir, 1); err == nil {
		t.Fatal("a corrupt key must not read")
	}
	if err = WriteKey(filepath.Join(dir, "access-1.key", "x"), 1, secret); err == nil {
		t.Fatal("an unwritable directory must fail")
	}
	_ = os.MkdirAll(filepath.Join(KeyPath(dir, 2)+".tmp", "x"), 0o700)
	if err = WriteKey(dir, 2, secret); err == nil {
		t.Fatal("an unwritable key file must fail")
	}
	// a stale or planted tmp file (a link, say) is replaced, never written through
	target := filepath.Join(t.TempDir(), "victim")
	_ = os.Symlink(target, KeyPath(dir, 3)+".tmp")
	if err = WriteKey(dir, 3, secret); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(target); err == nil {
		t.Fatal("the key was written through a link")
	}
	// a group-readable key and a group-writable directory are repaired, never left shared
	if err = os.Chmod(KeyPath(dir, 3), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadKey(dir, 3); err == nil {
		t.Fatal("a key readable by others must not read")
	}
	if err = os.Chmod(dir, 0o770); err != nil {
		t.Fatal(err)
	}
	if err = WriteKey(dir, 4, secret); err != nil {
		t.Fatal(err)
	}
	if info, err = os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("a shared directory must be repaired to private mode: %#o %v", info.Mode().Perm(), err)
	}
	_ = os.Chmod(dir, 0o700)
}

func TestStateDir(t *testing.T) {
	t.Setenv(DirEnv, "/x/y")
	if StateDir() != "/x/y" {
		t.Fatal(StateDir())
	}
	t.Setenv(DirEnv, "")
	if !strings.HasSuffix(StateDir(), filepath.Join("datatug", "daemon")) {
		t.Fatal(StateDir())
	}
}

func TestExchange(t *testing.T) {
	secret := secretOf(t)
	now := time.Unix(1_800_000_000, 0)
	a := New(secret)
	a.now = func() time.Time { return now }
	code, err := MintCode(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(code, "#&=? /") {
		t.Fatalf("a code must be fragment-safe: %s", code)
	}
	token, err := a.Exchange(code)
	if err != nil || token != SessionToken(secret) {
		t.Fatal(token, err)
	}
	if _, err = a.Exchange(code); err != ErrBadCode {
		t.Fatalf("a code is single-use: %v", err)
	}
	// tampered pieces, malformed, other secret
	other, _ := MintCode(secretOf(t), now)
	fresh, _ := MintCode(secret, now)
	p := strings.Split(fresh, ".")
	for name, bad := range map[string]string{
		"empty": "", "three parts": "v1.1.2", "version": "v2." + strings.Join(p[1:], "."),
		"expiry": "v1.x." + p[2] + "." + p[3], "later expiry": "v1.9999999999." + p[2] + "." + p[3],
		"signature": strings.Join(p[:3], ".") + ".AAAA", "other secret": other,
	} {
		if _, err = a.Exchange(bad); err != ErrBadCode {
			t.Errorf("%s: %v", name, err)
		}
	}
	// expiry; expired nonces are forgotten
	later := now.Add(CodeTTL)
	a.now = func() time.Time { return later }
	if _, err = a.Exchange(fresh); err != ErrBadCode {
		t.Fatalf("an expired code: %v", err)
	}
	if len(a.used) != 1 {
		t.Fatalf("the used nonce of an expired code is still kept: %d", len(a.used))
	}
	last, _ := MintCode(secret, later)
	if _, err = a.Exchange(last); err != nil || len(a.used) != 1 {
		t.Fatalf("%v %d", err, len(a.used))
	}
}

func TestAuthorized(t *testing.T) {
	secret := secretOf(t)
	a := New(secret)
	good := "Bearer " + SessionToken(secret)
	for header, want := range map[string][]string{
		"ok": {good}, "": nil, "wrong scheme": {"Basic " + SessionToken(secret)}, "wrong token": {"Bearer nope"},
		"two": {good, good}, "short": {"Bearer "},
	} {
		r := httptest.NewRequest(http.MethodGet, "/x", nil)
		for _, v := range want {
			r.Header.Add("Authorization", v)
		}
		if got := a.Authorized(r); got != (header == "ok") {
			t.Errorf("%q: %v", header, got)
		}
	}
}

func TestApply(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := secretOf(t)
	if err := WriteKey(dir, 8989, secret); err != nil {
		t.Fatal(err)
	}
	if err := WriteKey(dir, 80, secret); err != nil {
		t.Fatal(err)
	}
	if err := WriteKey(dir, 443, secret); err != nil {
		t.Fatal(err)
	}
	want := "Bearer " + SessionToken(secret)
	for url, token := range map[string]bool{
		"http://127.0.0.1:8989/x": true, "http://localhost:8989/x": true, "http://[::1]:8989/x": true,
		"http://127.0.0.1/x": true, "https://localhost/x": true,
		"http://127.0.0.1:9/x": false, "https://datatug.app:8989/x": false, "http://example.com/x": false,
	} {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		ApplyIn(func() string { return dir }, req)
		if got := req.Header.Get("Authorization") == want; got != token {
			t.Errorf("%s: %v", url, got)
		}
	}
	// an explicit Authorization is kept, nil is tolerated, the default dir is the state dir
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8989/x", nil)
	req.Header.Set("Authorization", "Bearer mine")
	ApplyIn(func() string { return dir }, req)
	if req.Header.Get("Authorization") != "Bearer mine" {
		t.Fatal("an explicit token is replaced")
	}
	ApplyIn(func() string { return dir }, nil)
	t.Setenv(DirEnv, dir)
	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8989/x", nil)
	Apply(req)
	if req.Header.Get("Authorization") != want {
		t.Fatal("Apply uses the state directory")
	}
}

func TestRandomnessFailure(t *testing.T) {
	orig := randRead
	defer func() { randRead = orig }()
	randRead = func([]byte) (int, error) { return 0, os.ErrInvalid }
	if _, err := NewSecret(); err == nil {
		t.Fatal("NewSecret must fail")
	}
	if _, err := MintCode(nil, time.Now()); err == nil {
		t.Fatal("MintCode must fail")
	}
}

func TestPrivateModeIgnoredOnWindows(t *testing.T) {
	orig := goos
	defer func() { goos = orig }()
	goos = "windows"
	info, err := os.Stat(t.TempDir())
	if err != nil || !privateMode(info) {
		t.Fatal(err)
	}
}
