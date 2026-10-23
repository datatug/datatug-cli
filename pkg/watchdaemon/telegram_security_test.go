package watchdaemon

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestWatchSMSSharedProviderSuccessAndInvalidInput(t *testing.T) {
	original := http.DefaultTransport
	defer func() { http.DefaultTransport = original }()
	http.DefaultTransport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 201, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"sid":"SMaccepted"}`)), Request: r}, nil
	})
	sender := TwilioSMSSender{}
	if err := sender.Send(context.Background(), SMSMessage{ID: "sms-accepted", To: "+353123456789", Body: "Generic update"}); err != nil {
		t.Fatal(err)
	}
	if sender.Send(context.Background(), SMSMessage{}) == nil {
		t.Fatal("invalid SMS accepted")
	}
}
