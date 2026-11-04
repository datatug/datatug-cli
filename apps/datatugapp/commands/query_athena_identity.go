package commands

import (
	"context"
	"errors"
	"os"
	"strings"

	aws "github.com/aws/aws-sdk-go-v2/aws"
	athenasdk "github.com/dal-go/dalgo2athena"
)

var errAthenaCredentials = errors.New("athena credentials are unavailable or invalid")

const (
	athenaMaxCredentialBytes   = 256
	athenaMaxSessionTokenBytes = 16384
)

func athenaConfiguredIdentity(account string, cfg athenaIdentityConfig) (athenasdk.IdentityProvider, error) {
	if !athenaAccountPattern.MatchString(account) ||
		!athenaBoundedText(cfg.Subject, athenaMaxSubjectBytes) ||
		!athenaBoundedText(cfg.Generation, athenaMaxGenerationBytes) ||
		!athenaEnv(cfg.AccessKeyIDEnv) ||
		!athenaEnv(cfg.SecretAccessKeyEnv) ||
		(cfg.SessionTokenEnv != "" && !athenaEnv(cfg.SessionTokenEnv)) {
		return nil, errAthenaConfig
	}
	account = strings.Clone(account)
	subject := strings.Clone(cfg.Subject)
	generation := strings.Clone(cfg.Generation)
	accessKeyIDEnv := strings.Clone(cfg.AccessKeyIDEnv)
	secretAccessKeyEnv := strings.Clone(cfg.SecretAccessKeyEnv)
	sessionTokenEnv := strings.Clone(cfg.SessionTokenEnv)
	return func(ctx context.Context) (identity athenasdk.Identity, err error) {
		if ctx == nil {
			return athenasdk.Identity{}, errAthenaCredentials
		}
		defer func() {
			if ctxErr := athenaFresh(ctx); ctxErr != nil {
				identity = athenasdk.Identity{}
				err = ctxErr
				return
			}
			if err != nil {
				identity = athenasdk.Identity{}
			}
		}()
		if err = athenaFresh(ctx); err != nil {
			return identity, err
		}
		accessKeyID, ok := os.LookupEnv(accessKeyIDEnv)
		if !ok {
			return identity, errAthenaCredentials
		}
		if err = athenaFresh(ctx); err != nil {
			return identity, err
		}
		secretAccessKey, ok := os.LookupEnv(secretAccessKeyEnv)
		if !ok {
			return identity, errAthenaCredentials
		}
		var sessionToken string
		if sessionTokenEnv != "" {
			if err = athenaFresh(ctx); err != nil {
				return identity, err
			}
			sessionToken, ok = os.LookupEnv(sessionTokenEnv)
			if !ok {
				return identity, errAthenaCredentials
			}
		}
		if !athenaCredentialValue(accessKeyID, athenaMaxCredentialBytes) ||
			!athenaCredentialValue(secretAccessKey, athenaMaxCredentialBytes) ||
			(sessionTokenEnv != "" && !athenaCredentialValue(sessionToken, athenaMaxSessionTokenBytes)) {
			return identity, errAthenaCredentials
		}
		if err = athenaFresh(ctx); err != nil {
			return identity, err
		}
		credentials := aws.Credentials{
			AccessKeyID:     strings.Clone(accessKeyID),
			SecretAccessKey: strings.Clone(secretAccessKey),
			SessionToken:    strings.Clone(sessionToken),
		}
		return athenasdk.AttestedIdentity(account, subject, generation, credentials), nil
	}, nil
}

func athenaCredentialValue(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 33 || value[i] > 126 {
			return false
		}
	}
	return true
}
