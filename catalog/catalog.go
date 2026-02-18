package catalog

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

// Catalog is the in-memory cached service for managing webhook event types.
type Catalog struct {
	store    Store
	cache    map[string]*EventType
	cacheTTL time.Duration
	lastLoad time.Time
	mu       sync.RWMutex
	logger   *slog.Logger
}

// Config configures the catalog service.
type Config struct {
	CacheTTL time.Duration
}

// NewCatalog creates a new Catalog backed by the given store.
func NewCatalog(store Store, cfg Config, logger *slog.Logger) *Catalog {
	if logger == nil {
		logger = slog.Default()
	}
	return &Catalog{
		store:    store,
		cache:    make(map[string]*EventType),
		cacheTTL: cfg.CacheTTL,
		logger:   logger,
	}
}

// RegisterType registers or updates an event type definition.
func (c *Catalog) RegisterType(ctx context.Context, def WebhookDefinition, opts ...RegisterOption) (*EventType, error) {
	ro := registerOptions{}
	for _, o := range opts {
		o(&ro)
	}

	et := &EventType{
		Entity:     entity.New(),
		ID:         id.NewEventTypeID(),
		Definition: def,
		ScopeAppID: ro.scopeAppID,
		Metadata:   ro.metadata,
	}

	if err := c.store.RegisterType(ctx, et); err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.cache[def.Name] = et
	c.mu.Unlock()

	return et, nil
}

// RegisterOption configures RegisterType behavior.
type RegisterOption func(*registerOptions)

type registerOptions struct {
	scopeAppID string
	metadata   map[string]string
}

// WithScopeAppID sets the app scope on a registered event type.
func WithScopeAppID(appID string) RegisterOption {
	return func(o *registerOptions) { o.scopeAppID = appID }
}

// WithMetadata sets metadata on a registered event type.
func WithMetadata(m map[string]string) RegisterOption {
	return func(o *registerOptions) { o.metadata = m }
}

// GetType returns an event type by name, using the cache when available.
func (c *Catalog) GetType(ctx context.Context, name string) (*EventType, error) {
	c.mu.RLock()
	if et, ok := c.cache[name]; ok && !c.cacheExpired() {
		c.mu.RUnlock()
		return et, nil
	}
	c.mu.RUnlock()

	et, err := c.store.GetType(ctx, name)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.cache[name] = et
	c.mu.Unlock()

	return et, nil
}

// ListTypes returns all registered event types.
func (c *Catalog) ListTypes(ctx context.Context, opts ListOpts) ([]*EventType, error) {
	return c.store.ListTypes(ctx, opts)
}

// MatchTypesForEvent returns all non-deprecated event types matching a given event type name.
func (c *Catalog) MatchTypesForEvent(ctx context.Context, eventType string) ([]*EventType, error) {
	return c.store.MatchTypes(ctx, eventType)
}

// DeleteType soft-deletes (deprecates) an event type and removes it from cache.
func (c *Catalog) DeleteType(ctx context.Context, name string) error {
	if err := c.store.DeleteType(ctx, name); err != nil {
		return err
	}

	c.mu.Lock()
	delete(c.cache, name)
	c.mu.Unlock()

	return nil
}

// InvalidateCache clears the in-memory cache, forcing fresh reads from the store.
func (c *Catalog) InvalidateCache() {
	c.mu.Lock()
	c.cache = make(map[string]*EventType)
	c.lastLoad = time.Time{}
	c.mu.Unlock()
}

// cacheExpired returns true if the cache TTL has elapsed. Must be called with at least RLock held.
func (c *Catalog) cacheExpired() bool {
	if c.cacheTTL == 0 {
		return false
	}
	return time.Since(c.lastLoad) > c.cacheTTL
}

// warmCache loads all types from the store into the cache.
func (c *Catalog) warmCache(ctx context.Context) error {
	types, err := c.store.ListTypes(ctx, ListOpts{IncludeDeprecated: false})
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.cache = make(map[string]*EventType, len(types))
	for _, et := range types {
		c.cache[et.Definition.Name] = et
	}
	c.lastLoad = time.Now()
	return nil
}

// WarmCache preloads the cache from the store.
func (c *Catalog) WarmCache(ctx context.Context) error {
	return c.warmCache(ctx)
}
