package ratelimit

import (
	"context"
	"sync"
	"time"
)

// Limiter implements token bucket rate limiting per endpoint.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens    float64
	lastFill  time.Time
	rateLimit float64 // tokens per second
}

// New creates a new rate limiter.
func New() *Limiter {
	return &Limiter{
		buckets: make(map[string]*bucket),
	}
}

// Allow checks whether an endpoint is allowed to proceed.
// A rateLimit of 0 means unlimited (always returns true).
func (l *Limiter) Allow(endpointID string, rateLimit int) bool {
	if rateLimit <= 0 {
		return true
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	b := l.getOrCreateBucket(endpointID, float64(rateLimit))
	b.refill()

	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// Wait blocks until the rate limit allows the request or the context is cancelled.
// A rateLimit of 0 means unlimited (returns immediately).
func (l *Limiter) Wait(ctx context.Context, endpointID string, rateLimit int) error {
	if rateLimit <= 0 {
		return nil
	}

	for {
		if l.Allow(endpointID, rateLimit) {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(float64(time.Second) / float64(rateLimit))):
			// Try again after estimated wait.
		}
	}
}

// Reset clears the rate limit state for an endpoint.
func (l *Limiter) Reset(endpointID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, endpointID)
}

func (l *Limiter) getOrCreateBucket(endpointID string, rateLimit float64) *bucket {
	b, ok := l.buckets[endpointID]
	if !ok {
		b = &bucket{
			tokens:    rateLimit, // start full
			lastFill:  time.Now(),
			rateLimit: rateLimit,
		}
		l.buckets[endpointID] = b
	}
	return b
}

func (b *bucket) refill() {
	now := time.Now()
	elapsed := now.Sub(b.lastFill).Seconds()
	b.tokens += elapsed * b.rateLimit
	if b.tokens > b.rateLimit {
		b.tokens = b.rateLimit // cap at burst size = rate limit
	}
	b.lastFill = now
}
