package commands

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/investigation"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const (
	incidentFormatFlag   = "format"
	incidentMutationFlag = "mutation"
	incidentSinceFlag    = "since"
)

type incidentCLI struct {
	client executionClient
	scope  apicontract.IncidentScope
}

func incidentCommand() *cobra.Command {
	command := &cobra.Command{
		Use: "incident", Short: "Work with incidents through a DataTug agent", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	command.PersistentFlags().String(executionAgentFlag, "", "DataTug agent base URL (default server.host/server.port or http://localhost:8989)")
	command.PersistentFlags().String(executionProjectFlag, "", "Project ID (optional when the agent serves exactly one project)")
	command.PersistentFlags().String(executionEnvironmentFlag, "", "Environment ID")
	command.PersistentFlags().String(executionStoreFlag, "", "Qualified incident store ID (default: project repository store)")
	command.PersistentFlags().StringP(incidentFormatFlag, "o", "", "Output format for reads: grid, json, or yaml")
	command.PersistentFlags().Bool(executionJSONFlag, false, "Print the exact JSON response")
	command.AddCommand(
		incidentListCommand(), incidentCreateCommand(), incidentShowCommand(), incidentAppendCommand(),
		incidentEventsCommand(false), incidentSearchCommand(), incidentSimilarCommand(), incidentMergeCommand(), incidentEventsCommand(true),
	)
	return command
}

func incidentListCommand() *cobra.Command {
	command := &cobra.Command{
		Use: "list", Short: "List current-policy incident views", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cli, err := newIncidentCLI(cmd)
			if err != nil {
				return incidentCommandError(err)
			}
			query := cli.values()
			statuses, _ := cmd.Flags().GetStringSlice("status")
			for _, status := range statuses {
				query.Add("status", status)
			}
			for _, flag := range []string{"query", "check", "board"} {
				value, _ := cmd.Flags().GetString(flag)
				if value != "" {
					query.Set(flag, value)
				}
			}
			var response apicontract.IncidentListResponse
			raw, err := cli.client.get(cmd.Context(), "/datatug/incidents?"+query.Encode(), &response)
			if err != nil {
				return incidentCommandError(err)
			}
			return writeIncidentOutput(cmd, raw, response, func(w io.Writer) {
				for _, incident := range response.Incidents {
					_, _ = fmt.Fprintf(w, "%s/%s  %-13s  %s\n", incident.Ref.StoreID, incident.Ref.IncidentID, incident.Status, incident.Title)
				}
			})
		},
	}
	command.Flags().StringSlice("status", nil, "Status filter (repeatable)")
	command.Flags().String("query", "", "Referenced query ID")
	command.Flags().String("check", "", "Referenced check ID")
	command.Flags().String("board", "", "Referenced board ID")
	return command
}

func incidentCreateCommand() *cobra.Command {
	command := &cobra.Command{
		Use: "create", Short: "Create an incident", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			title, _ := cmd.Flags().GetString("title")
			if strings.TrimSpace(title) == "" {
				return Exit("--title is required", exitCodeUsage)
			}
			cli, err := newIncidentCLI(cmd)
			if err != nil {
				return incidentCommandError(err)
			}
			mutationID := incidentMutationID(cmd)
			description, _ := cmd.Flags().GetString("description")
			request := apicontract.IncidentCreateRequest{
				IncidentScope: cli.scope, MutationID: mutationID, Title: title, Description: description,
				CanonicalContext: incidents.CanonicalContext{Facts: []investigation.Fact{}},
			}
			var response apicontract.IncidentResponse
			raw, err := cli.post(cmd.Context(), "/datatug/incidents", request, &response)
			if err != nil {
				return incidentCommandError(err)
			}
			return writeIncidentOutput(cmd, raw, response, func(w io.Writer) {
				_, _ = fmt.Fprintf(w, "%s/%s  %s\n", response.Incident.Ref.StoreID, response.Incident.Ref.IncidentID, response.Incident.Title)
			})
		},
	}
	command.Flags().String("title", "", "Incident title")
	command.Flags().String("description", "", "Incident description")
	command.Flags().String(incidentMutationFlag, "", "Idempotency key (generated when omitted)")
	return command
}

func incidentShowCommand() *cobra.Command {
	command := &cobra.Command{
		Use: "show <id>", Short: "Show a current or historical incident projection", Args: cobra.ExactArgs(1), SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cli, ref, err := newIncidentCLIRef(cmd, args[0])
			if err != nil {
				return incidentCommandError(err)
			}
			query := cli.values()
			at, _ := cmd.Flags().GetString("at")
			if at != "" {
				if _, parseErr := time.Parse(time.RFC3339, at); parseErr != nil {
					return Exit("--at must be RFC3339", exitCodeUsage)
				}
				query.Set("at", at)
			}
			var response apicontract.IncidentResponse
			raw, err := cli.client.get(cmd.Context(), incidentPath(ref)+"?"+query.Encode(), &response)
			if err != nil {
				return incidentCommandError(err)
			}
			return writeIncidentOutput(cmd, raw, response, func(w io.Writer) { writeIncidentGrid(w, response.Incident) })
		},
	}
	command.Flags().String("at", "", "Historical projection time (RFC3339)")
	return command
}

func incidentAppendCommand() *cobra.Command {
	command := &cobra.Command{
		Use: "append <id>", Short: "Append one incident event", Args: cobra.ExactArgs(1), SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cli, ref, err := newIncidentCLIRef(cmd, args[0])
			if err != nil {
				return incidentCommandError(err)
			}
			eventType, _ := cmd.Flags().GetString("type")
			payloadText, _ := cmd.Flags().GetString("payload")
			if eventType == "" || payloadText == "" {
				return Exit("--type and --payload are required", exitCodeUsage)
			}
			payload := json.RawMessage(payloadText)
			if !json.Valid(payload) {
				return Exit("--payload must be valid JSON", exitCodeUsage)
			}
			assertionText, _ := cmd.Flags().GetString("assertion")
			request := apicontract.IncidentAppendRequest{
				IncidentScope: cli.scope, MutationID: incidentMutationID(cmd), Incident: ref,
				Event: apicontract.IncidentEventInput{At: time.Now().UTC(), Type: incidents.EventType(eventType), Assertion: incidents.Assertion{Kind: incidents.AssertionKind(assertionText)}, Refs: incidentBacklinkRefs(cli.scope, cmd), Payload: payload},
			}
			if cmd.Flags().Changed("expected-seq") {
				expected, _ := cmd.Flags().GetUint64("expected-seq")
				request.ExpectedSeq = &expected
			}
			var response apicontract.IncidentAppendResponse
			raw, err := cli.post(cmd.Context(), incidentPath(ref)+"/events", request, &response)
			if err != nil {
				return incidentCommandError(err)
			}
			return writeIncidentOutput(cmd, raw, response, func(w io.Writer) { writeIncidentEventLine(w, response.Event) })
		},
	}
	command.Flags().String("type", "", "Event type")
	command.Flags().String("payload", "", "Event payload as JSON")
	command.Flags().String("assertion", string(incidents.AssertionClaim), "Assertion kind")
	command.Flags().Uint64("expected-seq", 0, "Required current sequence")
	command.Flags().String("query-ref", "", "Referenced query ID")
	command.Flags().String("check-ref", "", "Referenced check ID")
	command.Flags().String("board-ref", "", "Referenced board ID")
	command.Flags().String(incidentMutationFlag, "", "Idempotency key (generated when omitted)")
	return command
}

func incidentSearchCommand() *cobra.Command {
	command := &cobra.Command{
		Use: "search [text]", Short: "Search incident text and facts", Args: cobra.MaximumNArgs(1), SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cli, err := newIncidentCLI(cmd)
			if err != nil {
				return incidentCommandError(err)
			}
			facts, err := incidentFactSignals(cmd)
			if err != nil {
				return err
			}
			text := ""
			if len(args) == 1 {
				text = args[0]
			}
			if strings.TrimSpace(text) == "" && len(facts) == 0 {
				return Exit("search text or at least one --entity is required", exitCodeUsage)
			}
			request := apicontract.IncidentSearchRequest{IncidentScope: cli.scope, Text: text, Facts: facts}
			var response apicontract.IncidentSearchResponse
			raw, err := cli.post(cmd.Context(), "/datatug/incidents/search", request, &response)
			if err != nil {
				return incidentCommandError(err)
			}
			return writeIncidentOutput(cmd, raw, response, func(w io.Writer) {
				for _, match := range response.Matches {
					_, _ = fmt.Fprintf(w, "%s/%s  %s  %s\n", match.Incident.Ref.StoreID, match.Incident.Ref.IncidentID, matchedSignalText(match.MatchedSignals), match.Incident.Title)
				}
			})
		},
	}
	command.Flags().StringSlice("entity", nil, "Fact filter Entity.Field=value (repeatable)")
	return command
}

func incidentSimilarCommand() *cobra.Command {
	return &cobra.Command{
		Use: "similar <id>", Short: "Find deterministic similar incidents", Args: cobra.ExactArgs(1), SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cli, ref, err := newIncidentCLIRef(cmd, args[0])
			if err != nil {
				return incidentCommandError(err)
			}
			var response apicontract.IncidentSimilarResponse
			raw, err := cli.client.get(cmd.Context(), incidentPath(ref)+"/similar?"+cli.values().Encode(), &response)
			if err != nil {
				return incidentCommandError(err)
			}
			return writeIncidentOutput(cmd, raw, response, func(w io.Writer) {
				for _, match := range response.Matches {
					_, _ = fmt.Fprintf(w, "%s/%s  score=%d  %s  %s\n", match.Incident.Ref.StoreID, match.Incident.Ref.IncidentID, match.Score, matchedSignalText(match.MatchedSignals), match.Incident.Title)
				}
			})
		},
	}
}

func incidentMergeCommand() *cobra.Command {
	command := &cobra.Command{
		Use: "merge <duplicate-id>", Short: "Merge a duplicate incident into a survivor", Args: cobra.ExactArgs(1), SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			intoText, _ := cmd.Flags().GetString("into")
			if intoText == "" {
				return Exit("--into is required", exitCodeUsage)
			}
			cli, source, err := newIncidentCLIRef(cmd, args[0])
			if err != nil {
				return incidentCommandError(err)
			}
			into, err := cli.resolveRef(intoText)
			if err != nil {
				return err
			}
			request := apicontract.IncidentMergeRequest{IncidentScope: cli.scope, MutationID: incidentMutationID(cmd), Source: source, Into: into}
			var response apicontract.IncidentMergeResponse
			raw, err := cli.post(cmd.Context(), incidentPath(source)+"/merge", request, &response)
			if err != nil {
				return incidentCommandError(err)
			}
			return writeIncidentOutput(cmd, raw, response, func(w io.Writer) {
				_, _ = fmt.Fprintf(w, "%s/%s merged into %s/%s\n", response.Source.Ref.StoreID, response.Source.Ref.IncidentID, response.Into.Ref.StoreID, response.Into.Ref.IncidentID)
			})
		},
	}
	command.Flags().String("into", "", "Surviving incident ID")
	command.Flags().String(incidentMutationFlag, "", "Idempotency key (generated when omitted)")
	return command
}

func incidentEventsCommand(watch bool) *cobra.Command {
	name, short := "events [id]", "Read currently available incident events"
	if watch {
		name, short = "watch [id]", "Watch the server incident event stream"
	}
	command := &cobra.Command{
		Use: name, Short: short, Args: cobra.MaximumNArgs(1), SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cli, err := newIncidentCLI(cmd)
			if err != nil {
				return incidentCommandError(err)
			}
			path := "/datatug/incidents/events"
			if len(args) == 1 {
				var ref incidents.IncidentRef
				ref, err = cli.resolveRef(args[0])
				if err == nil {
					path = incidentPath(ref) + "/events"
				}
			}
			if err != nil {
				return err
			}
			query := cli.values()
			since, _ := cmd.Flags().GetString(incidentSinceFlag)
			if since != "" {
				query.Set("since", since)
			}
			if !watch {
				query.Set("follow", "false")
			}
			asJSON, formatErr := incidentJSONStream(cmd)
			if formatErr != nil {
				return formatErr
			}
			if err := cli.stream(cmd.Context(), path+"?"+query.Encode(), cmd.OutOrStdout(), cmd.ErrOrStderr(), asJSON); err != nil {
				return incidentCommandError(err)
			}
			return nil
		},
	}
	command.Flags().String(incidentSinceFlag, "", "Resume after an opaque event cursor")
	return command
}

func newIncidentCLI(cmd *cobra.Command) (incidentCLI, error) {
	client, scope, err := newExecutionClient(cmd)
	if err != nil {
		return incidentCLI{}, err
	}
	if scope.StoreID == "" {
		scope.StoreID = scope.Project
	}
	return incidentCLI{client: client, scope: apicontract.IncidentScope{
		StoreID: scope.StoreID, Project: scope.Project, Environment: scope.Environment, SecurityContextID: scope.SecurityContextID,
	}}, nil
}

func newIncidentCLIRef(cmd *cobra.Command, value string) (incidentCLI, incidents.IncidentRef, error) {
	cli, err := newIncidentCLI(cmd)
	if err != nil {
		return incidentCLI{}, incidents.IncidentRef{}, err
	}
	ref, err := cli.resolveRef(value)
	return cli, ref, err
}

func (c incidentCLI) resolveRef(value string) (incidents.IncidentRef, error) {
	storeID, incidentID := c.scope.StoreID, value
	if before, after, found := strings.Cut(value, "/"); found {
		storeID, incidentID = before, after
		if storeID != c.scope.StoreID {
			return incidents.IncidentRef{}, Exit("qualified incident store does not match --store", exitCodeUsage)
		}
	}
	ref := incidents.IncidentRef{StoreID: storeID, IncidentID: incidentID}
	if err := ref.Validate(); err != nil {
		return incidents.IncidentRef{}, Exit("invalid incident id", exitCodeUsage)
	}
	return ref, nil
}

func (c incidentCLI) values() url.Values {
	return url.Values{"storeId": {c.scope.StoreID}, "project": {c.scope.Project}, "environment": {c.scope.Environment}, "securityContextId": {c.scope.SecurityContextID}}
}

func (c incidentCLI) post(ctx context.Context, path string, input, target any) ([]byte, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	return c.do(ctx, http.MethodPost, path, bytes.NewReader(body), target)
}

func (c incidentCLI) do(ctx context.Context, method, path string, body io.Reader, target any) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, method, c.client.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.client.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("DataTug agent request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, incidentResponseError(response.StatusCode, raw)
	}
	if target != nil {
		if err := apicontract.DecodeStrict(raw, target); err != nil {
			return nil, fmt.Errorf("decode DataTug agent response: %w", err)
		}
	}
	return raw, nil
}

func (c incidentCLI) stream(ctx context.Context, path string, stdout, stderr io.Writer, asJSON bool) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.client.baseURL+path, nil)
	if err != nil {
		return err
	}
	httpClient := *c.client.client
	httpClient.Timeout = 0
	response, err := httpClient.Do(request)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil
		}
		return fmt.Errorf("DataTug agent request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, 8<<20))
		if readErr != nil {
			return readErr
		}
		return incidentResponseError(response.StatusCode, raw)
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	var cursor incidents.EventCursor
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var item incidents.StreamItem
		if err := apicontract.DecodeStrict(line, &item); err != nil {
			return fmt.Errorf("decode DataTug incident stream: %w", err)
		}
		if err := item.Validate(); err != nil {
			return fmt.Errorf("validate DataTug incident stream: %w", err)
		}
		cursor = item.Cursor
		if asJSON {
			if _, err := stdout.Write(append(line, '\n')); err != nil {
				return err
			}
		} else {
			writeIncidentEventLine(stdout, item.Event)
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(ctx.Err(), context.Canceled) {
		return err
	}
	if cursor != "" {
		_, err = fmt.Fprintln(stderr, cursor)
	}
	return err
}

func incidentResponseError(status int, raw []byte) error {
	var envelope apicontract.ErrorEnvelope
	if json.Unmarshal(raw, &envelope) == nil && envelope.Error.Message != "" {
		return fmt.Errorf("DataTug agent: %s: %s", envelope.Error.Code, envelope.Error.Message)
	}
	return fmt.Errorf("DataTug agent: HTTP %d", status)
}

func incidentCommandError(err error) error {
	var exit ExitCoder
	if errors.As(err, &exit) {
		return err
	}
	return Exit(err.Error(), 1)
}

func incidentMutationID(cmd *cobra.Command) string {
	value, _ := cmd.Flags().GetString(incidentMutationFlag)
	if value == "" {
		return uuid.NewString()
	}
	return value
}

func incidentJSONStream(cmd *cobra.Command) (bool, error) {
	format, _ := cmd.Flags().GetString(incidentFormatFlag)
	asJSON, _ := cmd.Flags().GetBool(executionJSONFlag)
	if asJSON && format != "" && format != "json" {
		return false, Exit("--json conflicts with --format "+format, exitCodeUsage)
	}
	if asJSON {
		format = "json"
	}
	if format == "" {
		format = "grid"
	}
	if format == "yaml" {
		return false, Exit("event streams support only grid or json output", exitCodeUsage)
	}
	if format != "grid" && format != "json" {
		return false, Exit("--format must be grid or json for event streams", exitCodeUsage)
	}
	return format == "json", nil
}

func incidentOutputFormat(cmd *cobra.Command) (string, error) {
	format, _ := cmd.Flags().GetString(incidentFormatFlag)
	asJSON, _ := cmd.Flags().GetBool(executionJSONFlag)
	if asJSON && format != "" && format != "json" {
		return "", Exit("--json conflicts with --format "+format, exitCodeUsage)
	}
	if asJSON {
		return "json", nil
	}
	if format == "" {
		if output, ok := cmd.OutOrStdout().(*os.File); ok {
			if info, err := output.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
				return "grid", nil
			}
		}
		return "json", nil
	}
	switch format {
	case "grid", "json", "yaml":
		return format, nil
	default:
		return "", Exit("--format must be grid, json, or yaml", exitCodeUsage)
	}
}

func writeIncidentOutput(cmd *cobra.Command, raw []byte, value any, grid func(io.Writer)) error {
	format, err := incidentOutputFormat(cmd)
	if err != nil {
		return err
	}
	switch format {
	case "json":
		_, err = cmd.OutOrStdout().Write(raw)
	case "yaml":
		var document any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err = decoder.Decode(&document); err == nil {
			var output []byte
			output, err = yaml.Marshal(document)
			if err == nil {
				_, err = cmd.OutOrStdout().Write(output)
			}
		}
	case "grid":
		grid(cmd.OutOrStdout())
	}
	return err
}

func writeIncidentGrid(w io.Writer, incident incidents.IncidentView) {
	_, _ = fmt.Fprintf(w, "%s/%s  %s\n%s\n", incident.Ref.StoreID, incident.Ref.IncidentID, incident.Status, incident.Title)
	if incident.Description != "" {
		_, _ = fmt.Fprintln(w, incident.Description)
	}
	for _, fact := range incident.CanonicalContext.Facts {
		_, _ = fmt.Fprintf(w, "%s.%s=%v\n", fact.Entity, fact.Field, fact.Value)
	}
}

func writeIncidentEventLine(w io.Writer, event incidents.Event) {
	_, _ = fmt.Fprintf(w, "%s  %s  %s\n", event.At.Format("15:04:05"), strings.ToUpper(string(event.Type)), incidentEventDetail(event))
}

func incidentEventDetail(event incidents.Event) string {
	switch event.Type {
	case incidents.EventNoteAdded:
		var payload incidents.NoteAddedPayload
		if json.Unmarshal(event.Payload, &payload) == nil {
			return payload.Body
		}
	case incidents.EventIncidentStatus:
		var payload incidents.StatusPayload
		if json.Unmarshal(event.Payload, &payload) == nil {
			return string(payload.Status)
		}
	case incidents.EventIncidentOutcome:
		var payload incidents.OutcomePayload
		if json.Unmarshal(event.Payload, &payload) == nil {
			return string(payload.Outcome)
		}
	case incidents.EventIncidentCreated:
		var payload incidents.CreatedViewPayload
		if json.Unmarshal(event.Payload, &payload) == nil {
			return payload.Title
		}
	case incidents.EventIncidentMerged:
		var payload incidents.MergedPayload
		if json.Unmarshal(event.Payload, &payload) == nil {
			return payload.Into.StoreID + "/" + payload.Into.IncidentID
		}
	}
	return event.ID
}

func incidentFactSignals(cmd *cobra.Command) ([]incidents.FactSignal, error) {
	values, _ := cmd.Flags().GetStringSlice("entity")
	facts := make([]incidents.FactSignal, 0, len(values))
	for _, value := range values {
		name, raw, found := strings.Cut(value, "=")
		entity, field, separated := strings.Cut(name, ".")
		if !found || !separated || entity == "" || field == "" || raw == "" {
			return nil, Exit("--entity must use Entity.Field=value", exitCodeUsage)
		}
		facts = append(facts, incidents.FactSignal{Entity: entity, Field: field, Value: incidentTypedValue(raw)})
	}
	return facts, nil
}

func incidentBacklinkRefs(scope apicontract.IncidentScope, cmd *cobra.Command) []incidents.ArtifactRef {
	refs := make([]incidents.ArtifactRef, 0, 3)
	for _, ref := range []struct {
		flag string
		kind incidents.RefKind
	}{{"query-ref", incidents.RefQuery}, {"check-ref", incidents.RefCheck}, {"board-ref", incidents.RefBoard}} {
		id, _ := cmd.Flags().GetString(ref.flag)
		if id == "" {
			continue
		}
		refs = append(refs, incidents.ArtifactRef{Kind: ref.kind, Artifact: &incidents.ProjectArtifactRef{
			StoreID: api.LocalStoreID, ProjectID: scope.Project, Environment: scope.Environment, ID: id,
		}})
	}
	return refs
}

func incidentTypedValue(value string) investigation.TypedValue {
	if parsed, err := strconv.ParseBool(value); err == nil {
		return investigation.NewBooleanValue(parsed)
	}
	if _, err := strconv.ParseInt(value, 10, 64); err == nil {
		return investigation.NewIntegerValue(value)
	}
	if parsed, err := strconv.ParseFloat(value, 64); err == nil {
		return investigation.NewNumberValue(parsed)
	}
	return investigation.NewStringValue(value)
}

func matchedSignalText(signals []incidents.MatchedSignal) string {
	values := make([]string, len(signals))
	for i, signal := range signals {
		values[i] = string(signal.Kind) + ":" + signal.Value
	}
	return strings.Join(values, ",")
}

func incidentPath(ref incidents.IncidentRef) string {
	return "/datatug/incidents/" + url.PathEscape(ref.IncidentID)
}
