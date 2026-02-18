package extension

import (
	"time"

	"github.com/xraph/relay"
)

// Config holds configuration for the Relay Forge extension.
type Config struct {
	// Config embeds the core relay configuration.
	relay.Config

	// Prefix is the URL prefix for all relay webhook routes (default: "/webhooks").
	Prefix string `default:"/webhooks" json:"prefix"`

	// DisableRoutes disables automatic route registration with the Forge router.
	DisableRoutes bool `default:"false" json:"disable_routes"`

	// DisableMigrations disables automatic database migration on Register.
	DisableMigrations bool `default:"false" json:"disable_migrations"`
}

// ToRelayOptions converts the embedded Config into relay.Option values.
func (c Config) ToRelayOptions() []relay.Option {
	var opts []relay.Option

	if c.Concurrency > 0 {
		opts = append(opts, relay.WithConcurrency(c.Concurrency))
	}
	if c.PollInterval > 0 {
		opts = append(opts, relay.WithPollInterval(c.PollInterval))
	}
	if c.BatchSize > 0 {
		opts = append(opts, relay.WithBatchSize(c.BatchSize))
	}
	if c.RequestTimeout > 0 {
		opts = append(opts, relay.WithRequestTimeout(c.RequestTimeout))
	}
	if c.MaxRetries > 0 {
		opts = append(opts, relay.WithMaxRetries(c.MaxRetries))
	}
	if len(c.RetrySchedule) > 0 {
		opts = append(opts, relay.WithRetrySchedule(c.RetrySchedule))
	}
	if c.ShutdownTimeout > 0 {
		opts = append(opts, relay.WithShutdownTimeout(c.ShutdownTimeout))
	}
	if c.CacheTTL > time.Duration(0) {
		opts = append(opts, relay.WithCacheTTL(c.CacheTTL))
	}

	return opts
}
