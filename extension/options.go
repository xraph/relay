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

// WithPrefix sets the URL prefix for all relay webhook routes.
func WithPrefix(prefix string) ExtOption {
	return func(e *Extension) {
		e.config.Prefix = prefix
	}
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

// WithDisableMigrations disables automatic database migration on Register.
func WithDisableMigrations() ExtOption {
	return func(e *Extension) {
		e.config.DisableMigrations = true
	}
}
