package commands

import (
	"strings"
	"testing"
)

func TestServeDetectiveAIProfileRequiresConfiguredModel(t *testing.T) {
	for _, tc := range []struct{ name, config, profile, want string }{
		{name: "no AI configuration", config: "", profile: "local", want: "not configured"},
		{name: "unknown profile", config: "ai:\n  profiles:\n    other:\n      model: test-model\n", profile: "local", want: "unknown or has no model"},
		{name: "profile without model", config: "ai:\n  profiles:\n    local:\n      baseURL: http://127.0.0.1:1\n", profile: "local", want: "unknown or has no model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cov100eHome(t, tc.config)
			call, _ := cov100eSeams(t, nil)
			cmd := serveCommandArgs()
			if err := cmd.Flags().Set(serveDetectiveAIProfileFlag, tc.profile); err != nil {
				t.Fatal(err)
			}
			if err := serveCommandAction(cmd, nil); err == nil || !strings.Contains(err.Error(), tc.want) || call.caps.DetectiveAIProfile != nil {
				t.Fatalf("inadmissible AI profile reached server: err=%v caps=%+v", err, call.caps)
			}
		})
	}
}

func TestServeDetectiveAIProfilePassesConfiguredProviderToAgent(t *testing.T) {
	cov100eHome(t, "ai:\n  profiles:\n    local:\n      model: test-model\n      baseURL: http://127.0.0.1:1\n")
	call, _ := cov100eSeams(t, nil)
	cmd := serveCommandArgs()
	if err := cmd.Flags().Set(serveDetectiveAIProfileFlag, "local"); err != nil {
		t.Fatal(err)
	}
	if err := serveCommandAction(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if call.caps.DetectiveAIProfile == nil || call.caps.DetectiveAIProfile.Model != "test-model" {
		t.Fatalf("configured model did not reach agent capability: %+v", call.caps.DetectiveAIProfile)
	}
}
