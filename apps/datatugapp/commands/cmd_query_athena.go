package commands

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

const athenaPolicyNote = "access: native Athena reads; configured workgroup and explicit IAM identity govern access; DataTug application policies are not applied"

// queryAthenaCommand is the one-shot registered Athena read command. It reuses
// the reviewed native config/input/client/executor helpers and adds no control
// reader, shared stdin parser or test-only seam.
func queryAthenaCommand() *cobra.Command {
	var configPath, requestPath string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:           "athena --config <native.json> --request <file|->",
		Short:         "Read existing mapped Athena tables as bounded nonstreaming JSONL",
		SilenceUsage:  true,
		Args:          cobra.NoArgs,
		SilenceErrors: false,
		Long: `Run one admitted native read against trusted existing-table configuration:
get, exists, ordered getMany, supplied-ID testConnection, one paged query, or
static configuration metadata. Output is private, bounded, nonstreaming JSONL.
A query returns every native page in one original query or the command fails on
local output delivery; there is no live page streaming or saved continuation.
Same-process next/cancel controls and shared request/control stdin are not
available in this one-shot command.

Cells keep their original STRING text or null, so numeric, temporal and nested
values are not retyped. Metadata is read from the trusted configuration only and
never opens the SDK, reads ENV/CA material or performs live catalog/IAM
discovery. Credentials are named environment variables; they are never values in
the configuration file.

There is no raw SQL, account auth, S3 permission, billing cap, hosted terminal
Stop or portable continuation claim. One successful cleanup Stop response is
accepted cleanup only, not an observed remote terminal. Owned SDK work is
drained before stdout with an independent maximum2s cleanup scope, which may
extend the whole operation deadline.

` + athenaPolicyNote + `.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			original := cmd.Context()
			parent := original
			if parent == nil {
				parent = context.Background()
			}
			ctx, _, stop := interruptContext(parent)
			cmd.SetContext(ctx)
			defer func() { cmd.SetContext(original); stop() }()
			return runAthenaCommand(cmd, configPath, requestPath, timeout)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "Trusted existing-table mapping JSON, without credential values")
	cmd.Flags().StringVar(&requestPath, "request", "", "Closed native read JSON file, or '-' for stdin")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "Whole command deadline (1ms through2m); independent cleanup may add at most2s")
	return cmd
}

func athenaCommandError(err error, usage bool) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return Exit("Athena read cancelled", exitCodeDatabase)
	case errors.Is(err, context.DeadlineExceeded):
		return Exit("Athena read deadline exceeded", exitCodeDatabase)
	case usage:
		return Exit("Athena configuration or request is invalid or unreadable", exitCodeUsage)
	case errors.Is(err, errAthenaSessionCleanup):
		return Exit("Athena cleanup did not become locally quiescent within its bounded scope", exitCodeDatabase)
	case errors.Is(err, errAthenaSessionOutput), errors.Is(err, errAthenaSessionWriter):
		return Exit("Athena output delivery is unknown; no replay is attempted", exitCodeDatabase)
	default:
		return Exit("Athena read failed; check trusted table mapping, explicit IAM identity and limits", exitCodeDatabase)
	}
}

func runAthenaCommand(cmd *cobra.Command, configPath, requestPath string, timeout time.Duration) error {
	// A lost caller is never hidden behind a later usage classification.
	if err := athenaFresh(cmd.Context()); err != nil {
		return athenaCommandError(err, false)
	}
	if configPath == "" || configPath == "-" || requestPath == "" || timeout < time.Millisecond || timeout > 2*time.Minute {
		return athenaCommandError(errDynamoInput, true)
	}
	// One original deadline covers config, input, admission, opener and every
	// page; it is never renewed per phase.
	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()
	raw, err := readDynamoFile(ctx, configPath)
	if err != nil {
		return athenaCommandError(err, true)
	}
	config, err := decodeAthenaConfig(ctx, raw)
	if err != nil {
		return athenaCommandError(err, true)
	}
	if requestPath == "-" {
		raw, err = readDynamoDocument(ctx, cmd.InOrStdin())
	} else {
		raw, err = readDynamoFile(ctx, requestPath)
	}
	if err != nil {
		return athenaCommandError(err, true)
	}
	request, err := admitAthenaRequest(ctx, config, raw)
	if err != nil {
		return athenaCommandError(err, true)
	}
	// Static metadata stays entirely local: no SDK construction, ENV lookup, CA
	// read or live discovery is admitted before this branch.
	if request.operation == "metadata" {
		frame, _, err := athenaConfiguredMetadataFrame(ctx, config, athenaWireBudget{})
		if err != nil {
			return athenaCommandError(err, false)
		}
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), athenaPolicyNote)
		return athenaCommandError(finishAthenaOutput(ctx, cmd.OutOrStdout(), frame), false)
	}
	client, closeTransport, err := openAthenaClient(ctx, config, timeout)
	if err != nil {
		return athenaCommandError(err, false)
	}
	_, _ = fmt.Fprintln(cmd.ErrOrStderr(), athenaPolicyNote)
	frame, err := executeAthenaRead(ctx, client, closeTransport, request)
	if err != nil {
		return athenaCommandError(err, false)
	}
	return athenaCommandError(finishAthenaOutput(ctx, cmd.OutOrStdout(), frame), false)
}
