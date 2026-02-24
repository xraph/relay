package extension

import (
	"github.com/xraph/relay"
	"github.com/xraph/relay/store"
)

// ExtOption configures the Relay Forge extension.
type ExtOption func(*Extension)

// WithStore sets the persistence backend via a relay option.
func WithStore(s store.Store) ExtOption {
	return func(e *Extension) {
		e.opts = append(e.opts, relay.WithStore(s))
	}
}

// WithBasePath sets the URL prefix for all relay webhook routes.
func WithBasePath(path string) ExtOption {
	return func(e *Extension) {
		e.config.BasePath = path
	}
}

// WithPrefix sets the URL prefix for all relay webhook routes.
// Deprecated: Use WithBasePath instead.
func WithPrefix(prefix string) ExtOption {
	return WithBasePath(prefix)
}

// WithConfig sets the extension configuration directly.
func WithConfig(cfg Config) ExtOption {
	return func(e *Extension) {
		e.config = cfg
	}
}

// WithRelayOption appends a raw relay.Option to the extension.
func WithRelayOption(opt relay.Option) ExtOption {
	return func(e *Extension) {
		e.opts = append(e.opts, opt)
	}
}

// WithDisableRoutes disables automatic route registration.
func WithDisableRoutes() ExtOption {
	return func(e *Extension) {
		e.config.DisableRoutes = true
	}
}

// WithDisableMigrate disables automatic database migration on Register.
func WithDisableMigrate() ExtOption {
	return func(e *Extension) {
		e.config.DisableMigrate = true
	}
}

// WithDisableMigrations disables automatic database migration on Register.
// Deprecated: Use WithDisableMigrate instead.
func WithDisableMigrations() ExtOption {
	return WithDisableMigrate()
}

// WithRequireConfig requires configuration to be present in YAML files.
// If true and no config is found, Register returns an error.
func WithRequireConfig(require bool) ExtOption {
	return func(e *Extension) {
		e.config.RequireConfig = require
	}
}
