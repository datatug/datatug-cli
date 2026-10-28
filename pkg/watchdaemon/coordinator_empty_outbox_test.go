package watchdaemon

import (
	"context"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/watch"
)

type emptyDeliveryQueueStore struct {
	CoordinatorStore
	cancel context.CancelFunc
	claims int
	acks   int
}

func (s *emptyDeliveryQueueStore) ClaimNotifications(context.Context) (*watch.Delivery, error) {
	s.claims++
	// Cancellation happens inside the completed empty claim. The caller must
	// take the empty-queue branch before its cancellation-aware wait returns.
	s.cancel()
	return nil, nil
}

func (s *emptyDeliveryQueueStore) FinishNotification(context.Context, string, int64, string) error {
	s.acks++
	return nil
}

func TestWatchDeliveriesEmptyQueueStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &emptyDeliveryQueueStore{cancel: cancel}
	server := &Server{Store: store}
	server.runDeliveries(ctx, time.Hour)
	if store.claims != 1 || store.acks != 0 || ctx.Err() != context.Canceled {
		t.Fatalf("empty queue did not stop cleanly: claims=%d acknowledgements=%d context=%v", store.claims, store.acks, ctx.Err())
	}
}
