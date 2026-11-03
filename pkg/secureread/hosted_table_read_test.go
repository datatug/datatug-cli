package secureread

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func hostedFixture(t *testing.T) (bindingFixture, Session) {
	t.Helper()
	f := newBindingFixture(t)
	data, e := os.ReadFile(f.target)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(data)
	c := hostedTableRegistration{Version: 1, SpaceID: "S1", ProjectID: "P1", ConnectionID: "C1", CatalogSHA256: strings.Repeat("a", 64), FileSHA256: hex.EncodeToString(sum[:]), Database: "controlled"}
	f.configValue.HostedTableRead, _ = json.Marshal(c)
	f.save(t)
	return f, f.load(t)
}
func TestHostedTableReadRegistrationClosed(t *testing.T) {
	if c, e := decodeHostedTableRegistration(nil); c != nil || e != nil {
		t.Fatal("absent")
	}
	f, _ := hostedFixture(t)
	valid := string(f.configValue.HostedTableRead)
	for _, raw := range []string{"null", "{}", `{"version":2}`, strings.Replace(valid, `"version":1`, `"version":1,"Version":1`, 1), strings.Replace(valid, `"spaceID":"S1"`, `"spaceID":"S1","\u0073paceID":"S1"`, 1), strings.Replace(valid, `"spaceID":"S1"`, `"spaceID":"S1","spaceId":"S1"`, 1), strings.Replace(valid, `"database":"controlled"`, `"database":" controlled"`, 1), strings.Replace(valid, `"database":"controlled"`, `"database":"bad\nname"`, 1), strings.Replace(valid, `"catalogSHA256":"`+strings.Repeat("a", 64), `"catalogSHA256":"bad`, 1), strings.Replace(valid, `"database":"controlled"`, `"database":"controlled","unexpected":true`, 1)} {
		t.Run(raw, func(t *testing.T) {
			if c, e := decodeHostedTableRegistration([]byte(raw)); e == nil || c != nil {
				t.Fatal("accepted malformed registration")
			}
		})
	}
	writeBindingFile(t, f.config, []byte(strings.Replace(string(mustHostedConfig(t, f)), `"version":1`, `"version":2`, 1)))
	if _, _, e := LoadBoundSession(context.Background(), f.root, BoundSessionOptions{Required: true}); e == nil {
		t.Fatal("unsupported config downgraded")
	}
}
func mustHostedConfig(t *testing.T, f bindingFixture) []byte {
	t.Helper()
	b, e := json.Marshal(f.configValue)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestHostedTableReadAvailableAbsentAndScope(t *testing.T) {
	f, s := hostedFixture(t)
	v, e := s.HostedTableRead(context.Background(), "bound", "local", "one")
	if e != nil || v.Status != "available" || v.SpaceID != "S1" || v.ProjectID != "P1" || v.ConnectionID != "C1" || v.Fingerprint.Engine != "sqlite" || v.Fingerprint.Database != "controlled" {
		t.Fatalf("available: %+v %v", v, e)
	}
	wire, _ := json.Marshal(v)
	t.Logf("available-contract: %s", wire)
	data, _ := os.ReadFile(f.target)
	sum := sha256.Sum256(data)
	if v.Fingerprint.EngineIdentity != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatal("not actual SHA")
	}
	for _, scope := range [][3]string{{"other", "local", "one"}, {"bound", "other", "one"}, {"bound", "local", "two"}} {
		if v, e = s.HostedTableRead(context.Background(), scope[0], scope[1], scope[2]); v != nil || !errors.Is(e, ErrAccessDenied) {
			t.Fatal("scope leaked")
		}
	}
	if v, e = (Session{}).HostedTableRead(context.Background(), "", "", ""); v != nil || e != nil {
		t.Fatal("legacy")
	}
	absent := newBindingFixture(t).load(t)
	if v, e = absent.HostedTableRead(context.Background(), "bound", "local", "one"); v != nil || e != nil {
		t.Fatal("unregistered")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if v, e = s.HostedTableRead(ctx, "bound", "local", "one"); v != nil || e == nil {
		t.Fatal("cancelled")
	}
}
func TestHostedTableReadContentAndLateChanges(t *testing.T) {
	for _, kind := range []string{"content", "replace", "symlink", "config", "registry", "late-config", "late-cancel", "late-size", "late-path", "read-error", "open-error", "closed-file", "oversize", "read-overflow", "stat-error"} {
		t.Run(kind, func(t *testing.T) {
			f, s := hostedFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			oldRead, oldOpen, oldStat := hostedSourceRead, hostedSourceOpen, bindingStat
			t.Cleanup(func() { hostedSourceRead, hostedSourceOpen, bindingStat = oldRead, oldOpen, oldStat })
			switch kind {
			case "content":
				file, e := os.OpenFile(f.target, os.O_WRONLY, 0)
				if e != nil {
					t.Fatal(e)
				}
				_, _ = file.WriteAt([]byte("changed"), 0)
				_ = file.Close()
			case "replace":
				_ = os.Rename(f.other, f.target)
			case "symlink":
				_ = os.Remove(f.target)
				_ = os.Symlink(f.other, f.target)
			case "config":
				writeBindingFile(t, f.config, []byte("{}"))
			case "registry":
				writeBindingFile(t, f.root+"/environments/local/new.json", []byte("{}"))
			case "open-error":
				hostedSourceOpen = func(string) (*os.File, error) { return nil, io.ErrUnexpectedEOF }
			case "closed-file":
				hostedSourceOpen = func(p string) (*os.File, error) {
					f, e := os.Open(p)
					if e == nil {
						_ = f.Close()
					}
					return f, e
				}
			case "oversize":
				if e := os.Truncate(f.target, (64<<20)+1); e != nil {
					t.Fatal(e)
				}
			default:
				called := false
				hostedSourceRead = func(file *os.File, p []byte) (int, error) {
					n, e := oldRead(file, p)
					if !called {
						called = true
						switch kind {
						case "late-config":
							writeBindingFile(t, f.config, []byte("{}"))
						case "late-cancel":
							cancel()
						case "late-size":
							_ = os.Truncate(f.target, 1)
						case "late-path":
							_ = os.Rename(f.other, f.target)
						case "read-error":
							return 0, io.ErrUnexpectedEOF
						case "read-overflow":
							_ = os.Truncate(f.target, (64<<20)+1)
						case "stat-error":
							bindingStat = func(string) (os.FileInfo, error) { return nil, io.ErrUnexpectedEOF }
						}
					}
					return n, e
				}
			}
			v, e := s.HostedTableRead(ctx, "bound", "local", "one")
			if e == nil {
				b, _ := json.Marshal(v)
				if string(b) != `{"version":1,"status":"unavailable","reasonCode":"source-binding-unavailable"}` {
					t.Fatalf("unsafe routing response %s", b)
				}
			} else if v != nil {
				t.Fatal("denial leaked routing")
			}
		})
	}
}
