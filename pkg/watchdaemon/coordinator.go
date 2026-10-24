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
		return errors.New("watch coordinator storage is unavailable")
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

// RunCoordinatorTick performs one bounded wakeup for a request-owned cloud
// instance. Durable jobs, claims and delivery acknowledgements stay in Store;
// retries or duplicate queue messages do not create another evaluation engine.
func (s *Server) RunCoordinatorTick(ctx context.Context, maxDeliveries int) error {
	if s.Store == nil || maxDeliveries < 1 || maxDeliveries > 50 {
		return watch.ErrInvalid
	}
	if err := s.Store.Reconcile(ctx); err != nil {
		return err
	}
	for count := 0; count < maxDeliveries; count++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		delivery, err := s.Store.ClaimNotifications(ctx)
		if err != nil {
			return err
		}
		if delivery == nil {
			return nil
		}
		failure := ""
		if err := s.deliver(ctx, *delivery); err != nil {
			failure = "DELIVERY_FAILED"
		}
		if err := s.Store.FinishNotification(ctx, delivery.ID, delivery.Fence, failure); err != nil {
			return err
		}
	}
	return nil
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
	if configured == "" && s.Auth != nil && s.ResolveCredential == nil {
		projects := map[string]bool{}
		for _, entry := range s.Auth.entries {
			projects[entry.credential.Project] = true
		}
		if len(projects) == 1 && projects[n.Project] {
			configured = s.PublicUIURL
		}
	}
	base, err := url.Parse(configured)
	if err != nil || ValidateEndpoint(configured) != nil {
		return text
	}
	if n.WatchID == "" || n.WatchID == "." || n.WatchID == ".." || strings.ContainsAny(n.WatchID, "/\\?#\r\n") {
		return text
	}
	// PublicUIURL is the operator's exact project-page URL. Construct its Watch
	// child route ourselves; never forward a URL, title or raw error from data.
	projectPath := strings.TrimRight(base.Path, "/")
	base.Path = projectPath + "/watch/" + n.WatchID
	query := base.Query()
	query.Set("env", n.Environment)
	base.RawQuery = query.Encode()
	text += "\n\n" + base.String()
	// Extract only a validated Check identifier from the state-generated relative
	// locator. Construct the URL from deployment authority; never forward raw URLs.
	locator, parseErr := url.Parse(n.Link)
	if parseErr == nil && locator.Scheme == "" && locator.Host == "" && locator.User == nil && locator.Fragment == "" && locator.Path == "/watch" {
		query, queryErr := url.ParseQuery(locator.RawQuery)
		check := query.Get("check")
		valid := queryErr == nil && len(query["check"]) == 1 && check != "" && len(check) <= 128 && check != "." && check != ".." && query.Get("project") == n.Project && query.Get("environment") == n.Environment && query.Get("watch") == n.WatchID
		for _, c := range check {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' && c != '-' && c != '.' {
				valid = false
			}
		}
		if valid {
			base.Path = projectPath + "/checks/" + check
			text += "\n" + base.String()
		}
	}
	return text
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

	}
	return errors.New("watch notification provider is unavailable")
}
