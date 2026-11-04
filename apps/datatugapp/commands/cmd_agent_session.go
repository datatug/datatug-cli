package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/datatug/datatug-cli/pkg/auth/device"
	"github.com/spf13/cobra"
	"github.com/zalando/go-keyring"
)

// Agent sessions (Perimeter D29). A harness (Claude Code, Codex, ...) starts
// one agent session per harness session; the server mints a short-lived
// credential bound to the agent, the agent session and the harness session.
// The CLI keeps it in the OS keyring, keyed by a digest of the harness session
// ID, and sends it on every agent call made under that harness session.
//
// Pruning: keyrings cannot list entries on every platform, so the CLI keeps a
// small index entry (account agentSessionIndexAccount) of the session entries
// it wrote and their credential expiry. Every save and delete rewrites the
// index and deletes entries whose credential expired more than
// agentSessionPruneAfter ago; a lookup also drops such an entry. The index is
// best effort: losing it only delays pruning, never grants anything.

const (
	agentSessionService       = "datatug-cli"
	agentSessionIndexAccount  = "agent-session-index"
	agentSessionRefreshWindow = 2 * time.Minute
	agentSessionPruneAfter    = 12 * time.Hour
	harnessSessionEnv         = "DATATUG_HARNESS_SESSION"
	agentSessionHeader        = "X-DataTug-Agent-Session"
)

const agentSessionHonestLimit = "Honest limit: binding an approval to a harness session scopes honest harness use and attributes activity to that session. It is not a boundary against a shell agent running as the same OS user: such an agent can name another of its own live harness sessions (DATATUG_HARNESS_SESSION=<id>) and use that session's approvals while it is live."

const agentSessionStoreUnavailable = "agent session store (OS keyring) unavailable"

// agentCaller names the agent of an agent call and its session credential.
type agentCaller struct {
	id, session string
}

type agentSessionEntry struct {
	AgentSessionID      string    `json:"agentSessionId"`
	Credential          string    `json:"credential"`
	CredentialExpiresAt time.Time `json:"credentialExpiresAt"`
	AgentID             string    `json:"agentId"`
	SpaceID             string    `json:"spaceId"`
	ProjectID           string    `json:"projectId"`
	Harness             string    `json:"harness"`
	BaseURL             string    `json:"baseURL"`
	AuthIssuer          string    `json:"authIssuer,omitempty"`
	AuthRealm           string    `json:"authRealm,omitempty"`
	InsecureStorage     bool      `json:"insecureStorage,omitempty"`
}

func (e agentSessionEntry) clientOptions() accessClientOptions {
	return accessClientOptions{baseURL: e.BaseURL, auth: device.ProductOptions{Issuer: e.AuthIssuer, Realm: e.AuthRealm, InsecureStorage: e.InsecureStorage}}
}

// agentSessions is the keyring and environment seam of agent sessions.
type agentSessions struct {
	get    func(service, user string) (string, error)
	set    func(service, user, secret string) error
	remove func(service, user string) error
	getenv func(string) string
	now    func() time.Time
}

func defaultAgentSessions() *agentSessions {
	return &agentSessions{get: keyring.Get, set: keyring.Set, remove: keyring.Delete, getenv: os.Getenv, now: time.Now}
}

func agentSessionAccount(harnessID string) string {
	digest := sha256.Sum256([]byte(harnessID))
	return "agent-session:" + hex.EncodeToString(digest[:])
}

// validHarnessSessionID accepts the opaque IDs harnesses use (UUIDs and
// similar): 1..256 ASCII letters, digits and . _ : -.
func validHarnessSessionID(id string) bool {
	if len(id) == 0 || len(id) > 256 {
		return false
	}
	for _, c := range id {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' && c != '.' && c != ':' {
			return false
		}
	}
	return true
}

// validSessionCredential keeps a server credential safe to send as a header.
func validSessionCredential(credential string) bool {
	if len(credential) == 0 || len(credential) > 512 {
		return false
	}
	for _, c := range credential {
		if c <= ' ' || c > '~' {
			return false
		}
	}
	return true
}

func (s *agentSessions) harnessID(flag string) string {
	if flag != "" {
		return flag
	}
	return s.getenv(harnessSessionEnv)
}

func (s *agentSessions) stale(expiresAt time.Time) bool {
	return expiresAt.Add(agentSessionPruneAfter).Before(s.now())
}

// load returns the stored session of a harness session ID, or nil when there
// is none. An unreadable entry, or one whose credential expired more than
// agentSessionPruneAfter ago, is deleted and reported as none.
func (s *agentSessions) load(harnessID string) (*agentSessionEntry, error) {
	raw, err := s.get(agentSessionService, agentSessionAccount(harnessID))
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New(agentSessionStoreUnavailable)
	}
	var entry agentSessionEntry
	if json.Unmarshal([]byte(raw), &entry) != nil || entry.AgentSessionID == "" || s.stale(entry.CredentialExpiresAt) {
		return nil, s.forget(harnessID)
	}
	return &entry, nil
}

func (s *agentSessions) save(harnessID string, entry agentSessionEntry) error {
	account := agentSessionAccount(harnessID)
	data, _ := json.Marshal(entry) // Strings, a bool and a time cannot fail encoding.
	if s.set(agentSessionService, account, string(data)) != nil {
		return errors.New(agentSessionStoreUnavailable)
	}
	s.updateIndex(func(index map[string]time.Time) { index[account] = entry.CredentialExpiresAt })
	return nil
}

func (s *agentSessions) forget(harnessID string) error {
	account := agentSessionAccount(harnessID)
	err := s.remove(agentSessionService, account)
	s.updateIndex(func(index map[string]time.Time) { delete(index, account) })
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return errors.New(agentSessionStoreUnavailable)
	}
	return nil
}

// updateIndex applies change to the index, deletes the entries it lists as
// stale and writes it back. Best effort: failures only delay pruning.
func (s *agentSessions) updateIndex(change func(map[string]time.Time)) {
	var index map[string]time.Time
	if raw, err := s.get(agentSessionService, agentSessionIndexAccount); err == nil {
		_ = json.Unmarshal([]byte(raw), &index)
	}
	if index == nil {
		index = map[string]time.Time{}
	}
	change(index)
	for account, expiresAt := range index {
		if s.stale(expiresAt) {
			_ = s.remove(agentSessionService, account)
			delete(index, account)
		}
	}
	data, _ := json.Marshal(index) // A map of strings to times cannot fail encoding.
	_ = s.set(agentSessionService, agentSessionIndexAccount, string(data))
}

const (
	codeSessionEnded   = "AGENT_SESSION_ENDED"
	codeSessionInvalid = "AGENT_SESSION_INVALID"
)

// refusedWith reports a server refusal with the given code.
func refusedWith(err error, code string) bool {
	var refusal *accessRefusal
	return errors.As(err, &refusal) && refusal.code == code
}

// sessionCall posts one agent-session lifecycle call with the session's own
// credential, to the API and login the session was started with, and decodes
// the answer.
func sessionCall(ctx context.Context, dependencies accessCommandDependencies, entry *agentSessionEntry, action string, body map[string]any, answer any) error {
	raw, err := accessDo(ctx, dependencies, entry.clientOptions(), http.MethodPost, "agent/sessions/"+entry.AgentSessionID+"/"+action, nil, body, agentCaller{id: entry.AgentID, session: entry.Credential})
	if err != nil {
		return err
	}
	if json.Unmarshal(raw, answer) != nil {
		return errors.New("invalid agent session response")
	}
	return nil
}

// callWithStored makes a lifecycle call. Another process (a concurrent start)
// may have stored a newer credential since entry was read: when the call is
// refused as AGENT_SESSION_INVALID and the keyring now holds a different
// credential, it retries once with the stored entry, which then replaces
// *entry.
func (s *agentSessions) callWithStored(ctx context.Context, dependencies accessCommandDependencies, harnessID string, entry *agentSessionEntry, action string, body map[string]any, answer any) error {
	err := sessionCall(ctx, dependencies, entry, action, body, answer)
	if !refusedWith(err, codeSessionInvalid) {
		return err
	}
	stored, loadErr := s.load(harnessID)
	if loadErr != nil || stored == nil || stored.Credential == entry.Credential {
		return err
	}
	*entry = *stored
	return sessionCall(ctx, dependencies, entry, action, body, answer)
}

// forgetIfStored deletes the entry only while it still holds credential: a
// refused credential never deletes one another process stored since.
func (s *agentSessions) forgetIfStored(harnessID, credential string) error {
	stored, err := s.load(harnessID)
	if err != nil || stored == nil || stored.Credential != credential {
		return err
	}
	return s.forget(harnessID)
}

// forAgentCall resolves the agent session of one agent call: the harness
// session ID comes from --harness-session, else DATATUG_HARNESS_SESSION. With
// a stored session it fills an absent agent ID (and refuses a different one),
// refreshes a credential that expires within agentSessionRefreshWindow and
// returns the credential to send. An ID with no stored session, or no ID,
// returns "": the call runs on standing access only, and the server never
// sees a raw harness session ID on a data call.
func (s *agentSessions) forAgentCall(ctx context.Context, dependencies accessCommandDependencies, options accessClientOptions, harnessFlag string, agentID *string) (string, error) {
	if s == nil {
		return "", nil
	}
	harnessID := s.harnessID(harnessFlag)
	if harnessID == "" {
		return "", nil
	}
	if !validHarnessSessionID(harnessID) {
		return "", Exit("invalid harness session ID (--harness-session or "+harnessSessionEnv+")", exitCodeUsage)
	}
	entry, err := s.load(harnessID)
	if err != nil || entry == nil {
		return "", err
	}
	if *agentID == "" {
		*agentID = entry.AgentID
	} else if *agentID != entry.AgentID {
		return "", Exit("--agent-id does not match the agent of this harness session", exitCodeUsage)
	}
	if entry.BaseURL != options.baseURL {
		return "", Exit("this harness session's agent session was started against another API base URL", exitCodeUsage)
	}
	if entry.CredentialExpiresAt.Sub(s.now()) >= agentSessionRefreshWindow {
		return entry.Credential, nil
	}
	var answer struct {
		Credential          string    `json:"credential"`
		CredentialExpiresAt time.Time `json:"credentialExpiresAt"`
	}
	err = s.callWithStored(ctx, dependencies, harnessID, entry, "refresh", map[string]any{"spaceID": entry.SpaceID, "projectId": entry.ProjectID}, &answer)
	switch {
	case refusedWith(err, codeSessionEnded):
		_ = s.forget(harnessID)
		return "", Exit("agent session ended; start a new one with 'datatug agent session start'", exitCodeAccessDenied)
	case refusedWith(err, codeSessionInvalid):
		_ = s.forgetIfStored(harnessID, entry.Credential)
		return "", Exit("agent session credential refused; start a new one with 'datatug agent session start'", exitCodeAccessDenied)
	case err != nil:
		if s.now().Before(entry.CredentialExpiresAt) {
			return entry.Credential, nil // Still valid: refresh again on the next call.
		}
		return "", err
	case !validSessionCredential(answer.Credential) || !answer.CredentialExpiresAt.After(s.now()):
		return "", errors.New("invalid agent session response")
	}
	entry.Credential, entry.CredentialExpiresAt = answer.Credential, answer.CredentialExpiresAt
	if err = s.save(harnessID, *entry); err != nil {
		return "", err
	}
	return entry.Credential, nil
}

func agentCommand() *cobra.Command {
	return agentCommandWith(accessCommandDependencies{credential: device.ProductCredentialForRead, client: newProtectedReadHTTPClient(), sessions: defaultAgentSessions()})
}

func agentCommandWith(dependencies accessCommandDependencies) *cobra.Command {
	command := &cobra.Command{Use: "agent", Short: "Work as your registered agent", Args: cobra.NoArgs}
	session := &cobra.Command{Use: "session", Short: "Start, end and inspect the agent session of a harness session", Long: "An agent session binds an agent's 'This session' approvals to one harness session (a Claude Code conversation, for example). The DataTug plugin starts and ends it from Claude Code's session hooks; on harnesses without hooks, run 'datatug agent session start' yourself and pass --harness-session (or set " + harnessSessionEnv + ") on agent calls. The session credential stays in the OS keyring, keyed by the harness session ID, and is never printed.\n\n" + agentSessionHonestLimit, Args: cobra.NoArgs}
	session.AddCommand(agentSessionStartCommand(dependencies), agentSessionEndCommand(dependencies), agentSessionStatusCommand(dependencies))
	command.AddCommand(session)
	return command
}

func addHarnessSessionFlag(command *cobra.Command, value *string) {
	command.Flags().StringVar(value, "harness-session", "", "Harness session ID (default: $"+harnessSessionEnv+")")
}

func validHarnessName(name string) bool {
	return len(name) <= 64 && validProtectedSelector(name)
}

func agentSessionStartCommand(dependencies accessCommandDependencies) *cobra.Command {
	var options accessClientOptions
	var scope accessScopeFlags
	var harness, harnessFlag, agentID string
	command := &cobra.Command{Use: "start", Short: "Start (or rejoin) the agent session of a harness session", Long: "Starts the agent session of a harness session and keeps its credential in the OS keyring. For a live session of the same agent and harness session it returns the same agent session with a new credential; an ended session is never revived.\n\n" + agentSessionHonestLimit, Args: cobra.NoArgs, SilenceUsage: true, RunE: func(command *cobra.Command, _ []string) error {
		harnessID := dependencies.sessions.harnessID(harnessFlag)
		if !scope.valid(false) || !validHarnessName(harness) || !validHarnessSessionID(harnessID) || agentID == "" || !validProtectedAgent(true, agentID) {
			return Exit("agent session start requires valid --space, --project, --harness, --harness-session and --agent-id (ag-...)", exitCodeUsage)
		}
		raw, err := accessDo(command.Context(), dependencies, options, http.MethodPost, "agent/sessions", nil, map[string]any{"spaceID": scope.space, "projectId": scope.project, "harness": harness, "harnessSessionId": harnessID}, agentCaller{id: agentID})
		if err != nil {
			return err
		}
		var answer struct {
			AgentSessionID      string    `json:"agentSessionId"`
			Credential          string    `json:"credential"`
			CredentialExpiresAt time.Time `json:"credentialExpiresAt"`
			IdleTimeoutSeconds  int       `json:"idleTimeoutSeconds"`
			MaxEndsAt           time.Time `json:"maxEndsAt"`
		}
		if json.Unmarshal(raw, &answer) != nil || !validProtectedSelector(answer.AgentSessionID) || !validSessionCredential(answer.Credential) || !answer.CredentialExpiresAt.After(dependencies.sessions.now()) {
			return errors.New("invalid agent session response")
		}
		entry := agentSessionEntry{AgentSessionID: answer.AgentSessionID, Credential: answer.Credential, CredentialExpiresAt: answer.CredentialExpiresAt, AgentID: agentID, SpaceID: scope.space, ProjectID: scope.project, Harness: harness, BaseURL: options.baseURL, AuthIssuer: options.auth.Issuer, AuthRealm: options.auth.Realm, InsecureStorage: options.auth.InsecureStorage}
		if err = dependencies.sessions.save(harnessID, entry); err != nil {
			return err
		}
		_, err = fmt.Fprintf(command.OutOrStdout(), "Agent session %s started for %s (idle timeout %ds, ends by %s)\n", answer.AgentSessionID, agentID, answer.IdleTimeoutSeconds, answer.MaxEndsAt.UTC().Format(time.RFC3339))
		return err
	}}
	addAccessScopeFlags(command, &scope, false)
	f := command.Flags()
	f.StringVar(&harness, "harness", "", "Harness name, for example claude-code")
	addHarnessSessionFlag(command, &harnessFlag)
	f.StringVar(&agentID, "agent-id", "", "The registered agent (IDs start with ag-)")
	addAccessClientFlags(command, &options)
	return command
}

func agentSessionEndCommand(dependencies accessCommandDependencies) *cobra.Command {
	var harnessFlag, reason string
	command := &cobra.Command{Use: "end", Short: "End the agent session of a harness session and delete its credential", Long: "Ends the agent session: every 'This session' approval bound to it ends and pending ones are withdrawn. The keyring entry is deleted, also when the server reports the session already ended.", Args: cobra.NoArgs, SilenceUsage: true, RunE: func(command *cobra.Command, _ []string) error {
		harnessID := dependencies.sessions.harnessID(harnessFlag)
		if !validHarnessSessionID(harnessID) || reason != "user" && reason != "harness-ended" {
			return Exit("agent session end requires a valid --harness-session (or "+harnessSessionEnv+") and --reason user|harness-ended", exitCodeUsage)
		}
		entry, err := dependencies.sessions.load(harnessID)
		if err != nil {
			return err
		}
		if entry == nil {
			_, err = fmt.Fprintln(command.OutOrStdout(), "No agent session for this harness session")
			return err
		}
		var answer struct {
			EndedAt time.Time `json:"endedAt"`
		}
		// The server accepts an expired credential of a live session here, so
		// no refresh comes first.
		err = dependencies.sessions.callWithStored(command.Context(), dependencies, harnessID, entry, "end", map[string]any{"spaceID": entry.SpaceID, "projectId": entry.ProjectID, "reason": reason}, &answer)
		message := "Agent session %s ended\n"
		switch {
		case refusedWith(err, codeSessionInvalid):
			message = "Agent session %s could not be confirmed ended (credential refused); the server expires it\n"
		case err != nil && !refusedWith(err, codeSessionEnded):
			return err
		}
		if err = dependencies.sessions.forget(harnessID); err != nil {
			return err
		}
		_, err = fmt.Fprintf(command.OutOrStdout(), message, entry.AgentSessionID)
		return err
	}}
	addHarnessSessionFlag(command, &harnessFlag)
	command.Flags().StringVar(&reason, "reason", "user", "Why the session ends: user or harness-ended")
	return command
}

func agentSessionStatusCommand(dependencies accessCommandDependencies) *cobra.Command {
	var harnessFlag string
	command := &cobra.Command{Use: "status", Short: "Show the stored agent session of a harness session", Long: "Shows what the keyring holds for a harness session, without calling the server and without printing the credential.", Args: cobra.NoArgs, SilenceUsage: true, RunE: func(command *cobra.Command, _ []string) error {
		harnessID := dependencies.sessions.harnessID(harnessFlag)
		if !validHarnessSessionID(harnessID) {
			return Exit("agent session status requires a valid --harness-session (or "+harnessSessionEnv+")", exitCodeUsage)
		}
		entry, err := dependencies.sessions.load(harnessID)
		if err != nil {
			return err
		}
		out := command.OutOrStdout()
		if entry == nil {
			_, err = fmt.Fprintln(out, "No agent session for this harness session")
			return err
		}
		state := "valid until"
		if !dependencies.sessions.now().Before(entry.CredentialExpiresAt) {
			state = "expired at (the next agent call tries to refresh it)"
		}
		_, err = fmt.Fprintf(out, "Agent session: %s\nAgent: %s\nHarness: %s\nSpace: %s\nProject: %s\nCredential %s %s\n", entry.AgentSessionID, entry.AgentID, entry.Harness, entry.SpaceID, entry.ProjectID, state, entry.CredentialExpiresAt.UTC().Format(time.RFC3339))
		return err
	}}
	addHarnessSessionFlag(command, &harnessFlag)
	return command
}
