package callercontext

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func env(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func TestDetect(t *testing.T) {
	const session = "0f88afc6-1234-4abc-9def-0123456789ab"
	cc := map[string]string{"CLAUDECODE": "1", "CLAUDE_CODE_SESSION_ID": session, "AI_AGENT": "claude-code_2-1-317_agent"}
	with := func(base map[string]string, extra map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range base {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	cases := map[string]struct {
		env  map[string]string
		want Caller
	}{
		"nothing":     {nil, Caller{}},
		"claude code": {cc, Caller{Harness: "claude-code", HarnessVersion: "2.1.317", Session: session, Source: SourceHarnessEnv}},
		"claude code without AI_AGENT": {
			map[string]string{"CLAUDECODE": "1", "CLAUDE_CODE_SESSION_ID": session},
			Caller{Harness: "claude-code", Session: session, Source: SourceHarnessEnv},
		},
		"marker with another value": {map[string]string{"CLAUDECODE": "0", "CLAUDE_CODE_SESSION_ID": session}, Caller{}},
		"codex": {
			map[string]string{"CODEX_THREAD_ID": "thread-1", "CODEX_SESSION_ID": "sess-1", "CODEX_VERSION": "0.58.0"},
			Caller{Harness: "codex", HarnessVersion: "0.58.0", Session: "sess-1", Source: SourceHarnessEnv},
		},
		"codex thread as session": {
			map[string]string{"CODEX_THREAD_ID": "thread-1"},
			Caller{Harness: "codex", Session: "thread-1", Source: SourceHarnessEnv},
		},
		"codex sandbox is not a marker": {map[string]string{"CODEX_SANDBOX": "seatbelt"}, Caller{}},
		"opencode": {
			map[string]string{"OPENCODE": "1", "OPENCODE_SESSION_ID": "ses_abc", "AGENT": "1", "AI_AGENT": "opencode"},
			Caller{Harness: "opencode", Session: "ses_abc", Source: SourceHarnessEnv},
		},
		"deepseek harness without an agent": {
			map[string]string{"DSH_SHELL": "1"},
			Caller{Harness: "deepseek-harness", Source: SourceHarnessEnv},
		},
		"nested: the harness with a session wins": {
			map[string]string{"CLAUDECODE": "1", "OPENCODE": "1", "OPENCODE_SESSION_ID": "ses_inner", "AI_AGENT": "claude-code_2-1-317_agent"},
			Caller{Harness: "opencode", Session: "ses_inner", Source: SourceHarnessEnv},
		},
		"nested tie: AI_AGENT names the harness": {
			with(cc, map[string]string{"OPENCODE": "1", "OPENCODE_SESSION_ID": "ses_inner"}),
			Caller{Harness: "claude-code", HarnessVersion: "2.1.317", Session: session, Source: SourceHarnessEnv},
		},
		"nested tie without AI_AGENT: the first listed": {
			map[string]string{"DSH_SHELL": "1", "OPENCODE": "1"},
			Caller{Harness: "opencode", Source: SourceHarnessEnv},
		},
		"override wins, harness env fills the session": {
			with(cc, map[string]string{HarnessEnv: "my-harness"}),
			Caller{Harness: "my-harness", Session: session, Source: SourceOverride},
		},
		"override session only": {map[string]string{SessionEnv: "s-1"}, Caller{Session: "s-1", Source: SourceOverride}},
		"generic AI_AGENT": {
			map[string]string{"AI_AGENT": "some-agent_1-4_agent"},
			Caller{Harness: "some-agent", HarnessVersion: "1.4", Source: SourceAIAgent},
		},
		"generic AI_AGENT name only": {map[string]string{"AI_AGENT": "agentx"}, Caller{Harness: "agentx", Source: SourceAIAgent}},
		"generic AI_AGENT under another named harness keeps no version": {
			map[string]string{HarnessEnv: "codex", "AI_AGENT": "some-agent_1-4_agent"},
			Caller{Harness: "codex", Source: SourceOverride},
		},
		"invalid values are dropped": {
			map[string]string{HarnessEnv: "Bad Harness", SessionEnv: "x;rm -rf", "CLAUDECODE": "1", "CLAUDE_CODE_SESSION_ID": strings.Repeat("a", 129), "AI_AGENT": "claude-code_\x1b[31m_agent"},
			Caller{Harness: "claude-code", Source: SourceHarnessEnv},
		},
		"malformed AI_AGENT": {map[string]string{"AI_AGENT": "not an agent"}, Caller{}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, c.want, Detect(env(c.env)))
		})
	}
}

func TestLine(t *testing.T) {
	assert.Equal(t, "claude-code 2.1.317 · session 0f88afc6… (self-reported)",
		Caller{Harness: "claude-code", HarnessVersion: "2.1.317", Session: "0f88afc6-1234"}.Line())
	assert.Equal(t, "unknown harness · session short (self-reported)", Caller{Session: "short"}.Line())
	assert.True(t, Caller{Source: SourceOverride}.Empty())
	assert.False(t, Caller{HarnessVersion: "1"}.Empty())
}
