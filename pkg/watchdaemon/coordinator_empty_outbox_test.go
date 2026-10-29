package watchdaemon

import (
	"context"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/watch"
)

type emptyDeliveryOutbox struct {
	CoordinatorStore
	cancel                   context.CancelFunc
	claims, acknowledgements int
}

func (s *emptyDeliveryOutbox) ClaimNotifications(context.Context) (*watch.Delivery, error) {
	s.claims++
	s.cancel()
	return nil, nil
}
func (s *emptyDeliveryOutbox) FinishNotification(context.Context, string, int64, string) error {
	s.acknowledgements++
	return nil
}

func TestDeliveryWorkerStopsEmptyDrainWithoutAcknowledgingOrSpinning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &emptyDeliveryOutbox{cancel: cancel}
	(&Server{Store: store}).runDeliveries(ctx, time.Hour)
	if store.claims != 1 || store.acknowledgements != 0 || ctx.Err() != context.Canceled {
		t.Fatal("empty outbox must stop its drain without acknowledgement or another claim", store.claims, store.acknowledgements)
	}
}
