package commands

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	aws "github.com/aws/aws-sdk-go-v2/aws"
	athenasdk "github.com/dal-go/dalgo2athena"
)

func athenaBoundaryIdentity(t *testing.T) (string, athenaIdentityConfig) {
	t.Helper()
	cfg, err := decodeAthenaConfig(context.Background(), []byte(athenaConfigTestJSON))
	if err != nil {
		t.Fatal("failed to decode golden configuration")
	}
	identity := cfg.Identity
	identity.SessionTokenEnv = "ATHENA_BOUNDARY_SESSION"
	return cfg.AccountID, identity
}

func TestAthenaIdentityInvalidFactoryConfig(t *testing.T) {
	account, base := athenaBoundaryIdentity(t)
	rows := []struct {
		name    string
		account string
		mutate  func(*athenaIdentityConfig)
	}{
		{"account_11_digits", strings.Repeat("1", 11), nil},
		{"account_nondigit", "11111111111x", nil},
		{"subject_empty", account, func(v *athenaIdentityConfig) { v.Subject = "" }},
		{"subject_513_bytes", account, func(v *athenaIdentityConfig) { v.Subject = strings.Repeat("s", 513) }},
		{"generation_129_bytes", account, func(v *athenaIdentityConfig) { v.Generation = strings.Repeat("g", 129) }},
		{"generation_nul", account, func(v *athenaIdentityConfig) { v.Generation = "gen\x00one" }},
		{"access_env_empty", account, func(v *athenaIdentityConfig) { v.AccessKeyIDEnv = "" }},
		{"secret_env_separator", account, func(v *athenaIdentityConfig) { v.SecretAccessKeyEnv = "ATHENA-SECRET" }},
		{"session_env_space", account, func(v *athenaIdentityConfig) { v.SessionTokenEnv = "ATHENA TOKEN" }},
		{"access_env_4097_bytes", account, func(v *athenaIdentityConfig) { v.AccessKeyIDEnv = strings.Repeat("A", 4097) }},
	}
	for _, test := range rows {
		t.Run(test.name, func(t *testing.T) {
			identity := base
			if test.mutate != nil {
				test.mutate(&identity)
			}
			provider, err := athenaConfiguredIdentity(test.account, identity)
			if provider != nil || err != errAthenaConfig {
				t.Fatal("factory accepted invalid identity metadata")
			}
		})
	}
}

func athenaBoundaryUnsetEnv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	if err := os.Unsetenv(name); err != nil {
		t.Fatal("failed to unset synthetic environment variable")
	}
}

func TestAthenaIdentityUnsetAndEmptyAreDistinct(t *testing.T) {
	account, identity := athenaBoundaryIdentity(t)
	rows := []struct {
		name   string
		change func(*testing.T, athenaIdentityConfig)
	}{
		{"unset_access", func(t *testing.T, v athenaIdentityConfig) { athenaBoundaryUnsetEnv(t, v.AccessKeyIDEnv) }},
		{"unset_secret", func(t *testing.T, v athenaIdentityConfig) { athenaBoundaryUnsetEnv(t, v.SecretAccessKeyEnv) }},
		{"unset_session", func(t *testing.T, v athenaIdentityConfig) { athenaBoundaryUnsetEnv(t, v.SessionTokenEnv) }},
		{"empty_access", func(t *testing.T, v athenaIdentityConfig) { t.Setenv(v.AccessKeyIDEnv, "") }},
		{"empty_secret", func(t *testing.T, v athenaIdentityConfig) { t.Setenv(v.SecretAccessKeyEnv, "") }},
	}
	for _, test := range rows {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("AWS_ACCESS_KEY_ID", "AMBIENT_ACCESS")
			t.Setenv("AWS_SECRET_ACCESS_KEY", "AMBIENT_SECRET")
			t.Setenv("AWS_SESSION_TOKEN", "AMBIENT_TOKEN")
			t.Setenv(identity.AccessKeyIDEnv, "CONFIGURED_ACCESS")
			t.Setenv(identity.SecretAccessKeyEnv, "CONFIGURED_SECRET")
			t.Setenv(identity.SessionTokenEnv, "CONFIGURED_SESSION")
			test.change(t, identity)
			provider, err := athenaConfiguredIdentity(account, identity)
			if err != nil || provider == nil {
				t.Fatal("factory failed before credential lookup")
			}
			athenaRequireIdentityError(t, provider, context.Background(), errAthenaCredentials)
		})
	}
}

func athenaBoundaryCredentialField(identity athenaIdentityConfig, field, value string) (string, aws.Credentials, bool) {
	credentials := aws.Credentials{AccessKeyID: "ACCESS_BASE", SecretAccessKey: "SECRET_BASE", SessionToken: "SESSION_BASE"}
	switch field {
	case "access":
		credentials.AccessKeyID = value
		return identity.AccessKeyIDEnv, credentials, true
	case "secret":
		credentials.SecretAccessKey = value
		return identity.SecretAccessKeyEnv, credentials, true
	case "session":
		credentials.SessionToken = value
		return identity.SessionTokenEnv, credentials, true
	}
	return "", aws.Credentials{}, false
}

func TestAthenaIdentityCredentialByteBounds(t *testing.T) {
	account, identity := athenaBoundaryIdentity(t)
	rows := []struct {
		name  string
		field string
		value string
		ok    bool
	}{
		{"access_256", "access", strings.Repeat("!", 256), true},
		{"access_257", "access", strings.Repeat("!", 257), false},
		{"secret_256", "secret", strings.Repeat("~", 256), true},
		{"secret_257", "secret", strings.Repeat("~", 257), false},
		{"session_16384", "session", strings.Repeat("~", 16384), true},
		{"session_16385", "session", strings.Repeat("~", 16385), false},
		{"access_space", "access", "ACCESS ", false},
		{"secret_del", "secret", "SEC\x7fRET", false},
		{"secret_unicode", "secret", "SECRETé", false},
		{"session_crlf", "session", "TOKEN\r\nVALUE", false},
	}
	for _, test := range rows {
		t.Run(test.name, func(t *testing.T) {
			env, want, ok := athenaBoundaryCredentialField(identity, test.field, test.value)
			if !ok {
				t.Fatal("test row uses an unknown credential field")
			}
			t.Setenv(identity.AccessKeyIDEnv, "ACCESS_BASE")
			t.Setenv(identity.SecretAccessKeyEnv, "SECRET_BASE")
			t.Setenv(identity.SessionTokenEnv, "SESSION_BASE")
			t.Setenv(env, test.value)
			provider, err := athenaConfiguredIdentity(account, identity)
			if err != nil || provider == nil {
				t.Fatal("factory rejected valid credential names")
			}
			if !test.ok {
				athenaRequireIdentityError(t, provider, context.Background(), errAthenaCredentials)
				return
			}
			athenaRequireIdentity(t, provider, athenasdk.AttestedIdentity(account, identity.Subject, identity.Generation, want))
		})
	}
}

func TestAthenaIdentityCallerPriorityOnInvalidCredentials(t *testing.T) {
	account, identity := athenaBoundaryIdentity(t)
	t.Setenv(identity.AccessKeyIDEnv, "CONFIGURED_ACCESS")
	t.Setenv(identity.SecretAccessKeyEnv, "CONFIGURED_SECRET")
	t.Setenv(identity.SessionTokenEnv, "CONFIGURED_SESSION")
	provider, err := athenaConfiguredIdentity(account, identity)
	if err != nil || provider == nil {
		t.Fatal("factory failed before credential lookup")
	}
	canceled, cancel := context.WithCancelCause(context.Background())
	defer func() { cancel(nil) }()
	cancel(context.Canceled)
	expired, stop := context.WithDeadlineCause(context.Background(), time.Now().Add(-time.Second), context.DeadlineExceeded)
	defer stop()
	athenaBoundaryUnsetEnv(t, identity.AccessKeyIDEnv)
	t.Run("canceled_with_unset_access", func(t *testing.T) {
		athenaRequireIdentityError(t, provider, canceled, context.Canceled)
	})
	t.Setenv(identity.AccessKeyIDEnv, "CONFIGURED_ACCESS")
	t.Setenv(identity.SessionTokenEnv, strings.Repeat("~", 16385))
	t.Run("expired_with_oversized_session", func(t *testing.T) {
		athenaRequireIdentityError(t, provider, expired, context.DeadlineExceeded)
	})
}
