package chat

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/strongo/aichat/ai"
	"github.com/strongo/aichat/ai/clientctx"
	"github.com/strongo/aichat/ai/cloud"
	"github.com/strongo/aichat/ai/cloudproto"
)

// InteractionReporter is the one metadata-only write needed by the chat
// client. Cloud AI requests carry the same interaction ID, so the server can
// join this report to its authoritative AI-call observations.
type InteractionReporter interface {
	ReportInteraction(context.Context, cloudproto.InteractionReport) error
}

type clientContextKey struct{}

func withClientContext(ctx context.Context, client *ai.ClientContext) context.Context {
	return context.WithValue(ctx, clientContextKey{}, client)
}

func clientContextFrom(ctx context.Context) *ai.ClientContext {
	value, _ := ctx.Value(clientContextKey{}).(*ai.ClientContext)
	return value
}

func isAIConversation(agent ContextualConversation) bool {
	_, ok := agent.(*AIConversation)
	return ok
}

// ConfigureTelemetry activates cloud reporting for this chat. It is called
// once before the UI starts. BYOK sessions have no reporter and never send
// their prompts, token counts, or local activity to the shared AI API.
func (c *SessionChat) ConfigureTelemetry(reporter InteractionReporter, base ai.ClientContext) {
	c.reporter = reporter
	c.clientContext = base
	c.telemetrySlots = make(chan struct{}, 32)
}

func (c *SessionChat) turnContext(ctx context.Context) (context.Context, string, *ai.ClientContext) {
	if c.reporter == nil {
		return ctx, "", nil
	}
	id, err := clientctx.NewUUID()
	if err != nil {
		return ctx, "", nil
	}
	client := c.clientContext
	// DataTug's durable chat session is its conversation identity. There is
	// no separate application session ID, so we do not invent one.
	client.ConversationID = c.activeID
	ctx = cloud.WithInteractionID(ctx, id)
	return withClientContext(ctx, &client), id, &client
}

func (c *SessionChat) reportTurn(ctx context.Context, interactionID string, client *ai.ClientContext, prompt string, turn Turn, turnErr error, usedAI bool) {
	if c.reporter == nil || interactionID == "" || client == nil {
		return
	}
	status := "completed"
	outcome := "answer"
	if turnErr != nil {
		status, outcome = "failed", "request_failed"
	} else if len(turn.Queries) > 0 || len(turn.Actions) > 0 {
		outcome = "action_attempted"
	}
	if ctx.Err() != nil || errors.Is(turnErr, context.Canceled) {
		status, outcome = "cancelled", "cancelled"
	}
	report := cloudproto.InteractionReport{
		InteractionID:    interactionID,
		Product:          "datatug",
		ClientContext:    client,
		UserMessageChars: utf8.RuneCountInString(prompt),
		UserMessageWords: len(strings.Fields(prompt)),
		Status:           status,
		Outcome:          outcome,
		WasCancelled:     status == "cancelled",
	}
	if !usedAI {
		report.DetectionSteps = []cloudproto.DetectionStep{{Method: "deterministic", Detector: "datatug.chat", Result: "unavailable_schema"}}
	}
	for _, query := range turn.Queries {
		actionStatus := "succeeded"
		if query.Err != nil {
			actionStatus = "failed"
		}
		report.ActionExecutions = append(report.ActionExecutions, cloudproto.ActionExecution{Action: "datatug.query.execute", Status: actionStatus})
	}
	for _, action := range turn.Actions {
		actionStatus := "succeeded"
		if action.Err != nil {
			actionStatus = "failed"
		}
		report.ActionExecutions = append(report.ActionExecutions, cloudproto.ActionExecution{Action: "datatug.workspace.action", Status: actionStatus})
	}
	c.enqueueReport(report)
}

// NewCommandInteractionID reserves correlation for a deterministic command.
// Async commands keep this ID until their completion message is observed.
func (c *SessionChat) NewCommandInteractionID() string {
	if c.reporter == nil {
		return ""
	}
	id, err := clientctx.NewUUID()
	if err != nil {
		return ""
	}
	return id
}

// ReportCommand records an observed command result. An async command calls
// this only from its completion handler; opening an overlay remains observed
// without claiming the later operation succeeded. Arguments stay local.
func (c *SessionChat) ReportCommand(interactionID, sessionID, command string, userChars, userWords int, commandErr error, completed bool) {
	if c.reporter == nil || interactionID == "" {
		return
	}
	known := map[string]bool{
		"/new": true, "/sessions": true, "/switch": true, "/rename": true,
		"/clear": true, "/delete": true, "/bucket": true, "/export": true,
		"/query": true, "/queries": true, "/http": true, "/connect": true,
		"/settings": true, "/help": true,
	}
	action := "datatug.chat.command.unknown"
	if known[command] {
		action = "datatug.chat.command." + strings.TrimPrefix(command, "/")
	}
	client := c.clientContext
	client.ConversationID = sessionID
	status := "submitted"
	actionStatus := "observed"
	outcome := "command_observed"
	if commandErr != nil {
		status, actionStatus = "failed", "failed"
		outcome = "command_failed"
	} else if completed {
		status, actionStatus = "completed", "succeeded"
		outcome = "command_completed"
	}
	c.enqueueReport(cloudproto.InteractionReport{
		InteractionID:    interactionID,
		Product:          "datatug",
		ClientContext:    &client,
		UserMessageChars: userChars,
		UserMessageWords: userWords,
		Status:           status,
		Outcome:          outcome,
		DetectionSteps:   []cloudproto.DetectionStep{{Method: "deterministic", Detector: "datatug.chat.command", Result: action, Actions: []string{action}}},
		ActionExecutions: []cloudproto.ActionExecution{{Action: action, Status: actionStatus}},
	})
}

func (c *SessionChat) enqueueReport(report cloudproto.InteractionReport) {
	select {
	case c.telemetrySlots <- struct{}{}:
	default:
		return // never let an analytics backlog slow down the user's chat
	}
	c.telemetryWG.Add(1)
	go func() {
		defer c.telemetryWG.Done()
		defer func() { <-c.telemetrySlots }()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = c.reporter.ReportInteraction(ctx, report) // best effort, outside the user-facing path
	}()
}

// WaitForTelemetry drains bounded sends when the CLI exits normally.
func (c *SessionChat) WaitForTelemetry() { c.telemetryWG.Wait() }
