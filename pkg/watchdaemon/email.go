package watchdaemon

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// EmailMessage contains only notification metadata, never query rows or inputs.
type EmailMessage struct {
	ID      string `json:"id"`
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}
type EmailSender interface {
	Send(context.Context, EmailMessage) error
}

// FileEmailSink is a persistent development mailbox. Stable message IDs make
// retries idempotent, including after a daemon restart. Use one coordinator.
type FileEmailSink struct {
	Path string
	mu   sync.Mutex
}

type sinkTemporaryFile interface {
	Write([]byte) (int, error)
	Sync() error
	Close() error
	Name() string
}

var sinkCreateTemp = func(dir, pattern string) (sinkTemporaryFile, error) { return os.CreateTemp(dir, pattern) }
var sinkLink = os.Link
var sinkLstat = os.Lstat
var sinkReadFile = os.ReadFile
var sinkOpenDirectory = os.Open

func (s *FileEmailSink) Send(ctx context.Context, message EmailMessage) error {
	if err := validateEmail(message); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.Path, 0700); err != nil {
		return errors.New("watch email sink is unavailable")
	}
	info, err := sinkLstat(s.Path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("watch email sink must be a private directory")
	}
	sum := sha256.Sum256([]byte(message.ID))
	target := filepath.Join(s.Path, fmt.Sprintf("%x.json", sum))
	checkExisting := func() error {
		info, err := sinkLstat(target)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 128*1024 {
			return errors.New("watch email sink entry is unavailable")
		}
		body, err := sinkReadFile(target)
		var prior EmailMessage
		if err != nil || json.Unmarshal(body, &prior) != nil || prior != message {
			return errors.New("watch email message identity conflicts")
		}
		return nil
	}
	if _, err = sinkLstat(target); err == nil {
		return checkExisting()
	} else if !os.IsNotExist(err) {
		return errors.New("watch email sink entry is unavailable")
	}
	f, err := sinkCreateTemp(s.Path, ".pending-")
	if err != nil {
		return errors.New("watch email sink is unavailable")
	}
	defer os.Remove(f.Name())
	if err = json.NewEncoder(f).Encode(message); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.New("watch email sink cannot be written")
	}
	// Link publishes without replacing an existing ID. An interrupted temporary
	// file is ignored, and a retry validates the complete published message.
	if err = sinkLink(f.Name(), target); err != nil {
		if os.IsExist(err) {
			return checkExisting()
		}
		return errors.New("watch email sink cannot publish delivery")
	}
	dir, err := sinkOpenDirectory(s.Path)
	if err != nil {
		return errors.New("watch email sink directory unavailable")
	}
	defer dir.Close()
	return dir.Sync()
}

type SMTPConfig struct {
	Address     string `json:"address"`
	From        string `json:"from"`
	UsernameEnv string `json:"usernameEnv,omitempty"`
	PasswordEnv string `json:"passwordEnv,omitempty"`
	// AllowLoopbackPlaintext is only for a local development SMTP receiver.
	AllowLoopbackPlaintext bool `json:"allowLoopbackPlaintext,omitempty"`
}
type SMTPSender struct{ Config SMTPConfig }

func validateEmail(m EmailMessage) error {
	if m.ID == "" || len(m.ID) > 256 || strings.ContainsAny(m.Subject, "\r\n") || len(m.Subject) > 300 || len(m.Body) > 32000 {
		return errors.New("invalid Watch email metadata")
	}
	for _, r := range m.ID {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			return errors.New("invalid Watch email identity")
		}
	}
	if _, err := mail.ParseAddress(m.To); err != nil || strings.ContainsAny(m.To, "\r\n") {
		return errors.New("invalid Watch email recipient")
	}
	return nil
}

func (s SMTPSender) Send(ctx context.Context, m EmailMessage) error {
	if err := validateEmail(m); err != nil {
		return err
	}
	cfg := s.Config
	from, err := mail.ParseAddress(cfg.From)
	if err != nil || strings.ContainsAny(cfg.From, "\r\n") {
		return errors.New("invalid Watch email sender")
	}
	to, _ := mail.ParseAddress(m.To)
	host, _, err := net.SplitHostPort(cfg.Address)
	if err != nil {
		return errors.New("invalid Watch SMTP address")
	}
	conn, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", cfg.Address)
	if err != nil {
		return errors.New("watch SMTP connection failed")
	}
	defer conn.Close()
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return errors.New("watch SMTP greeting failed")
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); ok {
		if client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}) != nil {
			return errors.New("watch SMTP TLS failed")
		}
	} else if ip := net.ParseIP(host); !cfg.AllowLoopbackPlaintext || ip == nil || !ip.IsLoopback() {
		return errors.New("watch SMTP requires TLS")
	}
	if cfg.UsernameEnv != "" || cfg.PasswordEnv != "" {
		user, password := os.Getenv(cfg.UsernameEnv), os.Getenv(cfg.PasswordEnv)
		if user == "" || password == "" {
			return errors.New("watch SMTP credentials are unavailable")
		}
		if client.Auth(smtp.PlainAuth("", user, password, host)) != nil {
			return errors.New("watch SMTP authentication failed")
		}
	}
	if client.Mail(from.Address) != nil || client.Rcpt(to.Address) != nil {
		return errors.New("watch SMTP recipient rejected")
	}
	w, err := client.Data()
	if err != nil {
		return errors.New("watch SMTP data rejected")
	}
	body := strings.ReplaceAll(strings.ReplaceAll(m.Body, "\r\n", "\n"), "\n", "\r\n")
	_, err = fmt.Fprintf(w, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMessage-ID: <%s@datatug-watch>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n", cfg.From, m.To, mime.QEncoding.Encode("UTF-8", m.Subject), m.ID, body)
	if err != nil || w.Close() != nil {
		return errors.New("watch SMTP delivery failed")
	}
	// A successful DATA close includes the provider's acceptance response.
	// Losing the courtesy QUIT exchange cannot revoke that known acceptance.
	_ = client.Quit()
	return nil
}
