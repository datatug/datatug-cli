package gauth

import (
	"context"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// A launcher owns its work until cancellation, then releases it before the
// consent flow returns. This fixture opens no OS process, browser or socket.
func TestGoogleCallbackCancellationWhileLaunching(t *testing.T) {
	l, ready := callbackFixture(t)
	entered, stopped := make(chan struct{}), make(chan struct{})
	openBrowser = func(ctx context.Context, _ string) error {
		<-ready
		close(entered)
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := getTokenFromWeb(ctx, &oauth2.Config{RedirectURL: "http://localhost:8080/oauth2callback"})
		done <- err
	}()
	<-entered
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled launch succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled launch retained flow")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("flow abandoned launcher work")
	}
	select {
	case <-l.closed:
	default:
		t.Fatal("flow retained listener")
	}
}

func TestGoogleBrowserLauncherAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := launchAuthBrowser(ctx, "https://accounts.google.com/unused-fixture"); err == nil {
		t.Fatal("cancelled launcher invoked opener")
	}
}
