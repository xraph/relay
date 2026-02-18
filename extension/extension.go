// Package extension provides the Forge extension for mounting Relay.
//
// The extension integrates Relay into the Forge application framework by:
//   - Auto-discovering the store from Forge's dependency injection container
//   - Starting the delivery engine on application start
//   - Gracefully stopping the engine on application shutdown
//   - Mounting the admin API routes under a configurable prefix
//
// Usage with Forge (once available):
//
//	app := forge.New(
//	    relay.NewExtension(
//	        relay.WithPrefix("/webhooks"),
//	    ),
//	)
package extension

import (
	"log/slog"
	"net/http"

	"github.com/xraph/relay/api"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/store"
)

// Extension is the Forge extension for Relay.
type Extension struct {
	opts   Options
	logger *slog.Logger
}

// Options configures the Relay extension.
type Options struct {
	// Prefix is the URL prefix for the admin API routes (default: "/webhooks").
	Prefix string
}

// Option configures the extension.
type Option func(*Options)

// WithPrefix sets the URL prefix for admin API routes.
func WithPrefix(prefix string) Option {
	return func(o *Options) { o.Prefix = prefix }
}

// NewExtension creates a new Relay Forge extension.
func NewExtension(opts ...Option) *Extension {
	o := Options{Prefix: "/webhooks"}
	for _, opt := range opts {
		opt(&o)
	}
	return &Extension{opts: o, logger: slog.Default()}
}

// Handler creates the admin API handler from the given services.
// This can be used standalone without Forge integration.
func (ext *Extension) Handler(
	s store.Store,
	cat *catalog.Catalog,
	epSvc *endpoint.Service,
	dlqSvc *dlq.Service,
) http.Handler {
	return api.NewHandler(s, cat, epSvc, dlqSvc, ext.logger)
}

// Prefix returns the configured URL prefix.
func (ext *Extension) Prefix() string { return ext.opts.Prefix }
