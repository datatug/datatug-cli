package watch

import (
	"context"
	"testing"
)

func registerTelegramFixture(t *testing.T, s *Store, runnerID, botID, username string) (RunnerIdentity, *TelegramPollLease) {
	t.Helper()
	id := RunnerIdentity{ID: runnerID, Scope: testScope, TelegramBotID: botID, TelegramBotUsername: username}
	if err := s.RegisterRunner(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	lease, err := s.ClaimTelegramPoll(context.Background(), id)
	if err != nil || lease == nil {
		t.Fatalf("poll claim: %+v %v", lease, err)
	}
	return id, lease
}
