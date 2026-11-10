// Package callercontext names the AI harness (and its session) a DataTug
// command runs under, from the harness's own environment, so local records
// such as policy exception proposals and their audit events can say who
// proposed or used what.
//
// It is self-reported: any process can set these variables. It never grants
// authority and is never sent to telemetry. Besides attribution it may only
// make a read stricter: a detected agent is refused, rather than warned
// about, columns that read-time suggestions mark sensitive. Hiding the
// harness drops a caller to the owner's baseline, never above it.
package callercontext

import (
	"regexp"
	"strings"
)

// Explicit overrides any harness, or the person, may set; they take
// precedence over everything detected.
const (
	HarnessEnv = "DATATUG_CALLER_HARNESS"
	SessionEnv = "DATATUG_CALLER_SESSION"
	// AIAgentEnv is the generic agent marker some harnesses set:
	// <name>[_<version with dashes>[_agent]] (Claude Code sets
	// claude-code_2-1-317_agent; OpenCode sets opencode).
	AIAgentEnv = "AI_AGENT"
)

// Sources of a detected caller.
const (
	SourceOverride   = "override"
	SourceHarnessEnv = "harness-env"
	SourceAIAgent    = "ai-agent"
)

// Caller is who a command says it runs for. Empty fields are unknown.
type Caller struct {
	Harness        string `json:"harness,omitempty"`
	HarnessVersion string `json:"harnessVersion,omitempty"`
	Session        string `json:"session,omitempty"`
	// Source says where the first known field came from: override,
	// harness-env or ai-agent.
	Source string `json:"source,omitempty"`
}

// Empty reports whether nothing is known.
func (c Caller) Empty() bool {
	return c.Harness == "" && c.HarnessVersion == "" && c.Session == ""
}

// Harness is one harness the detector knows, read from its own environment.
type Harness struct {
	Name string
	// Marker marks a process the harness runs: set to MarkerValue, or to any
	// non-empty value when MarkerValue is "".
	Marker, MarkerValue string
	// SessionVars hold the session id, first non-empty wins.
	SessionVars []string
	// VersionVar holds the version as is; AIAgentVersion reads it from
	// AI_AGENT instead.
	VersionVar     string
	AIAgentVersion bool
}

// Harnesses are the known harnesses. They are mutually exclusive: when
// several markers are set (nested harnesses) the one with a session wins,
// and a tie goes to the harness AI_AGENT names, else the first listed.
var Harnesses = []Harness{
	// Claude Code: CLAUDECODE=1 and CLAUDE_CODE_SESSION_ID, observed in a live
	// session; AI_AGENT=claude-code_<maj>-<min>-<patch>_agent.
	{Name: "claude-code", Marker: "CLAUDECODE", MarkerValue: "1", SessionVars: []string{"CLAUDE_CODE_SESSION_ID"}, AIAgentVersion: true},
	// Codex: openai/codex codex-rs/core/src/exec_env.rs (CODEX_SESSION_ID,
	// CODEX_VERSION) and codex-rs/protocol/src/shell_environment.rs
	// (CODEX_THREAD_ID). CODEX_SANDBOX is not a marker: it is set only under
	// the macOS seatbelt.
	{Name: "codex", Marker: "CODEX_THREAD_ID", SessionVars: []string{"CODEX_SESSION_ID", "CODEX_THREAD_ID"}, VersionVar: "CODEX_VERSION"},
	// OpenCode: anomalyco/opencode packages/core/src/tool/plugin/shell.ts
	// (OPENCODE=1, OPENCODE_SESSION_ID; also AGENT=1 and AI_AGENT=opencode).
	// No version.
	{Name: "opencode", Marker: "OPENCODE", MarkerValue: "1", SessionVars: []string{"OPENCODE_SESSION_ID"}},
	// DeepSeek Harness: deepseek-ai/deepseek-harness
	// packages/shell/shell-env/src/index.ts (DSH_SHELL=1; DSH_SESSION_ID only
	// when the call has an agent). No version.
	{Name: "deepseek-harness", Marker: "DSH_SHELL", MarkerValue: "1", SessionVars: []string{"DSH_SESSION_ID"}},
}

var (
	namePattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	versionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+-]{0,31}$`)
	sessionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

func valid(pattern *regexp.Regexp, value string) string {
	if len(value) <= 128 && pattern.MatchString(value) {
		return value
	}
	return ""
}

// parseAIAgent reads an AI_AGENT value: the name before the first "_", and
// the version between the first and the next "_" when present (dashes become
// dots). Invalid parts give nothing.
func parseAIAgent(value string) (name, version string) {
	parts := strings.Split(value, "_")
	name = valid(namePattern, parts[0])
	if name != "" && len(parts) > 1 {
		version = valid(versionPattern, strings.ReplaceAll(parts[1], "-", "."))
	}
	return name, version
}

func (h Harness) marked(get func(string) string) bool {
	value := get(h.Marker)
	if h.MarkerValue == "" {
		return value != ""
	}
	return value == h.MarkerValue
}

func (h Harness) session(get func(string) string) string {
	for _, name := range h.SessionVars {
		if value := get(name); value != "" {
			return valid(sessionPattern, value)
		}
	}
	return ""
}

// pick chooses the harness among the marked ones: one with a session first,
// a tie goes to the one AI_AGENT names, else the first listed.
func pick(marked []Harness, get func(string) string, agent string) (Harness, bool) {
	var withSession []Harness
	for _, h := range marked {
		if h.session(get) != "" {
			withSession = append(withSession, h)
		}
	}
	if len(withSession) > 0 {
		marked = withSession
	}
	if len(marked) == 0 {
		return Harness{}, false
	}
	for _, h := range marked {
		if h.Name == agent {
			return h, true
		}
	}
	return marked[0], true
}

// Detect reads the caller from the environment lookup gives: the explicit
// DATATUG_CALLER_* overrides first, then the known harness whose marker is
// set (see Harnesses for nested ones), then a generic AI_AGENT value; each
// field falls back separately. A value that does not match its conservative
// pattern (at most 128 characters) is dropped.
func Detect(lookup func(string) (string, bool)) Caller {
	get := func(name string) string {
		if name == "" {
			return ""
		}
		value, _ := lookup(name)
		return value
	}
	var c Caller
	set := func(field *string, value, source string) {
		if *field == "" && value != "" {
			*field = value
			if c.Source == "" {
				c.Source = source
			}
		}
	}
	set(&c.Harness, valid(namePattern, get(HarnessEnv)), SourceOverride)
	set(&c.Session, valid(sessionPattern, get(SessionEnv)), SourceOverride)
	agentName, agentVersion := parseAIAgent(get(AIAgentEnv))
	var marked []Harness
	for _, h := range Harnesses {
		if h.marked(get) {
			marked = append(marked, h)
		}
	}
	if h, ok := pick(marked, get, agentName); ok {
		set(&c.Harness, h.Name, SourceHarnessEnv)
		set(&c.Session, h.session(get), SourceHarnessEnv)
		version := valid(versionPattern, get(h.VersionVar))
		if h.AIAgentVersion && agentName == h.Name {
			version = agentVersion
		}
		if c.Harness == h.Name {
			set(&c.HarnessVersion, version, SourceHarnessEnv)
		}
	}
	set(&c.Harness, agentName, SourceAIAgent)
	if c.Harness == agentName {
		set(&c.HarnessVersion, agentVersion, SourceAIAgent)
	}
	return c
}

// Line is the one-line, self-reported description the apply and reject
// screens show: "claude-code 2.1.317 · session 0f88afc6… (self-reported)".
func (c Caller) Line() string {
	who := strings.TrimSpace(c.Harness + " " + c.HarnessVersion)
	if who == "" {
		who = "unknown harness"
	}
	parts := []string{who}
	if c.Session != "" {
		session := c.Session
		if len(session) > 8 {
			session = session[:8] + "…"
		}
		parts = append(parts, "session "+session)
	}
	return strings.Join(parts, " · ") + " (self-reported)"
}
