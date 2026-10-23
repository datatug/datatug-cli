package watchdaemon

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/datatug/datatug-cli/pkg/watch"
)

// RunCoordinator reconciles due schedules separately from assignment and drains
// the durable delivery outbox. A failed delivery never changes evaluation truth.
func (s *Server) RunCoordinator(ctx context.Context, interval time.Duration) error {
	if s.Store == nil {
		return errors.New("Watch coordinator storage is unavailable")
	}
	if interval <= 0 {
		interval = time.Second
	}
	deliveriesDone := make(chan struct{})
	go func() { defer close(deliveriesDone); s.runDeliveries(ctx, interval) }()
	defer func() { <-deliveriesDone }()
	for {
		if err := s.Store.Reconcile(ctx); err != nil {
			slog.Warn("watch_reconcile_failed", "code", "STORAGE_UNAVAILABLE")
		}
		if err := waitContext(ctx, interval); err != nil {
			return err
		}
	}
}

func (s *Server) runDeliveries(ctx context.Context, interval time.Duration) {
	for ctx.Err() == nil {
		for count := 0; count < 50 && ctx.Err() == nil; count++ {
			delivery, err := s.Store.ClaimNotifications(ctx)
			if err != nil {
				slog.Warn("watch_delivery_claim_failed", "code", "STORAGE_UNAVAILABLE")
				break
			}
			if delivery == nil {
				break
			}
			failure := ""
			if err = s.deliver(ctx, *delivery); err != nil {
				failure = "DELIVERY_FAILED"
			}
			if err = s.Store.FinishNotification(ctx, delivery.ID, delivery.Fence, failure); err != nil {
				slog.Warn("watch_delivery_ack_failed", "code", "STORAGE_UNAVAILABLE")
			}
		}
		if waitContext(ctx, interval) != nil {
			return
		}
	}
}

func (s *Server) notificationText(n watch.Notification) string {
	text := "A DataTug Watch condition changed. Open DataTug to view the Watch and its execution history."
	configured := s.ProjectUIURLs[n.Project]
	if configured == "" && s.Auth != nil {
		projects := map[string]bool{}
		for _, entry := range s.Auth.entries {
			projects[entry.credential.Project] = true
		}
		if len(projects) == 1 && projects[n.Project] {
			configured = s.PublicUIURL
		}
	}
	base, err := url.Parse(configured)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return text
	}
	if n.WatchID == "" || strings.ContainsAny(n.WatchID, "/\\?#\r\n") {
		return text
	}
	// PublicUIURL is the operator's exact project-page URL. Construct its Watch
	// child route ourselves; never forward a URL, title or raw error from data.
	base.Path = strings.TrimRight(base.Path, "/") + "/watch/" + n.WatchID
	return text + "\n\n" + base.String()
}
func (s *Server) deliver(ctx context.Context, d watch.Delivery) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	body := s.notificationText(d.Notification)
	switch d.Channel {
	case "in_app":
		return nil // durable inbox is materialized by the state owner
	case "email":
		if s.Email != nil {
			return s.Email.Send(ctx, EmailMessage{ID: d.ID, To: d.Email, Subject: "DataTug Watch update", Body: body})
		}
	case "sms":
		if s.SMS != nil {
			return s.SMS.Send(ctx, SMSMessage{ID: d.ID, To: d.Phone, Body: body})
		}
	case "telegram":
		if s.Telegram != nil {
			return s.Telegram.Send(ctx, TelegramMessage{ID: d.ID, ChatID: d.TelegramChatID, Body: body, DeliveryCapability: d.TelegramDeliveryCapability})
		}
	}
	return errors.New("Watch notification provider is unavailable")
}
