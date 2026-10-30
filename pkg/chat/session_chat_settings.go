package chat

import "context"

// BrowserSettings exposes only non-secret connection identity and the TUI's
// result-retention setting for the active session.
func (c *SessionChat) BrowserSettings(ctx context.Context, sessionID string) (int, string, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeID != sessionID {
		return 0, "", "", ErrActiveSessionChanged
	}
	count, err := c.store.ResultVersionsToKeep(ctx)
	if err != nil {
		return 0, "", "", err
	}
	return count, c.store.info.Environment, c.store.info.Database, nil
}

func (c *SessionChat) SetBrowserVersions(ctx context.Context, sessionID string, count int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeID != sessionID {
		return ErrActiveSessionChanged
	}
	if err := c.store.SetResultVersionsToKeep(ctx, count); err != nil {
		return err
	}
	c.notifyChanged()
	return nil
}
