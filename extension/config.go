package extension

import (
	"time"

	"github.com/xraph/relay"
)

// Config holds configuration for the Relay Forge extension.
// Fields can be set programmatically via ExtOption functions or loaded from
// YAML configuration files (under "extensions.relay" or "relay" keys).
type Config struct {
	// Config embeds the core relay configuration.
	relay.Config `json:",inline" yaml:",inline" mapstructure:",squash"`

	// BasePath is the URL prefix for all relay webhook routes (default: "/webhooks").
	BasePath string `json:"base_path" yaml:"base_path" mapstructure:"base_path"`

	// DisableRoutes disables automatic route registration with the Forge router.
	DisableRoutes bool `json:"disable_routes" yaml:"disable_routes" mapstructure:"disable_routes"`

	// DisableMigrate disables automatic database migration on Register.
	DisableMigrate bool `json:"disable_migrate" yaml:"disable_migrate" mapstructure:"disable_migrate"`

	// RequireConfig requires config to be present in YAML files.
	// If true and no config is found, Register returns an error.
	RequireConfig bool `json:"-" yaml:"-"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Config:   relay.DefaultConfig(),
		BasePath: "/webhooks",
	}
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
