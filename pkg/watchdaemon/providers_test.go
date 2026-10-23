package watchdaemon

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestWatchEmailSinkCrashRecoveryCollisionAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	sink := &FileEmailSink{Path: filepath.Join(dir, "mail")}
	m := EmailMessage{ID: "delivery-1", To: "alice@example.test", Subject: "DataTug Watch update", Body: "Open DataTug"}
	if err := os.MkdirAll(sink.Path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sink.Path, ".pending-interrupted"), []byte(`{"id":`), 0600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := (&FileEmailSink{Path: sink.Path}).Send(context.Background(), m); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := (&FileEmailSink{Path: sink.Path}).Send(context.Background(), m); err != nil {
		t.Fatal("restart retry", err)
	}
	m.Body = "different"
	if err := sink.Send(context.Background(), m); err == nil {
		t.Fatal("collision replaced delivered message")
	}
	symlink := filepath.Join(dir, "link")
	if err := os.Symlink(sink.Path, symlink); err != nil {
		t.Fatal(err)
	}
	if err := (&FileEmailSink{Path: symlink}).Send(context.Background(), m); err == nil {
		t.Fatal("symlink sink accepted")
	}
	m.ID = "invalid>header"
	if validateEmail(m) == nil {
		t.Fatal("unsafe Message-ID accepted")
	}
}

func TestWatchSMTPDevelopmentReceiverAndProviderFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	received := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		_, _ = io.WriteString(conn, "220 Watch test SMTP\r\n")
		var message strings.Builder
		inData := false
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if line == ".\r\n" {
					received <- message.String()
					inData = false
					_, _ = io.WriteString(conn, "250 accepted\r\n")
				} else {
					message.WriteString(line)
				}
				continue
			}
			switch {
			case strings.HasPrefix(line, "EHLO"):
				_, _ = io.WriteString(conn, "250 Watch test SMTP\r\n")
			case strings.HasPrefix(line, "MAIL"), strings.HasPrefix(line, "RCPT"):
				_, _ = io.WriteString(conn, "250 ok\r\n")
			case strings.HasPrefix(line, "DATA"):
				inData = true
				_, _ = io.WriteString(conn, "354 send\r\n")
			case strings.HasPrefix(line, "QUIT"):
				_, _ = io.WriteString(conn, "221 bye\r\n")
				return
			default:
				_, _ = io.WriteString(conn, "500 unexpected\r\n")
			}
		}
	}()
	sender := SMTPSender{Config: SMTPConfig{Address: listener.Addr().String(), From: "watch@example.test", AllowLoopbackPlaintext: true}}
	if err = sender.Send(context.Background(), EmailMessage{ID: "stable-123", To: "alice@example.test", Subject: "DataTug Watch ü", Body: "A condition changed."}); err != nil {
		t.Fatal(err)
	}
	message := <-received
	if !strings.Contains(message, "Message-ID: <stable-123@datatug-watch>") || !strings.Contains(message, "=?UTF-8?q?") {
		t.Fatalf("unsafe SMTP headers %q", message)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sender.Send(ctx, EmailMessage{ID: "cancel", To: "alice@example.test", Subject: "Update"}) == nil {
		t.Fatal("cancelled delivery accepted")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestWatchTwilioReusesSharedKernelAndRedactsProviderErrors(t *testing.T) {
	sender := TwilioSMSSender{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.Context().Err() != nil {
			return nil, r.Context().Err()
		}
		return nil, errors.New("sensitive +353123456789 token-secret")
	})}
	err := sender.Send(context.Background(), SMSMessage{ID: "sms-1", To: "+353123456789", Body: "A DataTug Watch changed"})
	if err == nil || strings.Contains(err.Error(), "353") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("provider error leaked %v", err)
	}
}

func TestWatchEndpointValidation(t *testing.T) {
	for _, endpoint := range []string{"http://public.example", "https://user:secret@example.com", "https://example.com/?token=secret", "https://example.com/#secret"} {
		if ValidateEndpoint(endpoint) == nil {
			t.Fatalf("accepted %s", endpoint)
		}
	}
	for _, endpoint := range []string{"https://coordinator.example", "http://127.0.0.1:8990", "http://[::1]:8990"} {
		if ValidateEndpoint(endpoint) != nil {
			t.Fatalf("rejected %s", endpoint)
		}
	}
}
