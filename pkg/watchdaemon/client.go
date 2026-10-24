package watchdaemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/datatug/datatug-cli/pkg/watch"
)

// ValidateEndpoint permits TLS, or explicit loopback HTTP for local operation.
// Credentials, fragments and query strings never belong in manifest endpoints.
func ValidateEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("watch endpoint must be an HTTPS URL without credentials, query or fragment")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" {
		ip := net.ParseIP(u.Hostname())
		if u.Hostname() == "localhost" || ip != nil && ip.IsLoopback() {
			return nil
		}
	}
	return errors.New("watch endpoint requires HTTPS or loopback HTTP")
}

type Client struct {
	Endpoint, Token string
	HTTP            *http.Client
}

func (c Client) call(ctx context.Context, path string, input, output any) error {
	if ValidateEndpoint(c.Endpoint) != nil || len(c.Token) < 32 {
		return errors.New("watch coordinator connection is invalid")
	}
	body, err := json.Marshal(input)
	if err != nil {
		return watch.ErrInvalid
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.Endpoint, "/")+"/v1/watch/"+path, bytes.NewReader(body))
	if err != nil {
		return watch.ErrInvalid
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	// Never forward a deployment credential through an HTTP redirect.
	owned := *client
	owned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := owned.Do(req)
	if err != nil {
		return errors.New("watch coordinator is unavailable")
	}
	// Closing a consumed response does not change its acknowledged result.
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		switch resp.StatusCode {
		case 401, 403:
			return watch.ErrForbidden
		case 409:
			return watch.ErrStaleLease
		case 400:
			return watch.ErrInvalid
		case 404:
			return watch.ErrNotFound
		}
		return errors.New("watch coordinator is unavailable")
	}
	if output == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 2*1024*1024))
		return err
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024))
	if decoder.Decode(output) != nil {
		return errors.New("watch coordinator response is invalid")
	}
	return nil
}
func (c Client) Register(ctx context.Context) error { return c.register(ctx, runnerRegistration{}) }
func (c Client) register(ctx context.Context, registration runnerRegistration) error {
	return c.call(ctx, "runner/register", registration, nil)
}
func (c Client) Claim(ctx context.Context) (*watch.Claim, error) {
	var out struct {
		Claim *watch.Claim `json:"claim"`
	}
	err := c.call(ctx, "runner/claim", struct{}{}, &out)
	return out.Claim, err
}
func (c Client) Heartbeat(ctx context.Context, id string, fence int64) error {
	return c.call(ctx, "runner/heartbeat", struct {
		ClaimID string `json:"claimId"`
		Fence   int64  `json:"fence"`
	}{id, fence}, nil)
}
func (c Client) Complete(ctx context.Context, result watch.Completion) error {
	return c.call(ctx, "runner/complete", result, nil)
}

type Executor interface {
	Execute(context.Context, watch.Claim) (watch.Completion, error)
}
type ExecutorFunc func(context.Context, watch.Claim) (watch.Completion, error)

func (f ExecutorFunc) Execute(ctx context.Context, c watch.Claim) (watch.Completion, error) {
	return f(ctx, c)
}

type Runner struct {
	Telegram                    *TelegramBotConfig
	telegramAPIBase             string
	telegramHTTP                *http.Client
	Client                      Client
	Executor                    Executor
	PollInterval, RenewInterval time.Duration
	RegistrationInterval        time.Duration
	// beforeSubmit is a local test seam for cancellation after renewal stops.
	beforeSubmit func()
}

// Run uses the same HTTP registration/claim/renew/complete protocol locally and
// remotely. A runner needs no inbound listener or source credentials on server.
func (r Runner) Run(ctx context.Context) error {
	if r.Executor == nil {
		return errors.New("watch runner has no Check executor")
	}
	var bot *telegramBot
	registrationBody := runnerRegistration{}
	if r.Telegram != nil {
		verifyCtx, verifyCancel := context.WithTimeout(ctx, 30*time.Second)
		var err error
		bot, err = newTelegramBot(verifyCtx, *r.Telegram, r.telegramAPIBase, r.telegramHTTP)
		verifyCancel()
		if err != nil {
			return err
		}
		registrationBody = runnerRegistration{TelegramBotID: bot.botID, TelegramBotUsername: bot.username}
	}
	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()
	botDone := make(chan error, 1)
	botStarted := false
	defer func() {
		workerCancel()
		if botStarted {
			_ = <-botDone // Join the cancelled worker; the primary result is already selected.
		}
	}()
	poll := r.PollInterval
	if poll <= 0 {
		poll = 2 * time.Second
	}
	registration := r.RegistrationInterval
	if registration <= 0 {
		registration = 30 * time.Second
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := r.Client.register(ctx, registrationBody); err != nil {
			if errors.Is(err, watch.ErrForbidden) {
				return err
			}
			slog.Warn("watch_coordinator_unavailable", "code", "REGISTRATION_UNAVAILABLE")
			if err = waitContext(ctx, poll); err != nil {
				return err
			}
			continue
		}
		if bot != nil && !botStarted {
			botStarted = true
			go func() { err := bot.run(workerCtx, r.Client); workerCancel(); botDone <- err }()
		}
		registered := time.Now()
		for ctx.Err() == nil {
			if workerCtx.Err() != nil {
				return errors.New("watch runner Telegram worker stopped")
			}
			if time.Since(registered) > registration {
				break
			}
			claim, err := r.Client.Claim(workerCtx)
			if err != nil {
				if errors.Is(err, watch.ErrForbidden) {
					return err
				}
				break
			}
			if claim != nil {
				slog.Info("watch_job_claimed", "run", claim.RunID, "fence", claim.Fence)
				if err := r.executeClaim(workerCtx, *claim); err != nil {
					slog.Warn("watch_job_unacknowledged", "run", claim.RunID, "code", "LEASE_OR_COORDINATOR_UNAVAILABLE")
				} else {
					slog.Info("watch_job_completed", "run", claim.RunID, "fence", claim.Fence)
				}
			}
			if err = waitContext(ctx, poll); err != nil {
				return err
			}
		}
		if err := waitContext(ctx, poll); err != nil {
			return err
		}
	}
}

func (r Runner) executeClaim(ctx context.Context, claim watch.Claim) error {
	jobCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	renew := r.RenewInterval
	if renew <= 0 {
		renew = 5 * time.Second
	}
	done := make(chan struct{})
	renewResult := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(renew)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				renewResult <- nil
				return
			case <-jobCtx.Done():
				renewResult <- jobCtx.Err()
				return
			case <-ticker.C:
				if err := r.Client.Heartbeat(jobCtx, claim.ClaimID, claim.Fence); err != nil {
					cancel()
					renewResult <- err
					return
				}
			}
		}
	}()
	result, err := r.Executor.Execute(jobCtx, claim)
	close(done)
	renewErr := <-renewResult
	if renewErr != nil {
		return renewErr
	}
	if r.beforeSubmit != nil {
		r.beforeSubmit()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	result.ClaimID, result.RunID, result.Fence = claim.ClaimID, claim.RunID, claim.Fence
	if err != nil {
		result.ExecutionStatus = "error"
		result.Outcome = "unknown"
		result.FailureCode = "SOURCE_UNAVAILABLE"
	}
	// Network failure may hide a committed completion; replay the exact metadata
	// only. The coordinator's completion digest rejects changed replay payloads.
	for attempt := 0; attempt < 3; attempt++ {
		err = r.Client.Complete(ctx, result)
		if err == nil || errors.Is(err, watch.ErrStaleLease) || errors.Is(err, watch.ErrForbidden) {
			return err
		}
		if err = waitContext(ctx, time.Duration(attempt+1)*time.Second); err != nil {
			return err
		}
	}
	return errors.New("watch completion acknowledgement is unavailable")
}
func waitContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
