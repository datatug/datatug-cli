package watchdaemon

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type faultySinkFile struct {
	*os.File
	phase string
}

func (f faultySinkFile) Write(p []byte) (int, error) {
	if f.phase == "write" {
		return 0, errors.New("disk full")
	}
	return f.File.Write(p)
}
func (f faultySinkFile) Sync() error {
	if f.phase == "sync" {
		return errors.New("durability unavailable")
	}
	return f.File.Sync()
}
func (f faultySinkFile) Close() error {
	err := f.File.Close()
	if f.phase == "close" {
		return errors.New("close failed")
	}
	return err
}

func TestWatchEmailSinkDurabilityFailuresDoNotAcknowledge(t *testing.T) {
	m := EmailMessage{ID: "sink-fault", To: "alice@example.test", Subject: "Update", Body: "Generic update"}
	ctx := context.Background()
	if (&FileEmailSink{Path: t.TempDir()}).Send(ctx, EmailMessage{}) == nil {
		t.Fatal("invalid sink message accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if (&FileEmailSink{Path: t.TempDir()}).Send(cancelled, m) == nil {
		t.Fatal("cancelled sink accepted")
	}
	file := filepath.Join(t.TempDir(), "regular")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if (&FileEmailSink{Path: filepath.Join(file, "child")}).Send(ctx, m) == nil {
		t.Fatal("non-directory parent accepted")
	}
	for _, phase := range []string{"create", "write", "sync", "close"} {
		t.Run(phase, func(t *testing.T) {
			original := sinkCreateTemp
			defer func() { sinkCreateTemp = original }()
			sinkCreateTemp = func(dir, pattern string) (sinkTemporaryFile, error) {
				if phase == "create" {
					return nil, errors.New("disk full")
				}
				f, err := os.CreateTemp(dir, pattern)
				return faultySinkFile{f, phase}, err
			}
			if (&FileEmailSink{Path: t.TempDir()}).Send(ctx, m) == nil {
				t.Fatal("durability fault acknowledged")
			}
		})
	}
	for _, phase := range []string{"stat-target", "stat-existing", "read-existing", "publish", "open-directory"} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			sink := &FileEmailSink{Path: dir}
			origStat, origRead, origLink, origOpen := sinkLstat, sinkReadFile, sinkLink, sinkOpenDirectory
			defer func() { sinkLstat, sinkReadFile, sinkLink, sinkOpenDirectory = origStat, origRead, origLink, origOpen }()
			if phase == "stat-existing" || phase == "read-existing" {
				if err := sink.Send(ctx, m); err != nil {
					t.Fatal(err)
				}
			}
			switch phase {
			case "stat-target":
				sinkLstat = func(path string) (os.FileInfo, error) {
					if path == dir {
						return os.Lstat(path)
					}
					return nil, os.ErrPermission
				}
			case "stat-existing":
				sinkLstat = func(path string) (os.FileInfo, error) {
					info, err := os.Lstat(path)
					if path != dir && err == nil {
						return fileInfoDirectory{info}, nil
					}
					return info, err
				}
			case "read-existing":
				sinkReadFile = func(string) ([]byte, error) { return nil, os.ErrPermission }
			case "publish":
				sinkLink = func(string, string) error { return errors.New("link unavailable") }
			case "open-directory":
				sinkOpenDirectory = func(string) (*os.File, error) { return nil, os.ErrPermission }
			}
			if sink.Send(ctx, m) == nil {
				t.Fatal("storage fault acknowledged")
			}
		})
	}
}

type fileInfoDirectory struct{ os.FileInfo }

func (fileInfoDirectory) Mode() os.FileMode { return os.ModeDir | 0700 }

func smtpFaultReceiver(t *testing.T, phase string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		if phase == "greeting" {
			_, _ = io.WriteString(conn, "500 rejected\r\n")
			return
		}
		_, _ = io.WriteString(conn, "220 ready\r\n")
		reader := bufio.NewReader(conn)
		inData := false
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if line == ".\r\n" {
					if phase == "finish" {
						_, _ = io.WriteString(conn, "554 failed\r\n")
					} else {
						_, _ = io.WriteString(conn, "250 accepted\r\n")
					}
					return
				}
				continue
			}
			switch {
			case strings.HasPrefix(line, "EHLO"):
				if phase == "tls" {
					_, _ = io.WriteString(conn, "250-test\r\n250 STARTTLS\r\n")
				} else {
					_, _ = io.WriteString(conn, "250-test\r\n250 AUTH PLAIN\r\n")
				}
			case strings.HasPrefix(line, "STARTTLS"):
				_, _ = io.WriteString(conn, "220 start\r\n")
				return
			case strings.HasPrefix(line, "AUTH"):
				_, _ = io.WriteString(conn, "535 denied\r\n")
				return
			case strings.HasPrefix(line, "MAIL"):
				if phase == "recipient" {
					_, _ = io.WriteString(conn, "550 denied\r\n")
					return
				}
				_, _ = io.WriteString(conn, "250 ok\r\n")
			case strings.HasPrefix(line, "RCPT"):
				_, _ = io.WriteString(conn, "250 ok\r\n")
			case strings.HasPrefix(line, "DATA"):
				if phase == "data" {
					_, _ = io.WriteString(conn, "554 denied\r\n")
					return
				}
				inData = true
				_, _ = io.WriteString(conn, "354 send\r\n")
			default:
				_, _ = io.WriteString(conn, "221 bye\r\n")
				return
			}
		}
	}()
	return listener.Addr().String()
}

func TestWatchSMTPFailureClassificationAndKnownAcceptance(t *testing.T) {
	m := EmailMessage{ID: "smtp-fault", To: "alice@example.test", Subject: "Update", Body: "Generic update"}
	for _, phase := range []string{"greeting", "tls", "no-tls", "missing-auth", "auth", "recipient", "data", "finish", "accepted-close"} {
		t.Run(phase, func(t *testing.T) {
			cfg := SMTPConfig{Address: smtpFaultReceiver(t, phase), From: "watch@example.test", AllowLoopbackPlaintext: true}
			if phase == "no-tls" {
				cfg.AllowLoopbackPlaintext = false
			}
			if phase == "missing-auth" {
				cfg.UsernameEnv = "WATCH_AUTH_MISSING"
			}
			if phase == "auth" {
				t.Setenv("WATCH_SMTP_USER", "user")
				t.Setenv("WATCH_SMTP_PASS", "password")
				cfg.UsernameEnv = "WATCH_SMTP_USER"
				cfg.PasswordEnv = "WATCH_SMTP_PASS"
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := (SMTPSender{Config: cfg}).Send(ctx, m)
			if phase == "accepted-close" {
				if err != nil {
					t.Fatalf("known DATA acceptance retried %v", err)
				}
			} else if err == nil {
				t.Fatal("SMTP fault acknowledged")
			}
		})
	}
	for _, cfg := range []SMTPConfig{{From: "invalid"}, {From: "watch@example.test", Address: "invalid"}} {
		if (SMTPSender{Config: cfg}).Send(context.Background(), m) == nil {
			t.Fatal("invalid SMTP config accepted")
		}
	}
	if (SMTPSender{}).Send(context.Background(), EmailMessage{}) == nil {
		t.Fatal("invalid SMTP message accepted")
	}
}

func TestWatchTelegramProviderFaultsAndGatewayCapability(t *testing.T) {
	ctx := context.Background()
	m := TelegramMessage{ID: "telegram-1", ChatID: "12345", Body: "Generic Watch update", DeliveryCapability: "opaque-capability"}
	t.Setenv("WATCH_TELEGRAM_PROVIDER", "123:fake")
	t.Setenv("WATCH_TELEGRAM_GATEWAY", strings.Repeat("gateway", 8))
	for _, sender := range []BotTelegramSender{{TokenEnv: "missing"}, {TokenEnv: "WATCH_TELEGRAM_PROVIDER", APIBaseURL: "http://evil.example"}} {
		if sender.Send(ctx, m) == nil {
			t.Fatal("invalid Telegram config accepted")
		}
	}
	if (BotTelegramSender{}).Send(ctx, TelegramMessage{ChatID: "group"}) == nil {
		t.Fatal("invalid recipient accepted")
	}
	failing := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("sensitive provider token") })}
	if (BotTelegramSender{TokenEnv: "WATCH_TELEGRAM_PROVIDER", HTTP: failing}).Send(ctx, m) == nil {
		t.Fatal("failed default provider accepted")
	}
	for _, status := range []int{200, 302, 500} {
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "https://evil.example")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"ok":false}`)
		}))
		if (BotTelegramSender{TokenEnv: "WATCH_TELEGRAM_PROVIDER", APIBaseURL: provider.URL}).Send(ctx, m) == nil {
			t.Fatal("Telegram rejected delivery accepted")
		}
		provider.Close()
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("gateway", 8) {
			t.Error("gateway auth missing")
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer provider.Close()
	gateway := GatewayTelegramSender{Endpoint: provider.URL, TokenEnv: "WATCH_TELEGRAM_GATEWAY"}
	if err := gateway.Send(ctx, m); err != nil {
		t.Fatal(err)
	}
	if gateway.Send(nil, m) == nil {
		t.Fatal("invalid request context accepted")
	}
	gateway.HTTP = failing
	if gateway.Send(ctx, m) == nil {
		t.Fatal("network failure accepted")
	}
	if (GatewayTelegramSender{}).Send(ctx, m) == nil {
		t.Fatal("unconfigured gateway accepted")
	}
	gateway.Endpoint = provider.URL
	gateway.TokenEnv = "missing"
	if gateway.Send(ctx, m) == nil {
		t.Fatal("missing gateway credential accepted")
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, provider.URL, 302) }))
	defer redirect.Close()
	gateway.Endpoint = redirect.URL
	gateway.TokenEnv = "WATCH_TELEGRAM_GATEWAY"
	gateway.HTTP = nil
	if gateway.Send(ctx, m) == nil {
		t.Fatal("gateway credential followed redirect")
	}
	if (BotTelegramSender{TokenEnv: "WATCH_TELEGRAM_PROVIDER", APIBaseURL: provider.URL}).Send(nil, m) == nil {
		t.Fatal("invalid bot request context accepted")
	}
}
