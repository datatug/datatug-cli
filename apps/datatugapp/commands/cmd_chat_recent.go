package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// Last-used chat options are local UI preferences, not project configuration.
// In particular, API keys are never persisted here.
type lastChatOptions struct {
	Project  string   `json:"project,omitempty"`
	Env      string   `json:"env,omitempty"`
	Database string   `json:"database,omitempty"`
	AI       string   `json:"ai,omitempty"`
	Model    string   `json:"model,omitempty"`
	BaseURL  string   `json:"base_url,omitempty"`
	Thinking string   `json:"thinking,omitempty"`
	As       string   `json:"as,omitempty"`
	Roles    []string `json:"roles,omitempty"`
	Groups   []string `json:"groups,omitempty"`
}

var lastChatOptionsPath = func() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "datatug", "chat-last.json"), nil
}

func applyLastChatOptions(cmd *cobra.Command, options *chatOptions) error {
	path, err := lastChatOptionsPath()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var saved lastChatOptions
	if err := json.Unmarshal(data, &saved); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	flags := cmd.Flags()
	// A remembered directory may have been moved or deleted. Keep the other
	// preferences, but resolve the current project's environment and database
	// independently. A bare name may be a registered project ID.
	if filepath.IsAbs(saved.Project) || strings.Contains(saved.Project, string(filepath.Separator)) {
		info, statErr := os.Stat(saved.Project)
		if statErr != nil || !info.IsDir() {
			if !flags.Changed("project") {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: saved chat project %q is unavailable; trying the current directory instead.\n", saved.Project)
			}
			saved.Project = ""
			saved.Env = ""
			saved.Database = ""
		}
	}
	if !flags.Changed("project") && saved.Project != "" {
		options.project = saved.Project
	}
	if !flags.Changed("env") && saved.Env != "" {
		options.env = saved.Env
	}
	if !flags.Changed("database") &&
		(!flags.Changed("project") || options.project == saved.Project) &&
		(!flags.Changed("env") || options.env == saved.Env) {
		options.database = saved.Database
	}
	sameAI := !flags.Changed("ai") || options.ai == saved.AI
	if !flags.Changed("ai") {
		options.ai = saved.AI
	}
	// Mark model overrides as changed so an AI profile does not replace one
	// that was explicitly chosen in the previous invocation.
	for _, field := range []struct{ flag, value string }{
		{"model", saved.Model},
		{"base-url", saved.BaseURL},
		{"thinking", saved.Thinking},
	} {
		if sameAI && field.value != "" && !flags.Changed(field.flag) {
			if err := flags.Set(field.flag, field.value); err != nil {
				return err
			}
			switch field.flag {
			case "model":
				options.model = field.value
			case "base-url":
				options.baseURL = field.value
			case "thinking":
				options.thinking = field.value
			}
		}
	}
	if !flags.Changed("as") {
		options.as = saved.As
	}
	if !flags.Changed("role") {
		options.roles = append([]string(nil), saved.Roles...)
	}
	if !flags.Changed("group") {
		options.groups = append([]string(nil), saved.Groups...)
	}
	return nil
}

func saveLastChatOptions(cmd *cobra.Command, options chatOptions) error {
	path, err := lastChatOptionsPath()
	if err != nil {
		return err
	}
	saved := lastChatOptions{
		Project: options.project, Env: options.env, Database: options.database,
		AI: options.ai, As: options.as,
		Roles: append([]string(nil), options.roles...), Groups: append([]string(nil), options.groups...),
	}
	if cmd.Flags().Changed("model") {
		saved.Model = options.model
	}
	if cmd.Flags().Changed("base-url") {
		saved.BaseURL = options.baseURL
	}
	if cmd.Flags().Changed("thinking") {
		saved.Thinking = options.thinking
	}
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".chat-last-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if _, err := temp.Write(append(data, '\n')); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}
