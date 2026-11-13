package commands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"encoding/json"

	"github.com/zalando/go-keyring"
)

// The AI a person chose inside the workspace is remembered per project, in
// the user's config directory and never in the project: a project opened for
// the first time has no AI until its owner chooses one, so nothing a person
// agreed to for the sample is ever applied to their own data.
const (
	aiKindHosted   = "hosted"   // DataTug AI, after consent and a sign-in
	aiKindKey      = "key"      // the person's own key at a provider
	aiKindEndpoint = "endpoint" // the person's own OpenAI-compatible endpoint
	aiKindNone     = "none"     // no AI: tables and queries only
)

type aiChoice struct {
	Kind    string `json:"kind"`
	Model   string `json:"model,omitempty"`
	BaseURL string `json:"baseUrl,omitempty"`
	// KeyAccount names the OS keyring entry that holds the API key. The key
	// itself is never written to this file.
	KeyAccount string `json:"keyAccount,omitempty"`
	// ConsentAt is when the person agreed to DataTug AI for this project.
	ConsentAt string `json:"consentAt,omitempty"`
}

type aiChoiceFile struct {
	// Projects maps a project's canonical directory (see consentKey) to its choice.
	Projects map[string]aiChoice `json:"projects"`
}

// aiChoicePath is where the choices are stored; a seam like chatConsentPath.
var aiChoicePath = func() (string, error) {
	dir, err := chatUserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "datatug", "ai-choices.json"), nil
}

func loadAIChoices(path string) (aiChoiceFile, error) {
	var file aiChoiceFile
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return file, nil
	}
	if err != nil {
		return file, errors.New("it cannot be opened")
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return aiChoiceFile{}, errors.New("it is not valid JSON")
	}
	return file, nil
}

// readAIChoice returns the choice recorded for the project. An unreadable
// store is "no choice": the safe answer, as for consent.
func readAIChoice(projectDir string) (aiChoice, bool) {
	path, err := aiChoicePath()
	if err != nil {
		return aiChoice{}, false
	}
	file, err := loadAIChoices(path)
	if err != nil {
		return aiChoice{}, false
	}
	choice, ok := file.Projects[consentKey(projectDir)]
	switch choice.Kind {
	case aiKindHosted, aiKindKey, aiKindEndpoint, aiKindNone:
		return choice, ok
	}
	return aiChoice{}, false
}

// writeAIChoice records the choice for the project, under the same lock and
// atomic replace as the consent store, so the file is private and whole.
func writeAIChoice(projectDir string, choice aiChoice) error {
	path, err := aiChoicePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	unlock, err := lockConsent(path)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	defer unlock()
	file, err := loadAIChoices(path)
	if err != nil {
		return fmt.Errorf("%s: %w; fix or delete it first", path, err)
	}
	if file.Projects == nil {
		file.Projects = map[string]aiChoice{}
	}
	file.Projects[consentKey(projectDir)] = choice
	payload, _ := json.MarshalIndent(file, "", "  ") // plain strings: cannot fail
	return replaceFile(path, payload)
}

// API keys typed into the workspace live in the OS keyring only.
const aiKeyringService = "datatug-cli"

var (
	aiKeyGet = keyring.Get
	aiKeySet = keyring.Set
)

// aiRunState is what one run of the workspace keeps between its reopenings.
type aiRunState struct {
	reopen  bool   // the workspace closed to open again on the same project
	pending string // the question to put back into the composer
	chosen  bool   // the next opening follows an AI setup
	// sessionKeys holds, by keyring account, a key the keyring could not
	// store: it works until the workspace is closed.
	sessionKeys map[string]string
}

func (s *aiRunState) key(account string) (string, error) {
	if s != nil {
		if key := s.sessionKeys[account]; key != "" {
			return key, nil
		}
	}
	return aiKeyGet(aiKeyringService, account)
}

// applyProjectAIChoice points options at the AI chosen for the project in the
// workspace. It returns a line to show when the choice cannot be used.
func applyProjectAIChoice(options *chatOptions, projectDir string) string {
	choice, ok := readAIChoice(projectDir)
	if !ok {
		return ""
	}
	key := ""
	if choice.KeyAccount != "" {
		var err error
		if key, err = options.aiState.key(choice.KeyAccount); err != nil || key == "" {
			return "The API key saved for this project could not be read from the OS keyring. Type " + aiCommand + " to enter it again."
		}
	}
	options.ai, options.aiFromChoice = "", true
	switch choice.Kind {
	case aiKindHosted:
		options.model, options.baseURL, options.apiKey = "cloud", "", ""
	case aiKindNone:
		options.model, options.baseURL, options.apiKey = "none", "", ""
	default:
		options.model, options.baseURL, options.apiKey = choice.Model, choice.BaseURL, key
	}
	return ""
}
