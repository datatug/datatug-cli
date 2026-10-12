package gauth

import (
	"context"
	"os/exec"
	"runtime"
)

// launchAuthBrowser owns the OS opener process for the consent flow. CommandContext
// kills it on cancellation and Run waits for it; there is no abandoned launcher
// goroutine. No opener output (which can include the URL) enters diagnostics.
func launchAuthBrowser(ctx context.Context, url string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.CommandContext(ctx, "/usr/bin/open", url)
	case "windows":
		command = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", url)
	default:
		command = exec.CommandContext(ctx, "xdg-open", url)
	}
	command.WaitDelay = authDrainTimeout
	return command.Run()
}
