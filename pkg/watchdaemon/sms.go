package watchdaemon

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/sneat-co/sneat-go-core/twiliosms"
)

type SMSMessage struct{ ID, To, Body string }
type SMSSender interface {
	Send(context.Context, SMSMessage) error
}

func validPhone(phone string) bool {
	if len(phone) < 8 || len(phone) > 16 || phone[0] != '+' {
		return false
	}
	for _, r := range phone[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// FileSMSSink shares the crash-safe, ID-deduplicated development mailbox kernel.
// Its To field ends in @sms.test and preserves the verified E.164 recipient.
type FileSMSSink struct{ Sink *FileEmailSink }

func (s FileSMSSink) Send(ctx context.Context, m SMSMessage) error {
	if !validPhone(m.To) || len(m.Body) > 1000 {
		return errors.New("invalid Watch SMS")
	}
	return s.Sink.Send(ctx, EmailMessage{ID: m.ID, To: m.To + "@sms.test", Subject: "DataTug Watch SMS", Body: m.Body})
}

type contextTransport struct {
	ctx  context.Context
	base http.RoundTripper
}

func (t contextTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.base.RoundTrip(r.Clone(t.ctx))
}

// TwilioSMSSender reuses the ecosystem's shared SMS provider kernel. Credentials
// remain deployment-owned. Provider errors are deliberately reduced to a code.
type TwilioSMSSender struct {
	Credentials twiliosms.Credentials
	Transport   http.RoundTripper
}

func (s TwilioSMSSender) Send(ctx context.Context, m SMSMessage) error {
	if !validPhone(m.To) || len(m.Body) > 1000 || m.ID == "" {
		return errors.New("invalid Watch SMS")
	}
	transport := s.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client := twiliosms.NewClient(s.Credentials, &http.Client{Timeout: 20 * time.Second, Transport: contextTransport{ctx, transport}})
	_, exception, err := twiliosms.Send(client, s.Credentials, m.To, m.Body)
	if err != nil || exception != nil {
		return errors.New("Watch SMS delivery failed")
	}
	return nil
}
