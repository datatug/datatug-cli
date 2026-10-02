package commands

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/datatug/datatug-cli/pkg/chat/narrowing"
	"github.com/strongo/aichat/ai/aiconfig"
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

// consentTempFile is the part of *os.File replaceFile writes through.
type consentTempFile interface {
	Name() string
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

// Seams over the consent lock's timing and the temporary file, so tests need not
// wait for the lock or break the disk. Always these values in production.
var (
	consentLockWait   = 5 * time.Second
	consentLockStale  = 30 * time.Second
	consentCreateTemp = func(dir string) (consentTempFile, error) {
		file, err := os.CreateTemp(dir, ".decision-consent-*")
		if err != nil {
			return nil, err
		}
		return file, nil
	}
	consentRename = os.Rename
)

// lockConsent takes a lock file next to the consent store, so that concurrent
// `datatug chat` processes do not lose each other's entries. The lock is a file
// created exclusively (portable, unlike flock); one older than consentLockStale
// belongs to a process that died and is taken over.
func lockConsent(path string) (func(), error) {
	lock := path + ".lock"
	deadline := time.Now().Add(consentLockWait)
	for {
		file, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = file.Close()
			return func() { _ = os.Remove(lock) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if info, statErr := os.Stat(lock); statErr == nil && time.Since(info.ModTime()) > consentLockStale {
			_ = os.Remove(lock)
			continue
		}
		if time.Now().After(deadline) {
			return nil, errors.New("another datatug process is writing it; try again")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// writeDecisionConsent records (or, for "forget", removes) the user's choice for
// the project. The store is read, changed and replaced under a lock, and replaced
// atomically (a temporary file in the same directory, synced, then renamed), so a
// reader never sees a partial file, no concurrent writer's entry is lost, and the
// file is always private (0600) whatever mode an earlier file had.
func writeDecisionConsent(projectDir, choice string) (string, error) {
	path, err := chatConsentPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return path, err
	}
	unlock, err := lockConsent(path)
	if err != nil {
		return path, fmt.Errorf("%s: %w", path, err)
	}
	defer unlock()
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
	return path, replaceFile(path, payload)
}

// replaceFile writes payload to a temporary file beside path, syncs it and renames
// it over path. The temporary file is created private (0600), so path is too.
func replaceFile(path string, payload []byte) error {
	temp, err := consentCreateTemp(filepath.Dir(path))
	if err != nil {
		return err
	}
	_, writeErr := temp.Write(payload)
	syncErr := temp.Sync()
	closeErr := temp.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		_ = os.Remove(temp.Name())
		return err
	}
	if err := consentRename(temp.Name(), path); err != nil {
		_ = os.Remove(temp.Name())
		return err
	}
	return nil
}

// consentAdvice is what `--cloud-decision <choice>` tells the user beyond
// "recorded": consent only matters when the project requests the decider (or the
// user turns it on for a session), so say what to do when it does not.
func consentAdvice(projectDir, choice string) string {
	if choice != consentAllow {
		return ""
	}
	if requested := narrowing.LoadSettings(projectDir).Decision; requested == "auto" || requested == "cloud" {
		return ""
	}
	return fmt.Sprintf("this project does not request the cloud decider (no \"decision: auto\" in %s), so the consent is recorded but nothing is turned on; to use it for a session run: %s=auto datatug chat", narrowing.SettingsFile, chatDecisionEnvPrefix+aiconfig.EnvDecision)
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
