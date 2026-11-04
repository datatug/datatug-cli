package commands

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	aws "github.com/aws/aws-sdk-go-v2/aws"
	athenasdk "github.com/dal-go/dalgo2athena"
)

func athenaRequireIdentity(t *testing.T, provider athenasdk.IdentityProvider, want athenasdk.Identity) {
	t.Helper()
	got, err := provider(context.Background())
	if err != nil {
		t.Fatal("identity provider returned an unexpected error")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("identity provider returned an unexpected tuple")
	}
}

func athenaRequireIdentityError(t *testing.T, provider athenasdk.IdentityProvider, ctx context.Context, want error) {
	t.Helper()
	got, err := provider(ctx)
	if err != want {
		t.Fatal("identity provider returned an unexpected error")
	}
	if !reflect.DeepEqual(got, athenasdk.Identity{}) {
		t.Fatal("identity provider returned a non-zero identity")
	}
}

func TestAthenaIdentityLazyCaptureAndRotation(t *testing.T) {
	cfg, err := decodeAthenaConfig(context.Background(), []byte(athenaConfigTestJSON))
	if err != nil {
		t.Fatal("failed to decode golden configuration")
	}
	const sessionEnv = "ATHENA_TEST_SESSION"
	cfg.Identity.SessionTokenEnv = sessionEnv
	account, identity := cfg.AccountID, cfg.Identity
	accessEnv, secretEnv := identity.AccessKeyIDEnv, identity.SecretAccessKeyEnv
	t.Setenv(accessEnv, "")
	t.Setenv(secretEnv, "")
	t.Setenv(sessionEnv, "")
	provider, err := athenaConfiguredIdentity(account, identity)
	if err != nil || provider == nil {
		t.Fatal("factory failed for empty credential values")
	}
	t.Setenv(accessEnv, "ACCESS_A")
	t.Setenv(secretEnv, "SECRET_A")
	t.Setenv(sessionEnv, "TOKEN_A")
	cfg.AccountID = "222222222222"
	cfg.Identity.Subject = "other"
	cfg.Identity.Generation = "two"
	cfg.Identity.AccessKeyIDEnv = "ATHENA_OTHER_ACCESS"
	cfg.Identity.SecretAccessKeyEnv = "ATHENA_OTHER_SECRET"
	cfg.Identity.SessionTokenEnv = "ATHENA_OTHER_SESSION"
	athenaRequireIdentity(t, provider, athenasdk.AttestedIdentity("111111111111", "reader", "one", aws.Credentials{
		AccessKeyID:     "ACCESS_A",
		SecretAccessKey: "SECRET_A",
		SessionToken:    "TOKEN_A",
	}))
	t.Setenv(accessEnv, "ACCESS_B")
	t.Setenv(secretEnv, "SECRET_B")
	t.Setenv(sessionEnv, "TOKEN_B")
	athenaRequireIdentity(t, provider, athenasdk.AttestedIdentity("111111111111", "reader", "one", aws.Credentials{
		AccessKeyID:     "ACCESS_B",
		SecretAccessKey: "SECRET_B",
		SessionToken:    "TOKEN_B",
	}))
}

func TestAthenaIdentityExplicitNamesAndOptionalSession(t *testing.T) {
	cfg, err := decodeAthenaConfig(context.Background(), []byte(athenaConfigTestJSON))
	if err != nil {
		t.Fatal("failed to decode golden configuration")
	}
	account, identity := cfg.AccountID, cfg.Identity
	identity.SessionTokenEnv = ""
	t.Setenv("AWS_ACCESS_KEY_ID", "AMBIENT_ACCESS")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "AMBIENT_SECRET")
	t.Setenv("AWS_SESSION_TOKEN", "AMBIENT_TOKEN")
	t.Setenv(identity.AccessKeyIDEnv, "")
	t.Setenv(identity.SecretAccessKeyEnv, "")
	provider, err := athenaConfiguredIdentity(account, identity)
	if err != nil || provider == nil {
		t.Fatal("factory failed for empty credential values")
	}
	athenaRequireIdentityError(t, provider, context.Background(), errAthenaCredentials)
	t.Setenv(identity.AccessKeyIDEnv, "CONFIGURED_ACCESS")
	t.Setenv(identity.SecretAccessKeyEnv, "CONFIGURED_SECRET")
	athenaRequireIdentity(t, provider, athenasdk.AttestedIdentity(account, identity.Subject, identity.Generation, aws.Credentials{
		AccessKeyID:     "CONFIGURED_ACCESS",
		SecretAccessKey: "CONFIGURED_SECRET",
	}))
	sessionIdentity := identity
	sessionIdentity.SessionTokenEnv = "ATHENA_TEST_SESSION"
	t.Setenv(sessionIdentity.SessionTokenEnv, "")
	sessionProvider, err := athenaConfiguredIdentity(account, sessionIdentity)
	if err != nil || sessionProvider == nil {
		t.Fatal("factory failed for an empty session token value")
	}
	athenaRequireIdentityError(t, sessionProvider, context.Background(), errAthenaCredentials)
}

func TestAthenaIdentityCallerContext(t *testing.T) {
	cfg, err := decodeAthenaConfig(context.Background(), []byte(athenaConfigTestJSON))
	if err != nil {
		t.Fatal("failed to decode golden configuration")
	}
	identity := cfg.Identity
	identity.SessionTokenEnv = ""
	t.Setenv(identity.AccessKeyIDEnv, "CONTEXT_ACCESS")
	t.Setenv(identity.SecretAccessKeyEnv, "CONTEXT_SECRET")
	provider, err := athenaConfiguredIdentity(cfg.AccountID, identity)
	if err != nil || provider == nil {
		t.Fatal("factory failed for valid configured values")
	}
	canceled, cancel := context.WithCancelCause(context.Background())
	defer func() { cancel(nil) }()
	cancel(errors.New("PRIVATE_CAUSE"))
	expired, stop := context.WithDeadlineCause(context.Background(), time.Now().Add(-time.Second), errors.New("PRIVATE_DEADLINE_CAUSE"))
	defer stop()
	var dynamicNil context.Context
	rows := []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"canceled", canceled, context.Canceled},
		{"wrapped_canceled", athenaCancelContext{Context: canceled}, context.Canceled},
		{"expired", expired, context.DeadlineExceeded},
		{"wrapped_expired", athenaCancelContext{Context: expired}, context.DeadlineExceeded},
		{"nil", dynamicNil, errAthenaCredentials},
	}
	for _, test := range rows {
		t.Run(test.name, func(t *testing.T) {
			athenaRequireIdentityError(t, provider, test.ctx, test.want)
		})
	}
}
