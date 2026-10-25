//go:build !windows

// Command datatug-entrypoint is the container PID 1. It applies the container's
// explicit owner-consent gate, then execs the actual CLI so signals and exit
// status reach the container runtime without a shell process in between.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const cliPath = "/usr/local/bin/datatug"

func invocation(args []string, localOwner string) ([]string, error) {
	if len(args) == 0 {
		return []string{"--help"}, nil
	}
	if args[0] == "healthcheck" {
		return nil, errors.New("healthcheck is handled by the entrypoint")
	}
	if asksForRunner(args) && localOwner != "true" {
		return nil, errors.New("daemon --run requires DATATUG_LOCAL_OWNER=true; a configured principal alone is not consent")
	}
	return args, nil
}

func asksForRunner(args []string) bool {
	daemonCommand := false
	runnerFlag := false
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "daemon" {
			daemonCommand = true
		}
		if arg == "--run" || arg == "--run=true" {
			runnerFlag = true
		}
		if value, ok := strings.CutPrefix(arg, "--run="); ok {
			// Cobra accepts every strconv boolean spelling, including 1/t/TRUE.
			// Invalid values fail in the CLI; only a parsed false is exempt.
			parsed, err := strconv.ParseBool(value)
			runnerFlag = runnerFlag || err != nil || parsed
		}
	}
	// A root persistent flag such as --config may precede the subcommand.
	// Searching the full argv avoids letting that supported ordering bypass the
	// container's local-owner admission check.
	return daemonCommand && runnerFlag
}

func healthcheck(ctx context.Context, rawURL string) error {
	client := &http.Client{
		Timeout: 3 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return healthcheckWithClient(ctx, rawURL, client)
}

func healthcheckWithClient(ctx context.Context, rawURL string, client *http.Client) (result error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid health URL")
	}
	host := net.ParseIP(u.Hostname())
	if (u.Scheme != "http" && u.Scheme != "https") || host == nil || !host.IsLoopback() ||
		u.Port() == "" || u.User != nil || u.Path != "/health" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("health URL must use a loopback IP, explicit port, and the /health path")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return errors.New("health URL has an invalid port")
	}

	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(checkCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return errors.New("could not create health request")
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("coordinator health request failed")
	}
	defer func() {
		if err := response.Body.Close(); err != nil && result == nil {
			result = errors.New("could not close coordinator health response")
		}
	}()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("coordinator health returned HTTP %d", response.StatusCode)
	}
	return nil
}

func run(args []string, getenv func(string) string) error {
	if len(args) == 1 && args[0] == "healthcheck" {
		if getenv("DATATUG_DAEMON_ROLE") != "coordinator" {
			return errors.New("healthcheck is available only for Coordinator containers")
		}
		if err := healthcheck(context.Background(), getenv("DATATUG_HEALTH_URL")); err != nil {
			return err
		}
		return nil
	}
	argv, err := invocation(args, getenv("DATATUG_LOCAL_OWNER"))
	if err != nil {
		return err
	}
	argv = append([]string{cliPath}, argv...)
	if err := syscall.Exec(cliPath, argv, os.Environ()); err != nil {
		return fmt.Errorf("exec DataTug CLI: %w", err)
	}
	return errors.New("exec returned unexpectedly")
}

func main() {
	if err := run(os.Args[1:], os.Getenv); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, strings.TrimSpace(err.Error()))
		os.Exit(1)
	}
}
