package commands

import (
	"fmt"
	"os"
	"strings"

	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/spf13/cobra"
)

var getChatSettings = dtconfig.GetSettings

// resolveChatAIProfile applies a named user configuration profile to the
// chat options. Explicit command-line model settings always win over profile
// defaults; the API key itself is kept in memory only for pimodels.
func resolveChatAIProfile(options *chatOptions, cmd *cobra.Command) error {
	settings, err := getChatSettings()
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("AI profile %q is not configured: DataTug config file not found", options.ai)
		}
		return fmt.Errorf("load DataTug AI profiles: %w", err)
	}
	if settings.AI == nil {
		return fmt.Errorf("unknown AI profile %q", options.ai)
	}
	profile, ok := settings.AI.Profiles[options.ai]
	if !ok {
		return fmt.Errorf("unknown AI profile %q", options.ai)
	}
	if strings.TrimSpace(profile.Model) == "" && !cmd.Flags().Changed("model") {
		return fmt.Errorf("AI profile %q has no model; set model or pass --model", options.ai)
	}
	if !cmd.Flags().Changed("model") {
		options.model = profile.Model
	}
	if !cmd.Flags().Changed("base-url") {
		options.baseURL = profile.BaseURL
	}
	if !cmd.Flags().Changed("thinking") && profile.Thinking != "" {
		options.thinking = profile.Thinking
	}

	keyEnv := strings.TrimSpace(profile.APIKeyEnv)
	options.apiKey = ""
	if keyEnv == "" {
		return nil
	}
	key, ok := os.LookupEnv(keyEnv)
	if !ok || strings.TrimSpace(key) == "" {
		return fmt.Errorf("AI profile %q requires a non-empty %s environment variable", options.ai, keyEnv)
	}
	options.apiKey = key
	return nil
}
