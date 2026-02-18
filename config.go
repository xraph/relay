package relay

import "time"

// Config holds the configuration for a Relay instance.
type Config struct {
	// Concurrency is the number of delivery worker goroutines.
	Concurrency int

	// PollInterval is how often the delivery engine checks for pending deliveries.
	PollInterval time.Duration

	// BatchSize is the maximum number of deliveries dequeued per poll cycle.
	BatchSize int

	// RequestTimeout is the HTTP timeout per delivery attempt.
	RequestTimeout time.Duration

	// MaxRetries is the global default for maximum delivery attempts.
	MaxRetries int

	// RetrySchedule defines the backoff intervals between retry attempts.
	RetrySchedule []time.Duration

	// ShutdownTimeout is the maximum time to wait for in-flight deliveries on shutdown.
	ShutdownTimeout time.Duration

	// CacheTTL is the TTL for the catalog's in-memory event type cache.
	// Set to 0 to disable caching.
	CacheTTL time.Duration
}

// DefaultRetrySchedule defines the default exponential backoff intervals.
var DefaultRetrySchedule = []time.Duration{
	5 * time.Second,
	30 * time.Second,
	2 * time.Minute,
	15 * time.Minute,
	2 * time.Hour,
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Concurrency:     10,
		PollInterval:    1 * time.Second,
		BatchSize:       50,
		RequestTimeout:  30 * time.Second,
		MaxRetries:      5,
		RetrySchedule:   DefaultRetrySchedule,
		ShutdownTimeout: 30 * time.Second,
		CacheTTL:        30 * time.Second,
	}
}
