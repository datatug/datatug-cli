// Package watchdaemon hosts DataTug's independently composable Watch roles.
package watchdaemon

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/mail"
	"os"
	"strings"
)

// Credential is deployment-owned authority. Tokens are resolved from the
// environment at startup, never accepted from a project or a request body.
type Credential struct {
	Hosted           bool     `json:"hosted,omitempty"`
	SpaceID          string   `json:"spaceID,omitempty"`
	CanManageRunners bool     `json:"canManageRunners,omitempty"`
	TelegramBotID    string   `json:"telegramBotId,omitempty"`
	TokenEnv         string   `json:"tokenEnv"`
	Kind             string   `json:"kind"` // user or runner
	ID               string   `json:"id"`
	AccountID        string   `json:"accountId,omitempty"`
	Namespace        string   `json:"namespace"`
	Project          string   `json:"project"`
	Environment      string   `json:"environment"`
	SharedEditor     bool     `json:"sharedEditor,omitempty"`
	Capabilities     []string `json:"capabilities,omitempty"`
	Email            string   `json:"email,omitempty"`
	Tier             string   `json:"tier,omitempty"`
	Phone            string   `json:"phone,omitempty"`
}

type credentialEntry struct {
	digest     [32]byte
	credential Credential
}

type Authenticator struct{ entries []credentialEntry }

func NewAuthenticator(credentials []Credential) (*Authenticator, error) {
	a := &Authenticator{}
	identities := map[string]bool{}
	accountTiers := map[string]string{}
	botScopes := map[string]string{}
	scopeBots := map[string]string{}
	for _, c := range credentials {
		if c.AccountID == "" {
			c.AccountID = c.ID
		}
		if c.Tier == "" {
			c.Tier = "free"
		}
		if c.Tier != "free" && c.Tier != "paid" {
			return nil, errors.New("invalid Watch account tier")
		}
		if c.Email != "" {
			if _, err := mail.ParseAddress(c.Email); err != nil || strings.ContainsAny(c.Email, "\r\n") {
				return nil, errors.New("invalid Watch verified email")
			}
		}
		if c.Phone != "" && !validPhone(c.Phone) {
			return nil, errors.New("invalid Watch verified phone")
		}
		if c.ID == "" || c.Namespace == "" || c.Project == "" || c.Environment == "" || (c.Kind != "user" && c.Kind != "runner") || c.TokenEnv == "" {
			return nil, errors.New("watch credential requires identity, kind, scope and tokenEnv")
		}
		if c.TelegramBotID != "" {
			if c.Kind != "runner" || !validChatID(c.TelegramBotID) {
				return nil, errors.New("invalid Watch runner Telegram bot grant")
			}
			scope := strings.Join([]string{c.Namespace, c.Project, c.Environment}, "\x00")
			if previous, ok := botScopes[c.TelegramBotID]; ok && previous != scope {
				return nil, errors.New("watch Telegram bot belongs to another scope")
			}
			if previous, ok := scopeBots[scope]; ok && previous != c.TelegramBotID {
				return nil, errors.New("watch scope has conflicting Telegram bot grants")
			}
			botScopes[c.TelegramBotID] = scope
			scopeBots[scope] = c.TelegramBotID
		}
		key := strings.Join([]string{c.Kind, c.Namespace, c.Project, c.Environment, c.ID}, "\x00")
		if identities[key] {
			return nil, errors.New("duplicate Watch credential identity")
		}
		identities[key] = true
		if c.Kind == "runner" && (c.Tier == "paid" || c.SharedEditor || c.Email != "" || c.Phone != "") {
			return nil, errors.New("watch runner credential cannot grant user account authority")
		}
		if c.Kind == "user" {
			if tier, ok := accountTiers[c.AccountID]; ok && tier != c.Tier {
				return nil, errors.New("conflicting Watch account tiers")
			}
			accountTiers[c.AccountID] = c.Tier
		}
		token := os.Getenv(c.TokenEnv)
		if len(token) < 32 || strings.TrimSpace(token) != token {
			return nil, errors.New("watch credential environment variable must contain at least 32 characters")
		}
		digest := sha256.Sum256([]byte(token))
		for _, previous := range a.entries {
			if subtle.ConstantTimeCompare(previous.digest[:], digest[:]) == 1 {
				return nil, errors.New("watch credentials must use distinct tokens")
			}
		}
		c.Capabilities = append([]string(nil), c.Capabilities...)
		a.entries = append(a.entries, credentialEntry{digest, c})
	}
	if len(a.entries) == 0 {
		return nil, errors.New("watch authentication has no configured identities")
	}
	return a, nil
}

func (a *Authenticator) Authenticate(r *http.Request, kind string) (Credential, bool) {
	if a == nil {
		return Credential{}, false
	}
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, "Bearer ") || len(value) > 4096 {
		return Credential{}, false
	}
	token := strings.TrimPrefix(value, "Bearer ")
	digest := sha256.Sum256([]byte(token))
	var found Credential
	for _, entry := range a.entries {
		if subtle.ConstantTimeCompare(entry.digest[:], digest[:]) == 1 && entry.credential.Kind == kind {
			found = entry.credential
		}
	}
	return found, found.ID != ""
}
