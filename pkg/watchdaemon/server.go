package watchdaemon

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/datatug/datatug-core/pkg/datatug"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/datatug/datatug-cli/pkg/watch"
)

// CoordinatorStore owns all transactional state; transports never implement
// scheduling, state transitions, visibility or lease decisions themselves.
type CoordinatorStore interface {
	Health(context.Context) error
	ListDefinitions(context.Context, watch.Actor) ([]watch.Definition, error)
	GetDefinition(context.Context, watch.Actor, string) (watch.Definition, error)
	PutDefinition(context.Context, watch.Actor, watch.Definition, string, int64) (watch.Definition, error)
	DeleteDefinition(context.Context, watch.Actor, string, string, int64) error
	SetLifecycle(context.Context, watch.Actor, string, string, string, int64) (watch.Definition, error)
	ResetBaseline(context.Context, watch.Actor, string, string, int64) (watch.Definition, error)
	Subscribe(context.Context, watch.Actor, watch.Subscription) error
	GetSubscription(context.Context, watch.Actor, string) (watch.Subscription, error)
	ListRuns(context.Context, watch.Actor, string) ([]watch.Run, error)
	ListDeliveries(context.Context, watch.Actor, string) ([]watch.Delivery, error)
	RetryNotification(context.Context, watch.Actor, string) error
	RegisterRunner(context.Context, watch.RunnerIdentity) error
	ClaimDue(context.Context, watch.RunnerIdentity) (*watch.Claim, error)
	Heartbeat(context.Context, watch.RunnerIdentity, string, int64) error
	FinishRun(context.Context, watch.RunnerIdentity, watch.Completion) (watch.Run, error)
	Inbox(context.Context, watch.Actor) ([]watch.Notification, error)
	MarkRead(context.Context, watch.Actor, string) error
	Status(context.Context, watch.Actor) (watch.Status, error)
	Reconcile(context.Context) error
	ClaimNotifications(context.Context) (*watch.Delivery, error)
	FinishNotification(context.Context, string, int64, string) error
}

type CoordinatorReference struct {
	Endpoint  string `json:"endpoint"`
	Namespace string `json:"namespace"`
}

// EntitlementChecker validates authorization for centralized/shared operational features.
type EntitlementChecker interface {
	CheckEntitlement() error
}

type Server struct {
	// ActiveDefinitions applies project discovery visibility after owning Store
	// authorization. It never affects GetDefinition, scheduling or execution.
	ActiveDefinitions func(context.Context, watch.Actor, []watch.Definition) ([]watch.Definition, error)
	// Display/discovery metadata supplied by the trusted hosted facade. It never
	// selects authentication destinations or grants transport authority.
	Coordination *CoordinatorReference
	// ResolveCredential is exclusive when configured. A cloud resolver failure
	// must never fall back to deployment tokens in Auth.
	ResolveCredential func(*http.Request, string) (Credential, error)
	Store             CoordinatorStore
	Auth              *Authenticator
	AllowedOrigins    []string
	Email             EmailSender
	SMS               SMSSender
	PublicUIURL       string
	ProjectUIURLs     map[string]string
	Limits            Limits
	Entitlement       EntitlementChecker
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
func fail(w http.ResponseWriter, err error) {
	status, code, message := http.StatusServiceUnavailable, "UNAVAILABLE", "Watch service unavailable"
	switch {
	case errors.Is(err, datatug.ErrProjectUsageIncomplete):
		status, code, message = 503, "REFERENCES_INCOMPLETE", "all retained project usage must be verified before deleting or restoring this Watch"
	case errors.Is(err, watch.ErrForbidden):
		status, code, message = 403, "FORBIDDEN", "Watch access denied"
	case errors.Is(err, watch.ErrNotFound):
		status, code, message = 404, "NOT_FOUND", "Watch resource unavailable"
	case errors.Is(err, watch.ErrConflict), errors.Is(err, watch.ErrStaleLease):
		status, code, message = 409, "CONFLICT", "Watch changed or lease expired"
	case errors.Is(err, watch.ErrCapacity):
		status, code, message = 429, "CAPACITY_REACHED", "Watch storage or subscription limit reached"
	case errors.Is(err, watch.ErrInvalid):
		status, code, message = 400, "INVALID_REQUEST", "Invalid Watch request"
	}
	reply(w, status, map[string]any{"error": apiError{code, message}})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 256*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return watch.ErrInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return watch.ErrInvalid
	}
	return nil
}

func credentialScope(c Credential) watch.Scope {
	return watch.Scope{Namespace: c.Namespace, Project: c.Project, Environment: c.Environment}
}
func credentialActor(c Credential) watch.Actor {
	return watch.Actor{ID: c.ID, AccountID: c.AccountID, Scope: credentialScope(c), SharedEditor: c.SharedEditor, Email: c.Email, Tier: c.Tier, Phone: c.Phone}
}
func credentialRunner(c Credential) watch.RunnerIdentity {
	return watch.RunnerIdentity{ID: c.ID, Scope: credentialScope(c), Capabilities: append([]string(nil), c.Capabilities...), TelegramBotID: c.TelegramBotID}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if origin := r.Header.Get("Origin"); origin != "" {
		allowed := false
		for _, o := range s.AllowedOrigins {
			if origin == o {
				allowed = true
				break
			}
		}
		if !allowed {
			fail(w, watch.ErrForbidden)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Add("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.URL.Path == "/health" && r.Method == http.MethodGet {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if s.Store == nil || s.Store.Health(ctx) != nil {
			reply(w, 503, map[string]string{"status": "unavailable"})
		} else {
			reply(w, 200, map[string]string{"status": "ready", "product": "DataTug Watch"})
		}
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/watch/")
	if path == r.URL.Path {
		reply(w, 404, map[string]any{"error": apiError{"NOT_FOUND", "Watch route unavailable"}})
		return
	}
	kind := "user"
	if strings.HasPrefix(path, "runner/") {
		kind = "runner"
	}
	var credential Credential
	var ok bool
	if s.ResolveCredential != nil {
		var err error
		credential, err = s.ResolveCredential(r, kind)
		if err != nil {
			fail(w, err)
			return
		}
		if credential.Kind != kind || credential.ID == "" || credential.Namespace == "" || credential.Project == "" || credential.Environment == "" {
			fail(w, watch.ErrForbidden)
			return
		}
		ok = true
	} else {
		credential, ok = s.Auth.Authenticate(r, kind)
	}
	if !ok {
		reply(w, 401, map[string]any{"error": apiError{"UNAUTHENTICATED", "Watch authentication required"}})
		return
	}
	for key, value := range map[string]string{"namespace": credential.Namespace, "project": credential.Project, "environment": credential.Environment} {
		if supplied := r.URL.Query().Get(key); supplied != "" && supplied != value {
			fail(w, watch.ErrForbidden)
			return
		}
	}
	if s.Store == nil {
		fail(w, errors.New("store unavailable"))
		return
	}
	if kind == "runner" {
		s.runnerRequest(w, r, path, credential)
		return
	}
	s.userRequest(w, r, path, credential)
}

func (s *Server) userRequest(w http.ResponseWriter, r *http.Request, path string, c Credential) {
	a := credentialActor(c)
	var result any
	var err error
	if s.telegramUser(w, r, path, c) {
		return
	}
	switch {
	case path == "identity" && r.Method == "GET":
		provider, providerErr := s.telegramProvider(r.Context(), a)
		err = providerErr
		limits, limitsErr := s.Limits.Resolve()
		if limitsErr != nil {
			err = limitsErr
		}
		result = map[string]any{"id": c.ID, "hosted": c.Hosted, "spaceID": c.SpaceID, "canManageRunners": c.CanManageRunners, "coordination": s.Coordination, "namespace": c.Namespace, "project": c.Project, "environment": c.Environment, "sharedEditor": c.SharedEditor, "tier": c.Tier, "telegramConnected": provider.Connected, "providers": map[string]bool{"email": s.Email != nil, "sms": s.SMS != nil}, "channels": map[string]bool{"in_app": true, "email": c.Email != "" && s.Email != nil, "sms": c.Tier == "paid" && c.Phone != "" && s.SMS != nil, "telegram": provider.BotID != ""}, "limits": limits}
	case path == "definitions" && r.Method == "GET":
		var list []watch.Definition
		list, err = s.Store.ListDefinitions(r.Context(), a)
		if err == nil && s.ActiveDefinitions != nil {
			list, err = s.ActiveDefinitions(r.Context(), a, list)
		}
		result = map[string]any{"definitions": list}
	case path == "definition" && r.Method == "GET":
		var d watch.Definition
		d, err = s.Store.GetDefinition(r.Context(), a, r.URL.Query().Get("id"))
		result = map[string]any{"definition": d}
	case path == "definition" && r.Method == "POST":
		var b struct {
			Definition      watch.Definition `json:"definition"`
			OperationID     string           `json:"operationId"`
			ExpectedVersion int64            `json:"expectedVersion"`
		}
		err = decode(w, r, &b)
		if err == nil {
			var d watch.Definition
			d, err = s.Store.PutDefinition(r.Context(), a, b.Definition, b.OperationID, b.ExpectedVersion)
			result = map[string]any{"definition": d}
		}
	case path == "lifecycle" && r.Method == "POST":
		var b struct {
			ID              string `json:"id"`
			Lifecycle       string `json:"lifecycle"`
			OperationID     string `json:"operationId"`
			ExpectedVersion int64  `json:"expectedVersion"`
		}
		err = decode(w, r, &b)
		if err == nil && b.Lifecycle == "deleted" {
			err = datatug.ErrProjectUsageIncomplete
		}
		if err == nil {
			var d watch.Definition
			d, err = s.Store.SetLifecycle(r.Context(), a, b.ID, b.Lifecycle, b.OperationID, b.ExpectedVersion)
			result = map[string]any{"definition": d}
		}
	case path == "reset" && r.Method == "POST":
		var b struct {
			ID              string `json:"id"`
			OperationID     string `json:"operationId"`
			ExpectedVersion int64  `json:"expectedVersion"`
		}
		err = decode(w, r, &b)
		if err == nil {
			var d watch.Definition
			d, err = s.Store.ResetBaseline(r.Context(), a, b.ID, b.OperationID, b.ExpectedVersion)
			result = map[string]any{"definition": d}
		}
	case path == "delete" && r.Method == "POST":
		var b struct {
			ID              string `json:"id"`
			OperationID     string `json:"operationId"`
			ExpectedVersion int64  `json:"expectedVersion"`
		}
		err = decode(w, r, &b)
		if err == nil {
			// The legacy management request has no complete project-usage proof.
			// A verified lifecycle authority may use the store primitive later;
			// public callers cannot bypass it through /delete or /lifecycle.
			err = datatug.ErrProjectUsageIncomplete
		}
		result = map[string]bool{"ok": err == nil}
	case path == "subscription" && r.Method == "POST":
		var b struct {
			WatchID      string   `json:"watchId"`
			Channels     []string `json:"channels"`
			NotifyOn     []string `json:"notifyOn,omitempty"`
			Unsubscribed bool     `json:"unsubscribed,omitempty"`
		}
		err = decode(w, r, &b)
		if err == nil {
			for _, channel := range b.Channels {
				if channel == "email" && s.Email == nil || channel == "sms" && s.SMS == nil || channel == "telegram" && !s.telegramAvailable(r.Context(), a) {
					err = watch.ErrInvalid
				}
			}
		}
		if err == nil {
			err = s.Store.Subscribe(r.Context(), a, watch.Subscription{WatchID: b.WatchID, Channels: b.Channels, NotifyOn: b.NotifyOn, Unsubscribed: b.Unsubscribed})
		}
		result = map[string]bool{"ok": err == nil}
	case path == "subscriptions" && r.Method == "GET":
		var sub watch.Subscription
		id := r.URL.Query().Get("watchId")
		sub, err = s.Store.GetSubscription(r.Context(), a, id)
		if errors.Is(err, watch.ErrNotFound) {
			if _, accessErr := s.Store.GetDefinition(r.Context(), a, id); accessErr == nil {
				sub = watch.Subscription{WatchID: id, UserID: a.ID, Channels: []string{}, Unsubscribed: true}
				err = nil
			} else {
				err = accessErr
			}
		}
		result = map[string]any{"subscription": sub}
	case path == "retry" && r.Method == "POST":
		var b struct {
			ID string `json:"id"`
		}
		err = decode(w, r, &b)
		if err == nil {
			err = s.Store.RetryNotification(r.Context(), a, b.ID)
		}
		result = map[string]bool{"ok": err == nil}
	case path == "event" && r.Method == "GET":
		store, ok := s.Store.(interface {
			GetEvent(context.Context, watch.Actor, string) (watch.Event, error)
		})
		if !ok {
			err = watch.ErrNotFound
			break
		}
		var event watch.Event
		event, err = store.GetEvent(r.Context(), a, r.URL.Query().Get("id"))
		result = map[string]any{"event": event}
	case path == "events" && r.Method == "GET":
		store, ok := s.Store.(interface {
			ListEventPage(context.Context, watch.Actor, string, string, int) ([]watch.Event, string, error)
		})
		if !ok {
			err = watch.ErrNotFound
			break
		}
		limit := 100
		if raw := r.URL.Query().Get("limit"); raw != "" {
			limit, err = strconv.Atoi(raw)
			if err != nil {
				err = watch.ErrInvalid
				break
			}
		}
		var events []watch.Event
		var next string
		events, next, err = store.ListEventPage(r.Context(), a, r.URL.Query().Get("watchId"), r.URL.Query().Get("before"), limit)
		result = map[string]any{"events": events, "nextCursor": next}
	case path == "runs" && r.Method == "GET":
		var runs []watch.Run
		runs, err = s.Store.ListRuns(r.Context(), a, r.URL.Query().Get("watchId"))
		result = map[string]any{"runs": runs}
	case path == "deliveries" && r.Method == "GET":
		var deliveries []watch.Delivery
		deliveries, err = s.Store.ListDeliveries(r.Context(), a, r.URL.Query().Get("watchId"))
		result = map[string]any{"deliveries": deliveries}
	case path == "status" && r.Method == "GET":
		result, err = s.Store.Status(r.Context(), a)
	case path == "inbox" && r.Method == "GET":
		var list []watch.Notification
		list, err = s.Store.Inbox(r.Context(), a)
		result = map[string]any{"notifications": list}
	case path == "read" && r.Method == "POST":
		var b struct {
			ID string `json:"id"`
		}
		err = decode(w, r, &b)
		if err == nil {
			err = s.Store.MarkRead(r.Context(), a, b.ID)
		}
		result = map[string]bool{"ok": err == nil}
	default:
		reply(w, 404, map[string]any{"error": apiError{"NOT_FOUND", "Watch route unavailable"}})
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, result)
}

func (s *Server) runnerRequest(w http.ResponseWriter, r *http.Request, path string, c Credential) {
	if r.Method != "POST" {
		fail(w, watch.ErrInvalid)
		return
	}
	identity := credentialRunner(c)
	if s.telegramRunner(w, r, path, identity) {
		return
	}
	var result any
	var err error
	switch path {
	case "runner/register":
		var registration runnerRegistration
		err = decode(w, r, &registration)
		if err == nil && (registration.TelegramBotID != identity.TelegramBotID || (registration.TelegramBotID != "" && !validBotUsername(registration.TelegramBotUsername)) || (registration.TelegramBotID == "" && registration.TelegramBotUsername != "")) {
			err = watch.ErrForbidden
		}
		identity.TelegramBotUsername = registration.TelegramBotUsername
		if err == nil {
			err = s.Store.RegisterRunner(r.Context(), identity)
		}
		result = map[string]bool{"ok": err == nil}
	case "runner/claim":
		var empty struct{}
		err = decode(w, r, &empty)
		if err == nil {
			var claim *watch.Claim
			claim, err = s.Store.ClaimDue(r.Context(), identity)
			result = map[string]any{"claim": claim}
		}
	case "runner/heartbeat":
		var b struct {
			ClaimID string `json:"claimId"`
			Fence   int64  `json:"fence"`
		}
		err = decode(w, r, &b)
		if err == nil {
			err = s.Store.Heartbeat(r.Context(), identity, b.ClaimID, b.Fence)
		}
		result = map[string]bool{"ok": err == nil}
	case "runner/complete":
		var b watch.Completion
		err = decode(w, r, &b)
		if err == nil {
			var run watch.Run
			run, err = s.Store.FinishRun(r.Context(), identity, b)
			result = map[string]any{"run": run}
		}
	default:
		fail(w, watch.ErrNotFound)
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, result)
}
