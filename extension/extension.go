package extension

import (
	"context"
	"errors"
	"net/http"

	"github.com/xraph/forge"
	"github.com/xraph/vessel"

	"github.com/xraph/relay"
	"github.com/xraph/relay/api"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/store"
)

// ExtensionName is the name registered with Forge.
const ExtensionName = "relay"
const ExtensionDescription = "Composable webhook delivery engine with guaranteed delivery and DLQ"
const ExtensionVersion = "0.1.0"

// Ensure Extension implements forge.Extension at compile time.
var _ forge.Extension = (*Extension)(nil)

// Extension adapts Relay as a Forge extension.
// It implements the forge.Extension interface for full Forge lifecycle integration
// including registration, migration, route mounting, and graceful shutdown.
type Extension struct {
	config Config
	r      *relay.Relay
	api    *api.ForgeAPI
	opts   []relay.Option
}

// New creates a Relay Forge extension with the given options.
func New(opts ...ExtOption) *Extension {
	ext := &Extension{}
	for _, opt := range opts {
		opt(ext)
	}
	return ext
}

// Name returns the extension name.
func (e *Extension) Name() string {
	return ExtensionName
}

// Description returns a human-readable description.
func (e *Extension) Description() string {
	return ExtensionDescription
}

// Version implements [forge.Extension].
func (e *Extension) Version() string {
	return ExtensionVersion
}

// Dependencies returns the list of extension names this extension depends on.
func (e *Extension) Dependencies() []string {
	return []string{}
}

// Relay returns the underlying Relay instance.
// This is nil until Register is called.
func (e *Extension) Relay() *relay.Relay {
	return e.r
}

// API returns the Forge API handler.
func (e *Extension) API() *api.ForgeAPI {
	return e.api
}

// Register implements [forge.Extension].
// It initializes Relay, runs migrations, registers routes, and provides
// the Relay instance to the DI container.
func (e *Extension) Register(fapp forge.App) error {
	if err := e.Init(fapp); err != nil {
		return err
	}

	if err := vessel.Provide(fapp.Container(), func() (*relay.Relay, error) {
		return e.r, nil
	}); err != nil {
		return err
	}

	return nil
}

// Init initializes the extension. In a Forge environment, this is called
// during Register. For standalone use, call it manually.
func (e *Extension) Init(fapp forge.App) error {
	// Build relay options from extension config + user options.
	relayOpts := make([]relay.Option, 0, len(e.opts)+4)
	relayOpts = append(relayOpts, e.opts...)
	relayOpts = append(relayOpts, e.config.ToRelayOptions()...)

	var err error
	e.r, err = relay.New(relayOpts...)
	if err != nil {
		return err
	}

	// Run migrations if not disabled.
	if !e.config.DisableMigrations {
		if err := e.r.Store().Migrate(context.Background()); err != nil {
			return err
		}
	}

	// Set up Forge API.
	e.api = api.NewForgeAPI(e.r.Store(), e.r.Catalog(), e.r.Endpoints(), e.r.DLQ(), e.r)
	if !e.config.DisableRoutes {
		prefix := e.config.Prefix
		if prefix == "" {
			prefix = "/webhooks"
		}
		e.api.RegisterRoutes(fapp.Router().Group(prefix))
	}

	return nil
}

// Start begins the delivery engine.
func (e *Extension) Start(ctx context.Context) error {
	e.r.Start(ctx)
	return nil
}

// Stop gracefully shuts down the delivery engine.
func (e *Extension) Stop(ctx context.Context) error {
	e.r.Stop(ctx)
	return nil
}

// Health implements [forge.Extension].
func (e *Extension) Health(ctx context.Context) error {
	if e.r == nil {
		return errors.New("relay extension not initialized")
	}
	return e.r.Store().Ping(ctx)
}

// Handler returns the standard HTTP handler for standalone use.
// This creates the raw http.Handler without Forge integration.
func (e *Extension) Handler(
	s store.Store,
	cat *catalog.Catalog,
	epSvc *endpoint.Service,
	dlqSvc *dlq.Service,
) http.Handler {
	return api.NewHandler(s, cat, epSvc, dlqSvc, nil)
}

// RegisterRoutes registers all Relay API routes into a Forge router
// with full OpenAPI metadata. Use this for Forge extension integration
// where the parent app owns the router.
func (e *Extension) RegisterRoutes(router forge.Router) {
	e.api.RegisterRoutes(router)
}

// Prefix returns the configured URL prefix.
func (e *Extension) Prefix() string {
	if e.config.Prefix == "" {
		return "/webhooks"
	}
	return e.config.Prefix
}
