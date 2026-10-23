// Package watch implements project Watches over DALGO transactional state.
package watch

import (
	"context"
	"errors"
	"time"

	detective "github.com/datatug/datatug-core/pkg/detective"
)

var (
	ErrForbidden  = errors.New("watch access forbidden")
	ErrNotFound   = errors.New("watch resource not found")
	ErrConflict   = errors.New("watch version conflict")
	ErrStaleLease = errors.New("watch lease expired or superseded")
	ErrInvalid    = errors.New("invalid watch request")
)

type Scope struct {
	Namespace   string `json:"namespace"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
}

// Actor is supplied by the authenticated transport, never decoded from a request.
type Actor struct {
	ID           string
	Email        string
	Scope        Scope
	SharedEditor bool
	AccountID    string
	Tier         string
	Phone        string
}

// RunnerIdentity contains only grants verified by the coordinator transport.
type RunnerIdentity struct {
	ID           string
	Scope        Scope
	Capabilities []string
}

type Schedule struct {
	IntervalSeconds int `json:"intervalSeconds"`
}
type NotificationPolicy struct {
	CooldownSeconds int  `json:"cooldownSeconds,omitempty"`
	Recovery        bool `json:"recovery"`
}
type Definition struct {
	SourceFingerprint string `json:"sourceFingerprint,omitempty"`
	Scope
	ID                 string                          `json:"id"`
	Version            int64                           `json:"version"`
	Title              string                          `json:"title"`
	Owner              string                          `json:"owner"`
	Visibility         string                          `json:"visibility"`
	CheckID            string                          `json:"checkId"`
	CheckVersion       string                          `json:"checkVersion,omitempty"`
	Inputs             map[string]detective.InputValue `json:"inputs,omitempty"`
	Schedule           Schedule                        `json:"schedule"`
	Trigger            string                          `json:"trigger"`            // becomes_true, becomes_false, changes
	InitialObservation string                          `json:"initialObservation"` // notify, baseline
	Mode               string                          `json:"mode"`               // one_shot, continuous
	Lifecycle          string                          `json:"lifecycle"`          // enabled, paused, completed, deleted
	Enabled            bool                            `json:"enabled"`
	Requirements       []string                        `json:"requirements,omitempty"`
	NotificationPolicy NotificationPolicy              `json:"notificationPolicy"`
	CreatedAt          time.Time                       `json:"createdAt"`
	UpdatedAt          time.Time                       `json:"updatedAt"`
}

// DefinitionRepository is durable project storage, independent of job/lease retention.
// This deployment supplies authenticated DALGO storage; personal definitions are
// never exported to a project's shared Git files.
type DefinitionRepository interface {
	ListDefinitions(context.Context, Actor) ([]Definition, error)
	GetDefinition(context.Context, Actor, string) (Definition, error)
	PutDefinition(context.Context, Actor, Definition, string, int64) (Definition, error)
}
type Subscription struct {
	AccountID    string   `json:"accountId,omitempty"`
	Tier         string   `json:"tier,omitempty"`
	Phone        string   `json:"phone,omitempty"`
	WatchID      string   `json:"watchId"`
	UserID       string   `json:"userId"`
	Channels     []string `json:"channels"`
	NotifyOn     []string `json:"notifyOn,omitempty"`
	Email        string   `json:"email,omitempty"`
	Unsubscribed bool     `json:"unsubscribed,omitempty"`
}
type Observation struct {
	SourceFingerprint string    `json:"sourceFingerprint,omitempty"`
	WatchID           string    `json:"watchId"`
	Value             string    `json:"value"` // true, false, unknown, never
	LastKnown         string    `json:"lastKnown"`
	LastRunID         string    `json:"lastRunId,omitempty"`
	LastObservedAt    time.Time `json:"lastObservedAt,omitempty"`
	LastNotifiedAt    time.Time `json:"lastNotifiedAt,omitempty"`
	NextEvaluation    time.Time `json:"nextEvaluation"`
	Availability      string    `json:"availability"`
}
type Run struct {
	Scope
	ID                string    `json:"id"`
	WatchID           string    `json:"watchId"`
	DefinitionVersion int64     `json:"definitionVersion"`
	DueAt             time.Time `json:"dueAt"`
	State             string    `json:"state"` // due, running, completed, retry, failed, cancelled
	RunnerID          string    `json:"runnerId,omitempty"`
	Fence             int64     `json:"fence"`
	Attempts          int       `json:"attempts"`
	Deadline          time.Time `json:"deadline,omitempty"`
	CompletionHash    string    `json:"completionHash,omitempty"`
	LeaseUntil        time.Time `json:"leaseUntil,omitempty"`
	RetryAt           time.Time `json:"retryAt,omitempty"`
	ExecutionStatus   string    `json:"executionStatus,omitempty"`
	Outcome           string    `json:"outcome,omitempty"`
	Severity          string    `json:"severity,omitempty"`
	DurationMS        int64     `json:"durationMs,omitempty"`
	CheckRunRef       string    `json:"checkRunRef,omitempty"`
	FailureCode       string    `json:"failureCode,omitempty"`
	FinishedAt        time.Time `json:"finishedAt,omitempty"`
}
type Claim struct {
	ClaimID    string     `json:"claimId"`
	RunID      string     `json:"runId"`
	Fence      int64      `json:"fence"`
	LeaseUntil time.Time  `json:"leaseUntil"`
	Definition Definition `json:"definition"`
}
type Completion struct {
	SourceFingerprint string `json:"sourceFingerprint,omitempty"`
	ClaimID           string `json:"claimId"`
	RunID             string `json:"runId"`
	Fence             int64  `json:"fence"`
	ExecutionStatus   string `json:"executionStatus"`
	Outcome           string `json:"outcome"`
	Severity          string `json:"severity,omitempty"`
	DurationMS        int64  `json:"durationMs"`
	CheckRunRef       string `json:"checkRunRef,omitempty"`
	FailureCode       string `json:"failureCode,omitempty"`
}
type Runner struct {
	Scope
	ID           string    `json:"id"`
	Capabilities []string  `json:"capabilities"`
	LastSeen     time.Time `json:"lastSeen"`
}
type Notification struct {
	Scope
	ID        string    `json:"id"`
	WatchID   string    `json:"watchId"`
	RunID     string    `json:"runId"`
	UserID    string    `json:"userId"`
	Title     string    `json:"title"`
	Event     string    `json:"event"`
	Link      string    `json:"link"`
	CreatedAt time.Time `json:"createdAt"`
	ReadAt    time.Time `json:"readAt,omitempty"`
}
type Delivery struct {
	TelegramDeliveryCapability string `json:"telegramDeliveryCapability,omitempty"`
	TelegramChatID             string `json:"telegramChatId,omitempty"`
	AccountID                  string `json:"accountId,omitempty"`
	Phone                      string `json:"phone,omitempty"`
	QuotaReserved              bool   `json:"quotaReserved"`
	Notification
	Channel     string    `json:"channel"`
	Email       string    `json:"email,omitempty"`
	State       string    `json:"state"`
	Attempts    int       `json:"attempts"`
	Fence       int64     `json:"fence"`
	LeaseUntil  time.Time `json:"leaseUntil,omitempty"`
	RetryAt     time.Time `json:"retryAt,omitempty"`
	FailureCode string    `json:"failureCode,omitempty"`
}
type AuditEvent struct {
	ID         string    `json:"id"`
	Scope      Scope     `json:"scope"`
	Actor      string    `json:"actor"`
	Action     string    `json:"action"`
	ResourceID string    `json:"resourceId"`
	At         time.Time `json:"at"`
}
type Status struct {
	Coordinator  string        `json:"coordinator"`
	Runners      []Runner      `json:"runners"`
	Observations []Observation `json:"observations"`
}
type Config struct {
	MaxWatchesPerUser   int
	MinIntervalSeconds  int
	TelegramDailyLimit  int
	TelegramMinuteLimit int
	IsPaidAccount       func(string) bool
	EmailDailyLimit     int
	SMSDailyLimit       int
	EmailMinuteLimit    int
	SMSMinuteLimit      int
	Namespace           string
	Now                 func() time.Time
	LeaseDuration       time.Duration
	RunnerTTL           time.Duration
	ExecutionTimeout    time.Duration
	MaxAttempts         int
	HistoryLimit        int
}
