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

	// GroveDatabase is the name of a grove.DB registered in the DI container.
	// When set, the extension resolves this named database and auto-constructs
	// the appropriate store based on the driver type (pg/sqlite/mongo).
	// When empty and WithGroveDatabase was called, the default (unnamed) DB is used.
	GroveDatabase string `json:"grove_database" mapstructure:"grove_database" yaml:"grove_database"`

	// GroveKV is the name of a grove kv.Store registered in the DI container.
	// When set, the extension resolves this named KV store and auto-constructs
	// a Redis-backed store. When empty and WithGroveKV was called, the default
	// (unnamed) kv.Store is used.
	GroveKV string `json:"grove_kv" mapstructure:"grove_kv" yaml:"grove_kv"`

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
