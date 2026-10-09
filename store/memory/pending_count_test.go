package memory

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPendingCountRespectsDeadlineBehindWriter(t *testing.T) {
	s := New()
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := s.CountPending(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pending count ignored context: %v", err)
	}
}

func TestPendingCountRejectsCancelledEmptyRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New().CountPending(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
