package memory

import (
	"context"
	"time"
)

// lockPendingCount honors telemetry deadlines without spawning a waiter goroutine.
func (s *Store) lockPendingCount(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.mu.TryRLock() {
		return nil
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if s.mu.TryRLock() {
				return nil
			}
		}
	}
}
