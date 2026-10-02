package commands

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// The cloud decision engine forwards the user's questions and the schema's table
// and column names to a third party. A project file may only REQUEST it
// ("decision: auto" in ai/table-rules.yaml): opening a cloned repository must never
// start that forwarding. It takes effect only with the user's own consent, which
// is stored per project, outside the project, in the user's config directory, or
// with the DATATUG_AI_DECISION_PROVIDER environment variable.
const (
	consentAllow  = "allow"
	consentRefuse = "refuse"
	consentForget = "forget"
)

// chatConsentPath is where consent is stored. A seam over the config directory
// so tests never touch the real one; always the user config directory in production.
var chatConsentPath = func() (string, error) {
	dir, err := chatUserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "datatug", "decision-consent.json"), nil
}

type consentFile struct {
	// Projects maps a project's canonical directory to "allow" or "refuse". The
	// directory, not the project id inside the project file, is the identity: a
	// clone elsewhere does not inherit the consent given to another copy.
	Projects map[string]string `json:"projects"`
}

// consentKey is a project's identity for consent: its canonical absolute path.
func consentKey(projectDir string) string {
	abs, _ := filepath.Abs(projectDir) // fails only without a working directory
	abs = cmp.Or(abs, projectDir)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return abs
}

// readDecisionConsent returns the user's recorded choice for the project
// ("allow", "refuse" or "" for none) and a warning when the store cannot be read.
// An unreadable store is "no consent": the safe answer.
func readDecisionConsent(projectDir string) (choice, path, warning string) {
	path, err := chatConsentPath()
	if err != nil {
		return "", "", fmt.Sprintf("the consent store cannot be located (%v); the cloud decision engine stays off", err)
	}
	file, err := loadConsent(path)
	if err != nil {
		return "", path, fmt.Sprintf("%s is not readable (%v); the cloud decision engine stays off", path, err)
	}
	choice = file.Projects[consentKey(projectDir)]
	if choice != consentAllow && choice != consentRefuse {
		choice = ""
	}
	return choice, path, ""
}

func loadConsent(path string) (consentFile, error) {
	var file consentFile
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return file, nil
	}
	if err != nil {
		return file, errors.New("it cannot be opened")
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return consentFile{}, errors.New("it is not valid JSON")
	}
	return file, nil
}

// writeDecisionConsent records (or, for "forget", removes) the user's choice for
// the project, privately (file 0600, directory 0700).
func writeDecisionConsent(projectDir, choice string) (string, error) {
	path, err := chatConsentPath()
	if err != nil {
		return "", err
	}
	file, err := loadConsent(path)
	if err != nil {
		return path, fmt.Errorf("%s: %w; fix or delete it first", path, err)
	}
	if file.Projects == nil {
		file.Projects = map[string]string{}
	}
	if choice == consentForget {
		delete(file.Projects, consentKey(projectDir))
	} else {
		file.Projects[consentKey(projectDir)] = choice
	}
	payload, _ := json.MarshalIndent(file, "", "  ") // plain strings: cannot fail
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return path, err
	}
	return path, os.WriteFile(path, payload, 0o600)
}

// setCloudDecisionConsent handles `datatug chat --cloud-decision allow|refuse|forget`.
func setCloudDecisionConsent(projectDir, choice string) (string, error) {
	switch choice {
	case consentAllow, consentRefuse, consentForget:
	default:
		return "", fmt.Errorf("--cloud-decision must be allow, refuse or forget, not %q", choice)
	}
	path, err := writeDecisionConsent(projectDir, choice)
	if err != nil {
		return "", fmt.Errorf("record the cloud decision choice: %w", err)
	}
	return path, nil
}
