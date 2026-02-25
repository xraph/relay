package extension

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/kv"
	"github.com/xraph/vessel"

	"github.com/xraph/relay"
	"github.com/xraph/relay/api"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/observability"
	"github.com/xraph/relay/store"
	mongostore "github.com/xraph/relay/store/mongo"
	pgstore "github.com/xraph/relay/store/postgres"
	redisstore "github.com/xraph/relay/store/redis"
	sqlitestore "github.com/xraph/relay/store/sqlite"
)

// ExtensionName is the name registered with Forge.
const ExtensionName = "relay"

// ExtensionDescription is the human-readable description.
const ExtensionDescription = "Composable webhook delivery engine with guaranteed delivery and DLQ"

// ExtensionVersion is the semantic version.
const ExtensionVersion = "0.1.0"

// Ensure Extension implements forge.Extension at compile time.
var _ forge.Extension = (*Extension)(nil)

// Extension adapts Relay as a Forge extension.
// It implements the forge.Extension interface for full Forge lifecycle integration
// including registration, migration, route mounting, and graceful shutdown.
type Extension struct {
	*forge.BaseExtension

	config     Config
	r          *relay.Relay
	api        *api.ForgeAPI
	opts       []relay.Option
	useGrove   bool
	useGroveKV bool
}

// New creates a Relay Forge extension with the given options.
func New(opts ...ExtOption) *Extension {
	ext := &Extension{
		BaseExtension: forge.NewBaseExtension(ExtensionName, ExtensionVersion, ExtensionDescription),
	}
	for _, opt := range opts {
		opt(ext)
	}
	return ext
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
// It loads configuration, initializes Relay, runs migrations, registers routes,
// and provides the Relay instance to the DI container.
func (e *Extension) Register(fapp forge.App) error {
	if err := e.BaseExtension.Register(fapp); err != nil {
		return err
	}

	if err := e.loadConfiguration(); err != nil {
		return err
	}

	if err := e.Init(fapp); err != nil {
		return err
	}

	return vessel.Provide(fapp.Container(), func() (*relay.Relay, error) {
		return e.r, nil
	})
}

// Init initializes the extension. In a Forge environment, this is called
// during Register. For standalone use, call it manually.
func (e *Extension) Init(fapp forge.App) error {
	// Resolve grove database store if configured (takes precedence over grove KV).
	if e.useGrove {
		groveDB, err := e.resolveGroveDB(fapp)
		if err != nil {
			return fmt.Errorf("relay: %w", err)
		}
		s, err := e.buildStoreFromGroveDB(groveDB)
		if err != nil {
			return err
		}
		e.opts = append(e.opts, relay.WithStore(s))
	} else if e.useGroveKV {
		kvStore, err := e.resolveGroveKV(fapp)
		if err != nil {
			return fmt.Errorf("relay: %w", err)
		}
		e.opts = append(e.opts, relay.WithStore(redisstore.New(kvStore)))
	}

	// Build relay options from extension config + user options.
	relayOpts := make([]relay.Option, 0, len(e.opts)+6)
	relayOpts = append(relayOpts, e.opts...)
	relayOpts = append(relayOpts, e.config.ToRelayOptions()...)

	// Wire observability through the forge-managed metrics system.
	relayOpts = append(relayOpts,
		relay.WithMetrics(observability.NewMetrics(fapp.Metrics())),
		relay.WithTracer(observability.NewTracer()),
	)

	var err error
	e.r, err = relay.New(relayOpts...)
	if err != nil {
		return err
	}

	// Run migrations if not disabled.
	if !e.config.DisableMigrate {
		if err := e.r.Store().Migrate(context.Background()); err != nil {
			return err
		}
	}

	// Set up Forge API.
	e.api = api.NewForgeAPI(e.r.Store(), e.r.Catalog(), e.r.Endpoints(), e.r.DLQ(), e.r, fapp.Logger())
	if !e.config.DisableRoutes {
		basePath := e.config.BasePath
		if basePath == "" {
			basePath = "/webhooks"
		}
		e.api.RegisterRoutes(fapp.Router().Group(basePath))
	}

	return nil
}

// Start begins the delivery engine.
func (e *Extension) Start(ctx context.Context) error {
	e.MarkStarted()
	e.r.Start(ctx)
	return nil
}

// Stop gracefully shuts down the delivery engine.
func (e *Extension) Stop(ctx context.Context) error {
	e.r.Stop(ctx)
	e.MarkStopped()
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

// BasePath returns the configured URL base path.
func (e *Extension) BasePath() string {
	if e.config.BasePath == "" {
		return "/webhooks"
	}
	return e.config.BasePath
}

// Prefix returns the configured URL prefix.
//
// Deprecated: Use BasePath instead.
func (e *Extension) Prefix() string {
	return e.BasePath()
}

// --- Config Loading (mirrors grove extension pattern) ---

// loadConfiguration loads config from YAML files or programmatic sources.
func (e *Extension) loadConfiguration() error {
	programmaticConfig := e.config

	// Try loading from config file.
	fileConfig, configLoaded := e.tryLoadFromConfigFile()

	if !configLoaded {
		if programmaticConfig.RequireConfig {
			return errors.New("relay: configuration is required but not found in config files; " +
				"ensure 'extensions.relay' or 'relay' key exists in your config")
		}

		// Use programmatic config merged with defaults.
		e.config = e.mergeWithDefaults(programmaticConfig)
	} else {
		// Config loaded from YAML -- merge with programmatic options.
		e.config = e.mergeConfigurations(fileConfig, programmaticConfig)
	}

	// Enable grove resolution if YAML config specifies grove settings.
	if e.config.GroveDatabase != "" {
		e.useGrove = true
	}
	if e.config.GroveKV != "" {
		e.useGroveKV = true
	}

	e.Logger().Debug("relay: configuration loaded",
		forge.F("disable_routes", e.config.DisableRoutes),
		forge.F("disable_migrate", e.config.DisableMigrate),
		forge.F("base_path", e.config.BasePath),
		forge.F("grove_database", e.config.GroveDatabase),
		forge.F("grove_kv", e.config.GroveKV),
	)

	return nil
}

// tryLoadFromConfigFile attempts to load config from YAML files.
func (e *Extension) tryLoadFromConfigFile() (Config, bool) {
	cm := e.App().Config()
	var cfg Config

	// Try "extensions.relay" first (namespaced pattern).
	if cm.IsSet("extensions.relay") {
		if err := cm.Bind("extensions.relay", &cfg); err == nil {
			e.Logger().Debug("relay: loaded config from file",
				forge.F("key", "extensions.relay"),
			)
			return cfg, true
		}
		e.Logger().Warn("relay: failed to bind extensions.relay config",
			forge.F("error", "bind failed"),
		)
	}

	// Try legacy "relay" key.
	if cm.IsSet("relay") {
		if err := cm.Bind("relay", &cfg); err == nil {
			e.Logger().Debug("relay: loaded config from file",
				forge.F("key", "relay"),
			)
			return cfg, true
		}
		e.Logger().Warn("relay: failed to bind relay config",
			forge.F("error", "bind failed"),
		)
	}

	return Config{}, false
}

// mergeWithDefaults fills zero-valued fields with defaults.
func (e *Extension) mergeWithDefaults(cfg Config) Config {
	defaults := DefaultConfig()
	if cfg.BasePath == "" {
		cfg.BasePath = defaults.BasePath
	}
	if cfg.Concurrency == 0 {
		cfg.Concurrency = defaults.Concurrency
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = defaults.PollInterval
	}
	if cfg.BatchSize == 0 {
		cfg.BatchSize = defaults.BatchSize
	}
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = defaults.RequestTimeout
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = defaults.MaxRetries
	}
	if len(cfg.RetrySchedule) == 0 {
		cfg.RetrySchedule = defaults.RetrySchedule
	}
	if cfg.ShutdownTimeout == 0 {
		cfg.ShutdownTimeout = defaults.ShutdownTimeout
	}
	if cfg.CacheTTL == 0 {
		cfg.CacheTTL = defaults.CacheTTL
	}
	return cfg
}

// mergeConfigurations merges YAML config with programmatic options.
// YAML config takes precedence for most fields; programmatic bool flags fill gaps.
func (e *Extension) mergeConfigurations(yamlConfig, programmaticConfig Config) Config {
	// Programmatic bool flags override when true.
	if programmaticConfig.DisableRoutes {
		yamlConfig.DisableRoutes = true
	}
	if programmaticConfig.DisableMigrate {
		yamlConfig.DisableMigrate = true
	}

	// String fields: YAML takes precedence.
	if yamlConfig.BasePath == "" && programmaticConfig.BasePath != "" {
		yamlConfig.BasePath = programmaticConfig.BasePath
	}
	if yamlConfig.GroveDatabase == "" && programmaticConfig.GroveDatabase != "" {
		yamlConfig.GroveDatabase = programmaticConfig.GroveDatabase
	}
	if yamlConfig.GroveKV == "" && programmaticConfig.GroveKV != "" {
		yamlConfig.GroveKV = programmaticConfig.GroveKV
	}

	// Duration/int fields: YAML takes precedence, programmatic fills gaps.
	if yamlConfig.Concurrency == 0 && programmaticConfig.Concurrency != 0 {
		yamlConfig.Concurrency = programmaticConfig.Concurrency
	}
	if yamlConfig.PollInterval == 0 && programmaticConfig.PollInterval != 0 {
		yamlConfig.PollInterval = programmaticConfig.PollInterval
	}
	if yamlConfig.BatchSize == 0 && programmaticConfig.BatchSize != 0 {
		yamlConfig.BatchSize = programmaticConfig.BatchSize
	}
	if yamlConfig.RequestTimeout == 0 && programmaticConfig.RequestTimeout != 0 {
		yamlConfig.RequestTimeout = programmaticConfig.RequestTimeout
	}
	if yamlConfig.MaxRetries == 0 && programmaticConfig.MaxRetries != 0 {
		yamlConfig.MaxRetries = programmaticConfig.MaxRetries
	}
	if len(yamlConfig.RetrySchedule) == 0 && len(programmaticConfig.RetrySchedule) > 0 {
		yamlConfig.RetrySchedule = programmaticConfig.RetrySchedule
	}
	if yamlConfig.ShutdownTimeout == 0 && programmaticConfig.ShutdownTimeout != 0 {
		yamlConfig.ShutdownTimeout = programmaticConfig.ShutdownTimeout
	}
	if yamlConfig.CacheTTL == 0 && programmaticConfig.CacheTTL != 0 {
		yamlConfig.CacheTTL = programmaticConfig.CacheTTL
	}

	// Fill remaining zeros with defaults.
	return e.mergeWithDefaults(yamlConfig)
}

// resolveGroveDB resolves a *grove.DB from the DI container.
// If GroveDatabase is set, it looks up the named DB; otherwise it uses the default.
func (e *Extension) resolveGroveDB(fapp forge.App) (*grove.DB, error) {
	if e.config.GroveDatabase != "" {
		db, err := vessel.InjectNamed[*grove.DB](fapp.Container(), e.config.GroveDatabase)
		if err != nil {
			return nil, fmt.Errorf("grove database %q not found in container: %w", e.config.GroveDatabase, err)
		}
		return db, nil
	}
	db, err := vessel.Inject[*grove.DB](fapp.Container())
	if err != nil {
		return nil, fmt.Errorf("default grove database not found in container: %w", err)
	}
	return db, nil
}

// buildStoreFromGroveDB constructs the appropriate store backend
// based on the grove driver type (pg, sqlite, mongo).
func (e *Extension) buildStoreFromGroveDB(db *grove.DB) (store.Store, error) {
	driverName := db.Driver().Name()
	switch driverName {
	case "pg":
		return pgstore.New(db), nil
	case "sqlite":
		return sqlitestore.New(db), nil
	case "mongo":
		return mongostore.New(db), nil
	default:
		return nil, fmt.Errorf("relay: unsupported grove driver %q", driverName)
	}
}

// resolveGroveKV resolves a *kv.Store from the DI container.
// If GroveKV is set, it looks up the named store; otherwise it uses the default.
func (e *Extension) resolveGroveKV(fapp forge.App) (*kv.Store, error) {
	if e.config.GroveKV != "" {
		s, err := vessel.InjectNamed[*kv.Store](fapp.Container(), e.config.GroveKV)
		if err != nil {
			return nil, fmt.Errorf("grove kv store %q not found in container: %w", e.config.GroveKV, err)
		}
		return s, nil
	}
	s, err := vessel.Inject[*kv.Store](fapp.Container())
	if err != nil {
		return nil, fmt.Errorf("default grove kv store not found in container: %w", err)
	}
	return s, nil
}
