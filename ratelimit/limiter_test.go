package ratelimit

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestAllow_Unlimited(t *testing.T) {
	l := New()
	for i := 0; i < 100; i++ {
		if !l.Allow("ep-1", 0) {
			t.Fatal("Allow(0) should always return true")
		}
	}
}

func TestAllow_RateLimited(t *testing.T) {
	l := New()
	epID := "ep-limited"
	rateLimit := 2

	// First two should be allowed (bucket starts full).
	if !l.Allow(epID, rateLimit) {
		t.Fatal("first call should be allowed")
	}
	if !l.Allow(epID, rateLimit) {
		t.Fatal("second call should be allowed")
	}

	// Third should be denied (bucket exhausted).
	if l.Allow(epID, rateLimit) {
		t.Fatal("third call should be denied")
	}
}

func TestAllow_Refills(t *testing.T) {
	l := New()
	epID := "ep-refill"
	rateLimit := 10 // 10 per second

	// Exhaust the bucket.
	for i := 0; i < 10; i++ {
		l.Allow(epID, rateLimit)
	}

	if l.Allow(epID, rateLimit) {
		t.Fatal("should be denied after exhausting bucket")
	}

	// Wait for refill.
	time.Sleep(200 * time.Millisecond)

	// Should be allowed again (at least 1 token refilled).
	if !l.Allow(epID, rateLimit) {
		t.Fatal("should be allowed after refill")
	}
}

func TestWait_Unlimited(t *testing.T) {
	l := New()
	ctx := context.Background()
	if err := l.Wait(ctx, "ep-1", 0); err != nil {
		t.Fatalf("Wait(0) should return nil, got %v", err)
	}
}

func TestWait_ContextCancelled(t *testing.T) {
	l := New()
	epID := "ep-wait"
	rateLimit := 1

	// Exhaust the bucket.
	l.Allow(epID, rateLimit)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := l.Wait(ctx, epID, rateLimit)
	if err == nil {
		t.Fatal("Wait should return error when context is cancelled")
	}
}

func TestWait_EventuallyAllowed(t *testing.T) {
	l := New()
	epID := "ep-eventual"
	rateLimit := 20 // 20 per second, so ~50ms per token

	// Exhaust all tokens.
	for i := 0; i < 20; i++ {
		l.Allow(epID, rateLimit)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	if err := l.Wait(ctx, epID, rateLimit); err != nil {
		t.Fatalf("Wait should succeed, got %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 20*time.Millisecond {
		t.Fatal("Wait should have blocked for at least some time")
	}
}

func TestReset(t *testing.T) {
	l := New()
	epID := "ep-reset"
	rateLimit := 1

	l.Allow(epID, rateLimit)
	if l.Allow(epID, rateLimit) {
		t.Fatal("should be denied")
	}

	l.Reset(epID)

	if !l.Allow(epID, rateLimit) {
		t.Fatal("should be allowed after reset")
	}
}

func TestConcurrentAccess(t *testing.T) {
	l := New()
	epID := "ep-concurrent"
	rateLimit := 100

	var wg sync.WaitGroup
	allowed := make(chan bool, 200)

	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			allowed <- l.Allow(epID, rateLimit)
		}()
	}

	wg.Wait()
	close(allowed)

	trueCount := 0
	for v := range allowed {
		if v {
			trueCount++
		}
	}

	// The bucket starts with 100 tokens, so at most 100 should be allowed.
	if trueCount > 100 {
		t.Fatalf("expected at most 100 allowed, got %d", trueCount)
	}
	if trueCount < 90 {
		// Due to timing/refill, we might get slightly more, but not significantly less.
		t.Fatalf("expected at least 90 allowed (timing), got %d", trueCount)
	}
}
